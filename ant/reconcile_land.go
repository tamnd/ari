package ant

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/permission"
)

// landReconcile turns a writer fan-out's reconcile result into the foreground's
// account of it and, on a clean diff the user approves, applies the combined
// patch to the working tree. A conflict is reported honestly and nothing lands,
// because the colony surfaces a collision rather than guessing a merge (doc 09
// section 6.5). A clean diff faces the same D15 gate a single-ant edit would:
// full-auto and auto-edit apply it without a prompt, plan mode and a headless
// run refuse it, and the default ask mode prompts the foreground once and lands
// only on an allow (doc 09 section 6.4, D15). It returns the preface the
// foreground turn reads, so the worker reports what landed rather than redoing
// the edit itself.
func (r *Runner) landReconcile(ctx context.Context, t *core.TurnHandle, res *colony.ReconcileResult) string {
	if res == nil {
		return ""
	}
	if res.Conflict != nil {
		// The conflict was already journaled by reconcile through the JournalFunc
		// seam; here it becomes the foreground's honest account of the collision.
		return renderConflictPreface(res.Conflict)
	}
	if strings.TrimSpace(res.Diff) == "" {
		return ""
	}
	if !r.approveReconcile(ctx, t, res.Diff) {
		return "Parallel writers produced a combined patch, but it was not approved, so nothing landed. Tell the user the change was declined.\n\n"
	}
	if err := r.applyPatch(ctx, res.Diff); err != nil {
		r.logDebug(t, "applying the reconciled patch: "+err.Error())
		return "Parallel writers' combined patch was approved but did not apply cleanly to the working tree, so nothing landed. Tell the user the merge did not land.\n\n"
	}
	_ = r.emit(event.TypeWorktreeReconciled, string(t.Session), string(t.Turn), event.WorktreeReconciled{
		Files:  res.Applied,
		Landed: true,
	})
	return renderReconciledPreface(res.Applied)
}

// approveReconcile gates the combined patch through the session's permission
// mode. It mirrors the pipeline's mode trichotomy without routing a synthetic
// tool call through it: full-auto and auto-edit approve an in-tree edit, plan
// mode and a headless run (nobody to ask) refuse, and the default ask mode
// emits a diff-consequence permission request and blocks on the same asks broker
// a worker's own prompt waits on (D15, doc 05 section 11).
func (r *Runner) approveReconcile(ctx context.Context, t *core.TurnHandle, diff string) bool {
	if r.headless {
		return false
	}
	mode := permission.Mode(r.config.mode)
	if t.Request.Mode != "" {
		mode = permission.Mode(t.Request.Mode)
	}
	switch mode {
	case permission.ModeFullAuto, permission.ModeAutoEdit:
		return true
	case permission.ModePlan:
		return false
	}
	if r.asks == nil {
		return false
	}
	reqID := "reconcile-" + string(t.Turn)
	_ = r.emit(event.TypePermissionRequested, string(t.Session), string(t.Turn), event.PermissionRequested{
		ID:          reqID,
		Tool:        "colony",
		Call:        "apply combined patch",
		Consequence: event.Consequence{Kind: "diff", Content: diff},
		Mode:        string(mode),
		Reason:      "parallel writers composed one patch; approve it to land their combined work on the tree",
	})
	ans, err := r.asks.Wait(ctx, t.Session, reqID)
	if err != nil {
		return false
	}
	return ans.Decision == core.Allow || ans.Decision == core.AllowSession
}

// applyPatch lands a unified diff on the working tree with git apply. The patch
// is staged to a temp file under the project state dir rather than piped so the
// apply is a plain file argument git can report a clean error against, and the
// file is removed whether or not it applied.
func (r *Runner) applyPatch(ctx context.Context, diff string) error {
	f, err := os.CreateTemp(r.nest.ProjectStateDir(), "reconcile-*.patch")
	if err != nil {
		return fmt.Errorf("staging the patch: %w", err)
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.WriteString(diff); err != nil {
		_ = f.Close()
		return fmt.Errorf("writing the patch: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing the patch: %w", err)
	}
	if out, err := (gitRunner{}).Run(ctx, r.nest.Root, "git", "apply", name); err != nil {
		return fmt.Errorf("git apply: %w: %s", err, strings.TrimSpace(out))
	}
	return nil
}

// renderConflictPreface is the foreground's plain account of a reconcile that
// could not compose. It names the task that collided and the reason, and it
// tells the worker to report the conflict rather than attempt a merge, which is
// the colony's honest-over-clever rule (doc 09 section 6.5).
func renderConflictPreface(c *colony.Conflict) string {
	var b strings.Builder
	b.WriteString("Parallel writers could not compose their patches cleanly. The clean prefix landed; this one did not:\n")
	fmt.Fprintf(&b, "- task %s: %s\n", c.TaskID, c.Reason)
	if len(c.Collided) > 0 {
		fmt.Fprintf(&b, "  it collided with already-landed work from: %s\n", strings.Join(c.Collided, ", "))
	}
	b.WriteString("Report this conflict to the user plainly and do not guess a merge.\n\n")
	return b.String()
}

// renderReconciledPreface is the foreground's note that the combined patch is
// already on the tree, so the worker describes the change rather than reapplying
// it.
func renderReconciledPreface(files []string) string {
	var b strings.Builder
	b.WriteString("Parallel writers finished and their combined patch already landed on the working tree")
	if n := len(files); n > 0 {
		fmt.Fprintf(&b, " (%d patches composed into one diff)", n)
	}
	b.WriteString(". Tell the user what changed and that the edit is already applied; do not reapply it.\n\n")
	return b.String()
}
