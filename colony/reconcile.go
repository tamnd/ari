package colony

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// ReconcilePatch is a Patch paired with the ordering keys reconcile needs: what
// its task depended on, so a dependent lands after its dependency, and when its
// claim was taken, so independents land in a stable order (doc 09 section 6.4).
type ReconcilePatch struct {
	Patch     Patch
	DependsOn []string
	ClaimedAt time.Time
}

// VerifyFunc is the cheap structural check reconcile runs inside the reconcile
// worktree after each apply, so a semantic conflict between two textually
// disjoint patches is caught at the first bad composition rather than at the
// end (doc 09 section 6.4 step 3). A build for a Go tree is the canonical one.
// A nil VerifyFunc skips the check.
type VerifyFunc func(ctx context.Context, dir string) error

// JournalFunc records a colony event. The kernel does not import the journal
// package, so reconcile hands the event name and the colliding task ids to this
// seam and the wiring layer writes them (doc 09 section 6.5). A nil JournalFunc
// drops the event.
type JournalFunc func(name string, taskIDs []string)

// Conflict is what reconcile reports when a Patch cannot join the composition,
// either because git apply --3way could not land it or because it applied clean
// but broke the integration verification. It never guesses a merge: it names
// the patch that failed and the applied prefix it collided with, so the queen
// can send a directed rebase or surface it to the foreground as a Finding (doc
// 09 section 6.5).
type Conflict struct {
	TaskID   string   // the patch that could not join the composition
	Collided []string // the task ids already landed, in apply order
	Reason   string   // human-readable cause
	Diff     string   // the conflicted patch's own diff, for the foreground Finding
}

// ReconcileResult is the outcome of composing a fan-out's patches: the task ids
// that landed cleanly in apply order, the composed diff of that clean prefix
// against the base, and a Conflict when one patch could not join. On the happy
// path Conflict is nil and Diff is the whole composition, which nothing touches
// the user's tree with until the D15 permission prompt (doc 09 section 6.4).
type ReconcileResult struct {
	Applied  []string
	Diff     string
	Conflict *Conflict
}

// Reconcile composes a fan-out's patches onto a throwaway reconcile checkout and
// returns one combined diff or the exact conflict. It orders the patches
// dependency-first then by claim time, applies each with git apply --3way, and
// re-verifies after every apply so the patch that breaks the integration is
// caught at the point it broke, not at the end. The reconcile checkout is
// detached at baseRef and torn down unconditionally, so the user's working tree
// is never touched (doc 09 sections 6.4 and 6.5).
func (w *Worktrees) Reconcile(ctx context.Context, sessionID, baseRef string, patches []ReconcilePatch, verify VerifyFunc, journal JournalFunc) (ReconcileResult, error) {
	ordered := orderPatches(patches)

	dir := filepath.Join(w.nestDir, "worktrees", "reconcile-"+sessionID)
	if _, err := w.sh.Run(ctx, w.repoRoot, "git", "worktree", "add", "--detach", dir, baseRef); err != nil {
		return ReconcileResult{}, fmt.Errorf("reconcile worktree add: %w", err)
	}
	defer func() {
		_, _ = w.sh.Run(ctx, w.repoRoot, "git", "worktree", "remove", "--force", dir)
		_, _ = w.sh.Run(ctx, w.repoRoot, "git", "worktree", "prune")
	}()

	patchFile := filepath.Join(w.nestDir, "worktrees", "reconcile-"+sessionID+".patch")
	defer func() { _ = os.Remove(patchFile) }()

	var applied []string
	var lastGoodDiff string

	fail := func(p ReconcilePatch, reason string) ReconcileResult {
		collided := append([]string(nil), applied...)
		if journal != nil {
			journal(EventWorktreeConflict, append([]string{p.Patch.TaskID}, collided...))
		}
		return ReconcileResult{
			Applied: collided,
			Diff:    lastGoodDiff,
			Conflict: &Conflict{
				TaskID:   p.Patch.TaskID,
				Collided: collided,
				Reason:   reason,
				Diff:     p.Patch.Diff,
			},
		}
	}

	for _, p := range ordered {
		if err := os.WriteFile(patchFile, []byte(p.Patch.Diff), 0o644); err != nil {
			return ReconcileResult{}, fmt.Errorf("write patch for task %s: %w", p.Patch.TaskID, err)
		}
		if _, err := w.sh.Run(ctx, dir, "git", "apply", "--3way", patchFile); err != nil {
			return fail(p, "git apply --3way could not land the patch"), nil
		}
		// Capture the composed diff of everything landed so far before running
		// verification, so a build-breaker's clean prefix is what we ship.
		composed, err := w.Diff(ctx, Worktree{Dir: dir, BaseRef: baseRef})
		if err != nil {
			return ReconcileResult{}, err
		}
		if verify != nil {
			if verr := verify(ctx, dir); verr != nil {
				return fail(p, "patch applied clean but failed integration verification: "+verr.Error()), nil
			}
		}
		applied = append(applied, p.Patch.TaskID)
		lastGoodDiff = composed
		// Reset the index to the base so the next --3way apply reconstructs its
		// merge base cleanly; the working tree keeps everything landed so far.
		if _, err := w.sh.Run(ctx, dir, "git", "reset", "-q"); err != nil {
			return ReconcileResult{}, fmt.Errorf("reset reconcile index after task %s: %w", p.Patch.TaskID, err)
		}
	}

	return ReconcileResult{Applied: applied, Diff: lastGoodDiff}, nil
}

// orderPatches sorts patches dependency-first then by claim time (doc 09 section
// 6.4 step 1): a patch whose task depended on another lands after it, and
// independents land oldest-claim-first with the task id as a final tiebreak so
// the order is deterministic. Dependencies naming absent tasks are ignored, and
// a dependency cycle degrades to pure claim-time order rather than looping.
func orderPatches(patches []ReconcilePatch) []ReconcilePatch {
	present := make(map[string]bool, len(patches))
	for _, p := range patches {
		present[p.Patch.TaskID] = true
	}

	remaining := append([]ReconcilePatch(nil), patches...)
	sort.SliceStable(remaining, func(i, j int) bool {
		if !remaining[i].ClaimedAt.Equal(remaining[j].ClaimedAt) {
			return remaining[i].ClaimedAt.Before(remaining[j].ClaimedAt)
		}
		return remaining[i].Patch.TaskID < remaining[j].Patch.TaskID
	})

	emitted := make(map[string]bool, len(patches))
	var out []ReconcilePatch
	for len(remaining) > 0 {
		progressed := false
		for i, p := range remaining {
			if dependenciesMet(p, present, emitted) {
				out = append(out, p)
				emitted[p.Patch.TaskID] = true
				remaining = append(remaining[:i], remaining[i+1:]...)
				progressed = true
				break
			}
		}
		if !progressed {
			// A cycle among the remaining tasks: emit them in claim-time order
			// so reconcile still makes progress instead of spinning.
			out = append(out, remaining...)
			break
		}
	}
	return out
}

// dependenciesMet reports whether every dependency a patch names that is also
// present in this fan-out has already been emitted.
func dependenciesMet(p ReconcilePatch, present, emitted map[string]bool) bool {
	for _, dep := range p.DependsOn {
		if present[dep] && !emitted[dep] {
			return false
		}
	}
	return true
}
