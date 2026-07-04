package colony

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// execRunner drives the real git through os/exec, the same seam the sh tool
// fills in production. A test file may import os/exec; the kernel may not, which
// is the whole reason Runner is injected.
type execRunner struct{}

func (execRunner) Run(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// gitRepo stands up a real repository with two tracked files on one commit and
// returns its root and the base commit sha every worktree branches from.
func gitRepo(t *testing.T) (root, base string) {
	t.Helper()
	root = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		out, err := execRunner{}.Run(context.Background(), root, "git", args...)
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return out
	}
	run("init", "-q", "-b", "main")
	run("config", "user.email", "tamnd87@gmail.com")
	run("config", "user.name", "Duc-Tam Nguyen")
	run("config", "commit.gpgsign", "false")

	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("bravo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "shared.txt"), []byte("base line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "base")
	base = strings.TrimSpace(run("rev-parse", "HEAD"))
	return root, base
}

// writeIn overwrites a file inside a worktree and returns nothing; the worker's
// edit is a plain file write, which is all a worktree isolates.
func writeIn(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readIn(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestWorktreeIsolatesUncommittedChanges is the DoD that two writers in parallel
// worktrees never see each other's uncommitted changes and the parent tree is
// untouched by either.
func TestWorktreeIsolatesUncommittedChanges(t *testing.T) {
	root, base := gitRepo(t)
	nest := filepath.Join(root, ".ari")
	wm := NewWorktrees(root, nest, execRunner{})
	ctx := context.Background()

	wt1, err := wm.Create(ctx, "t1", base)
	if err != nil {
		t.Fatalf("create t1: %v", err)
	}
	wt2, err := wm.Create(ctx, "t2", base)
	if err != nil {
		t.Fatalf("create t2: %v", err)
	}

	writeIn(t, wt1.Dir, "a.txt", "t1 changed a\n")
	writeIn(t, wt2.Dir, "a.txt", "t2 changed a\n")

	if got := readIn(t, wt1.Dir, "a.txt"); got != "t1 changed a\n" {
		t.Errorf("t1 worktree sees %q, want its own change", got)
	}
	if got := readIn(t, wt2.Dir, "a.txt"); got != "t2 changed a\n" {
		t.Errorf("t2 worktree sees %q, want its own change; the writers are not isolated", got)
	}
	if got := readIn(t, root, "a.txt"); got != "alpha\n" {
		t.Errorf("parent tree a.txt = %q, want the untouched base; a worker leaked into the shared tree", got)
	}

	// Each worker's Diff is its own change and nothing of its sibling's.
	d1, err := wm.Diff(ctx, wt1)
	if err != nil {
		t.Fatalf("diff t1: %v", err)
	}
	if !strings.Contains(d1, "t1 changed a") || strings.Contains(d1, "t2 changed a") {
		t.Errorf("t1 diff = %q, want only t1's change", d1)
	}
}

// TestNeedsWorktree pins the mechanical skip rule: a Patch worker gets a
// worktree, every other deliverable shares the base tree.
func TestNeedsWorktree(t *testing.T) {
	if !NeedsWorktree(KindPatch) {
		t.Error("a patch deliverable must get a worktree")
	}
	for _, k := range []Kind{KindFinding, KindVerdict, KindQuestion, KindTaskBrief} {
		if NeedsWorktree(k) {
			t.Errorf("deliverable %q must not get a worktree; only writers do", k)
		}
	}
}

// TestWorktreeRemovableAfterCrashLeavesNoLitter is the crash and cleanup DoD: a
// worktree whose worker never came back is still removable, and an unchanged
// worktree removed cleanly leaves no directory behind and no dangling branch
// because the checkout was detached.
func TestWorktreeRemovableAfterCrashLeavesNoLitter(t *testing.T) {
	root, base := gitRepo(t)
	nest := filepath.Join(root, ".ari")
	wm := NewWorktrees(root, nest, execRunner{})
	ctx := context.Background()

	wt, err := wm.Create(ctx, "t1", base)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := wm.Remove(ctx, wt); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(wt.Dir); !os.IsNotExist(err) {
		t.Errorf("worktree dir still present after remove: err=%v", err)
	}

	// A detached checkout creates no branch, so nothing dangles after cleanup.
	branches, err := execRunner{}.Run(ctx, root, "git", "branch", "--list")
	if err != nil {
		t.Fatalf("git branch: %v", err)
	}
	if strings.Contains(branches, "t1") {
		t.Errorf("cleanup left a dangling branch: %q", branches)
	}
	list, err := execRunner{}.Run(ctx, root, "git", "worktree", "list")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(list, filepath.Join(".ari", "worktrees", "t1")) {
		t.Errorf("git still tracks the removed worktree: %q", list)
	}
}

// patchFromEdit produces a real Patch by editing files in a scratch worktree and
// diffing it, so reconcile tests apply diffs a worker would actually post.
func patchFromEdit(t *testing.T, wm *Worktrees, base, taskID string, edits map[string]string) Patch {
	t.Helper()
	ctx := context.Background()
	wt, err := wm.Create(ctx, "edit-"+taskID, base)
	if err != nil {
		t.Fatalf("create scratch for %s: %v", taskID, err)
	}
	defer func() { _ = wm.Remove(ctx, wt) }()
	for name, content := range edits {
		writeIn(t, wt.Dir, name, content)
	}
	diff, err := wm.Diff(ctx, wt)
	if err != nil {
		t.Fatalf("diff scratch for %s: %v", taskID, err)
	}
	return Patch{
		Header:  Header{ID: "h-" + taskID, Kind: KindPatch, From: taskID, TaskID: taskID},
		BaseRef: base,
		Diff:    diff,
	}
}

// TestReconcileComposesDisjointPatches is the happy-path DoD: two patches on
// different files land with git apply --3way into one combined diff, no
// conflict, and the composition does not depend on which finished first.
func TestReconcileComposesDisjointPatches(t *testing.T) {
	root, base := gitRepo(t)
	nest := filepath.Join(root, ".ari")
	wm := NewWorktrees(root, nest, execRunner{})
	ctx := context.Background()

	p1 := patchFromEdit(t, wm, base, "t1", map[string]string{"a.txt": "alpha one\n"})
	p2 := patchFromEdit(t, wm, base, "t2", map[string]string{"b.txt": "bravo two\n"})

	rp1 := ReconcilePatch{Patch: p1, ClaimedAt: time.Unix(100, 0)}
	rp2 := ReconcilePatch{Patch: p2, ClaimedAt: time.Unix(200, 0)}

	res, err := wm.Reconcile(ctx, "s1", base, []ReconcilePatch{rp1, rp2}, nil, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Conflict != nil {
		t.Fatalf("disjoint patches conflicted: %+v", res.Conflict)
	}
	if len(res.Applied) != 2 {
		t.Fatalf("applied %v, want both tasks", res.Applied)
	}
	if !strings.Contains(res.Diff, "alpha one") || !strings.Contains(res.Diff, "bravo two") {
		t.Errorf("composed diff missing a change:\n%s", res.Diff)
	}

	// Order independence: the reverse claim order lands the same composition.
	res2, err := wm.Reconcile(ctx, "s2", base, []ReconcilePatch{rp2, rp1}, nil, nil)
	if err != nil {
		t.Fatalf("reconcile reverse: %v", err)
	}
	if res2.Conflict != nil || res2.Diff != res.Diff {
		t.Errorf("composition depends on finish order:\n first:\n%s\n second:\n%s", res.Diff, res2.Diff)
	}
}

// TestReconcileOrdersDependencyBeforeClaimTime proves a dependent patch lands
// after the patch its task depended on, even when it was claimed earlier.
func TestReconcileOrdersDependencyBeforeClaimTime(t *testing.T) {
	root, base := gitRepo(t)
	wm := NewWorktrees(root, filepath.Join(root, ".ari"), execRunner{})

	p1 := patchFromEdit(t, wm, base, "t1", map[string]string{"a.txt": "alpha one\n"})
	p2 := patchFromEdit(t, wm, base, "t2", map[string]string{"b.txt": "bravo two\n"})
	// t2 was claimed first but depends on t1, so t1 must apply first.
	early := ReconcilePatch{Patch: p2, DependsOn: []string{"t1"}, ClaimedAt: time.Unix(100, 0)}
	late := ReconcilePatch{Patch: p1, ClaimedAt: time.Unix(200, 0)}

	ordered := orderPatches([]ReconcilePatch{early, late})
	if ordered[0].Patch.TaskID != "t1" || ordered[1].Patch.TaskID != "t2" {
		t.Errorf("order = %s,%s; want t1 before its dependent t2", ordered[0].Patch.TaskID, ordered[1].Patch.TaskID)
	}
}

// TestReconcileConflictJournalsCollidingTaskIds is the conflict DoD: two patches
// editing the same line cannot both land, and reconcile journals
// colony.worktree.conflict with both task ids, lands the clean prefix, and
// surfaces the conflicted patch rather than guessing a merge.
func TestReconcileConflictJournalsCollidingTaskIds(t *testing.T) {
	root, base := gitRepo(t)
	wm := NewWorktrees(root, filepath.Join(root, ".ari"), execRunner{})
	ctx := context.Background()

	p1 := patchFromEdit(t, wm, base, "t1", map[string]string{"shared.txt": "t1 owns this line\n"})
	p2 := patchFromEdit(t, wm, base, "t2", map[string]string{"shared.txt": "t2 owns this line\n"})
	rp1 := ReconcilePatch{Patch: p1, ClaimedAt: time.Unix(100, 0)}
	rp2 := ReconcilePatch{Patch: p2, ClaimedAt: time.Unix(200, 0)}

	var events [][]string
	journal := func(name string, ids []string) { events = append(events, append([]string{name}, ids...)) }

	res, err := wm.Reconcile(ctx, "s1", base, []ReconcilePatch{rp1, rp2}, nil, journal)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Conflict == nil {
		t.Fatal("two patches on the same line did not conflict")
	}
	if res.Conflict.TaskID != "t2" {
		t.Errorf("conflict task = %s, want the second patch t2", res.Conflict.TaskID)
	}
	if len(res.Applied) != 1 || res.Applied[0] != "t1" {
		t.Errorf("applied = %v, want the clean prefix t1 landed", res.Applied)
	}
	if !strings.Contains(res.Diff, "t1 owns this line") {
		t.Errorf("clean prefix diff missing t1's change:\n%s", res.Diff)
	}
	if len(events) != 1 || events[0][0] != EventWorktreeConflict {
		t.Fatalf("journal events = %v, want one %s", events, EventWorktreeConflict)
	}
	ids := events[0][1:]
	if !slices.Contains(ids, "t1") || !slices.Contains(ids, "t2") {
		t.Errorf("conflict event ids = %v, want both colliding tasks t1 and t2", ids)
	}
}

// TestReconcileCatchesBuildBreaker is the integration-verification DoD: a patch
// that applies clean but fails the post-apply check is caught and reported
// against the patch that broke it, and the clean prefix is what ships.
func TestReconcileCatchesBuildBreaker(t *testing.T) {
	root, base := gitRepo(t)
	wm := NewWorktrees(root, filepath.Join(root, ".ari"), execRunner{})
	ctx := context.Background()

	p1 := patchFromEdit(t, wm, base, "t1", map[string]string{"a.txt": "alpha good\n"})
	p2 := patchFromEdit(t, wm, base, "t2", map[string]string{"b.txt": "BREAK the build\n"})
	rp1 := ReconcilePatch{Patch: p1, ClaimedAt: time.Unix(100, 0)}
	rp2 := ReconcilePatch{Patch: p2, ClaimedAt: time.Unix(200, 0)}

	// The check fails when b.txt carries the breaker, so verification catches
	// the second patch even though it applied without a textual conflict.
	verify := func(_ context.Context, dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, "b.txt"))
		if err != nil {
			return err
		}
		if strings.Contains(string(b), "BREAK") {
			return errBrokeBuild
		}
		return nil
	}

	res, err := wm.Reconcile(ctx, "s1", base, []ReconcilePatch{rp1, rp2}, verify, nil)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if res.Conflict == nil || res.Conflict.TaskID != "t2" {
		t.Fatalf("build breaker not reported against t2: %+v", res.Conflict)
	}
	if !strings.Contains(res.Conflict.Reason, "integration") {
		t.Errorf("conflict reason = %q, want it to name the failed integration check", res.Conflict.Reason)
	}
	if !strings.Contains(res.Diff, "alpha good") || strings.Contains(res.Diff, "BREAK") {
		t.Errorf("shipped diff should be the clean prefix only:\n%s", res.Diff)
	}
}

var errBrokeBuild = &buildError{}

type buildError struct{}

func (*buildError) Error() string { return "go build failed" }
