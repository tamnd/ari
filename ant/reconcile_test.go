package ant

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/provider"
)

// writerProvider is a tiny model stand-in for the writer fan-out: on its first
// turn it reads a "RUN: <command>" line out of the brief and asks the sh tool to
// run it, and on its next turn, seeing the command already ran, it finishes. It
// decides on the request's own contents, not a call counter, so two writers
// drawing from the same provider on two goroutines each get the right command
// regardless of how their turns interleave.
type writerProvider struct{}

func (writerProvider) Name() string { return "writer" }

func (writerProvider) Caps() provider.Capabilities {
	return provider.Capabilities{PromptCache: true, UsageReport: true}
}

func (writerProvider) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	if err := ctx.Err(); err != nil {
		return provider.Result{}, err
	}
	// A tool result already in the history means the command ran; finish.
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Kind == "tool_result" {
				sink.OnText("done")
				u := provider.Usage{Input: 8, Output: 3}
				sink.OnUsage(u)
				return provider.Result{Usage: u, StopReason: "end_turn", Model: req.Model}, nil
			}
		}
	}
	cmd := runLineFrom(req)
	if cmd == "" {
		sink.OnText("nothing to do")
		u := provider.Usage{Input: 5, Output: 2}
		sink.OnUsage(u)
		return provider.Result{Usage: u, StopReason: "end_turn", Model: req.Model}, nil
	}
	call := provider.ToolCall{ID: "c1", Name: "sh", Input: `{"command":` + jsonString(cmd) + `}`}
	sink.OnToolCall(call)
	u := provider.Usage{Input: 12, Output: 6}
	sink.OnUsage(u)
	return provider.Result{Usage: u, StopReason: "tool_use", Model: req.Model}, nil
}

// runLineFrom pulls the command off the first "RUN:" line in any user text.
func runLineFrom(req provider.Request) string {
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Kind != "text" {
				continue
			}
			for _, line := range strings.Split(b.Text, "\n") {
				if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "RUN:"); ok {
					return strings.TrimSpace(rest)
				}
			}
		}
	}
	return ""
}

// jsonString quotes a string for embedding in a tool call's raw JSON input.
func jsonString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// initRepo turns dir into a git repository with one committed base file, the
// shape a writer fan-out branches its worktrees from.
func initRepo(t *testing.T, dir string) {
	t.Helper()
	runGit(t, dir, "init", "-q")
	runGit(t, dir, "config", "user.email", "ant@test")
	runGit(t, dir, "config", "user.name", "ant")
	if err := os.WriteFile(filepath.Join(dir, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", "-A")
	runGit(t, dir, "commit", "-q", "-m", "base")
}

// writerSubtask builds a patch subtask directed at the generalist worker, whose
// goal carries the RUN line the writer provider executes in its worktree.
func writerSubtask(taskID, run string) colony.TaskBrief {
	return colony.TaskBrief{
		Header:      colony.Header{ID: "brief-" + taskID, Kind: colony.KindTaskBrief, From: "queen", TaskID: taskID},
		Goal:        "make the change\n\nRUN: " + run,
		Deliverable: colony.KindPatch,
		Class:       colony.ClassEdit,
		DirectedTo:  "worker",
		Budget:      colony.Budget{Tokens: 50000},
	}
}

// worktreeCount reports how many worktrees git tracks for the repo, so a test
// can prove every per-writer checkout was torn down after the fan-out.
func worktreeCount(t *testing.T, dir string) int {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "worktree", "list", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("worktree list: %v\n%s", err, out)
	}
	return strings.Count(string(out), "worktree ")
}

// TestDispatchReconcilesWriterPatches is the writer half of the fan-out: two
// patch subtasks each run in their own worktree off a shared base, and dispatch
// composes their diffs into one clean patch. Each writer adds a distinct file,
// so the two patches compose without a conflict, and every worktree is removed
// once its diff is captured.
func TestDispatchReconcilesWriterPatches(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	r, c := openDispatchColony(t, root, writerProvider{})
	ctx := context.Background()
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	parent := colony.TaskBrief{
		Header:      colony.Header{ID: "brief-parent", Kind: colony.KindTaskBrief, From: "queen", TaskID: "parent"},
		Goal:        "split the edit two ways",
		Deliverable: colony.KindPatch,
		Class:       colony.ClassEdit,
		Budget:      colony.Budget{Tokens: 100000},
	}
	plan := &colony.FanOutPlan{Subtasks: []colony.TaskBrief{
		writerSubtask("w-a", "echo alpha > a.txt"),
		writerSubtask("w-b", "echo bravo > b.txt"),
	}}

	res, err := r.dispatch(ctx, c.Store(), sid, "t1", parent, plan)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.Reconcile == nil {
		t.Fatal("a writer fan-out produced no reconcile result")
	}
	if res.Reconcile.Conflict != nil {
		t.Fatalf("two disjoint patches conflicted: %+v", res.Reconcile.Conflict)
	}
	if len(res.Reconcile.Applied) != 2 {
		t.Fatalf("applied %d patches, want 2", len(res.Reconcile.Applied))
	}
	if !strings.Contains(res.Reconcile.Diff, "a.txt") || !strings.Contains(res.Reconcile.Diff, "b.txt") {
		t.Errorf("composed diff missing one of the files:\n%s", res.Reconcile.Diff)
	}
	if !strings.Contains(res.Reconcile.Diff, "alpha") || !strings.Contains(res.Reconcile.Diff, "bravo") {
		t.Errorf("composed diff missing one of the changes:\n%s", res.Reconcile.Diff)
	}

	// Every per-writer worktree was torn down; only the main worktree remains.
	if n := worktreeCount(t, root); n != 1 {
		t.Errorf("git tracks %d worktrees after the fan-out, want 1 (the main tree)", n)
	}
}

// TestDispatchReconcileReportsConflict is the collision path: two writers that
// change the same file in incompatible ways cannot compose, so reconcile lands
// the first and reports the second as a conflict rather than guessing a merge.
func TestDispatchReconcileReportsConflict(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	r, c := openDispatchColony(t, root, writerProvider{})
	ctx := context.Background()
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	parent := colony.TaskBrief{
		Header:      colony.Header{ID: "brief-parent", Kind: colony.KindTaskBrief, From: "queen", TaskID: "parent"},
		Goal:        "two edits to the same file",
		Deliverable: colony.KindPatch,
		Class:       colony.ClassEdit,
		Budget:      colony.Budget{Tokens: 100000},
	}
	plan := &colony.FanOutPlan{Subtasks: []colony.TaskBrief{
		writerSubtask("w-a", "echo alpha > base.txt"),
		writerSubtask("w-b", "echo bravo > base.txt"),
	}}

	res, err := r.dispatch(ctx, c.Store(), sid, "t1", parent, plan)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.Reconcile == nil || res.Reconcile.Conflict == nil {
		t.Fatalf("two clashing patches did not report a conflict: %+v", res.Reconcile)
	}
	if len(res.Reconcile.Applied) != 1 {
		t.Errorf("applied %d patches before the conflict, want 1", len(res.Reconcile.Applied))
	}
	if n := worktreeCount(t, root); n != 1 {
		t.Errorf("git tracks %d worktrees after the fan-out, want 1", n)
	}
}
