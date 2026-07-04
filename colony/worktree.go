package colony

import (
	"context"
	"fmt"
	"path/filepath"
)

// EventWorktreeConflict is the journal event name reconcile emits when a Patch
// cannot land on the reconcile branch (doc 09 section 6.5). The kernel does not
// import the journal package, so it names the event and hands the colliding
// task ids to a journal seam the wiring layer supplies.
const EventWorktreeConflict = "colony.worktree.conflict"

// Runner shells one command to completion in a directory and returns its
// combined output. It is the doc 04 sh tool's process runner, injected so the
// colony kernel never imports os/exec and a test can drive a real or a fake
// git (doc 09 section 6.2).
type Runner interface {
	Run(ctx context.Context, dir, name string, args ...string) (string, error)
}

// Worktree is one worker's isolated checkout, detached at BaseRef so it commits
// nothing and its whole output is a diff against that base (doc 09 section 6.2).
type Worktree struct {
	Dir     string
	TaskID  string
	BaseRef string
}

// NeedsWorktree is the mechanical skip rule of doc 09 section 6.3: a worktree
// exists if and only if the deliverable is a Patch. A reader races nothing, so
// it shares the base tree and a checkout for it would be pure waste. The
// decision is a boolean on the deliverable, never a judgement call.
func NeedsWorktree(deliverable Kind) bool { return deliverable == KindPatch }

// Worktrees owns worktree lifecycle for one project. It shells out to the
// user's git through the injected Runner; there is no go-git dependency because
// the user's git is the source of truth and `git worktree list` must show every
// checkout the colony holds (doc 09 section 6.2).
type Worktrees struct {
	repoRoot string
	nestDir  string // the project nest, <root>/.ari
	sh       Runner
}

// NewWorktrees builds the manager. repoRoot is the git repository the worktrees
// branch from; nestDir is the project nest under which they are parked so they
// never appear in the user's tracked tree (doc 09 section 6.2).
func NewWorktrees(repoRoot, nestDir string, sh Runner) *Worktrees {
	return &Worktrees{repoRoot: repoRoot, nestDir: nestDir, sh: sh}
}

// dirFor is where a task's worktree lives: .ari/worktrees/<task-id>, outside
// the repo's tracked tree so it never shows up in git status.
func (w *Worktrees) dirFor(taskID string) string {
	return filepath.Join(w.nestDir, "worktrees", taskID)
}

// Create adds a detached worktree for one writing worker at the task's base
// commit, so every sibling in a fan-out sees the same base and their diffs are
// comparable (doc 09 section 6.2). The checkout is detached because the worker
// commits nothing; its result is the diff Reconcile later composes.
func (w *Worktrees) Create(ctx context.Context, taskID, baseRef string) (Worktree, error) {
	dir := w.dirFor(taskID)
	if _, err := w.sh.Run(ctx, w.repoRoot, "git", "worktree", "add", "--detach", dir, baseRef); err != nil {
		return Worktree{}, fmt.Errorf("worktree add for task %s: %w", taskID, err)
	}
	return Worktree{Dir: dir, TaskID: taskID, BaseRef: baseRef}, nil
}

// Diff returns the worktree's changes against its base, the raw patch a worker
// posts and the salvage a crashed worker leaves behind (doc 09 sections 6.4 and
// 11.1). It stages everything first so a new file is captured, then diffs the
// index against the detached base.
func (w *Worktrees) Diff(ctx context.Context, wt Worktree) (string, error) {
	if _, err := w.sh.Run(ctx, wt.Dir, "git", "add", "-A"); err != nil {
		return "", fmt.Errorf("stage worktree for task %s: %w", wt.TaskID, err)
	}
	out, err := w.sh.Run(ctx, wt.Dir, "git", "diff", "--cached", wt.BaseRef)
	if err != nil {
		return "", fmt.Errorf("diff worktree for task %s: %w", wt.TaskID, err)
	}
	return out, nil
}

// Remove tears a worktree down unconditionally with force plus a prune, so a
// worker that crashed mid-task leaves a checkout that is still removable and
// does not block the survivors (doc 09 section 6.5). A detached worktree has no
// branch, so there is no dangling ref to clean after it.
func (w *Worktrees) Remove(ctx context.Context, wt Worktree) error {
	if _, err := w.sh.Run(ctx, w.repoRoot, "git", "worktree", "remove", "--force", wt.Dir); err != nil {
		return fmt.Errorf("worktree remove for task %s: %w", wt.TaskID, err)
	}
	return w.Prune(ctx)
}

// Prune drops git's administrative record of any worktree whose directory is
// already gone, the sweep's backstop for a checkout removed out from under git
// by a crash (doc 09 section 6.5).
func (w *Worktrees) Prune(ctx context.Context) error {
	if _, err := w.sh.Run(ctx, w.repoRoot, "git", "worktree", "prune"); err != nil {
		return fmt.Errorf("worktree prune: %w", err)
	}
	return nil
}
