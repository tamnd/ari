package ant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
)

// stubAsker answers every permission wait with a fixed decision, standing in
// for a client that clicks allow or deny on the reconcile diff.
type stubAsker struct{ decision core.RespondChoice }

func (s stubAsker) Wait(ctx context.Context, sid core.SessionID, request string) (core.RespondRequest, error) {
	return core.RespondRequest{Session: sid, RequestID: request, Decision: s.decision}, nil
}

// cleanReconcile runs the two-writer fan-out and returns the runner, colony,
// session, and the clean reconcile result its patches composed into, so a land
// test starts from a real diff rather than a hand-built one.
func cleanReconcile(t *testing.T) (*Runner, *core.Colony, core.SessionID, *colony.ReconcileResult) {
	t.Helper()
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
	if res.Reconcile == nil || res.Reconcile.Conflict != nil {
		t.Fatalf("want a clean reconcile, got %+v", res.Reconcile)
	}
	return r, c, sid, res.Reconcile
}

// TestLandReconcileAppliesApprovedDiff is the happy path: under full-auto the
// combined patch needs no prompt, so landReconcile applies it to the working
// tree, the reconciled event fires with the landed files, and the preface tells
// the foreground the edit is already applied.
func TestLandReconcileAppliesApprovedDiff(t *testing.T) {
	r, c, sid, res := cleanReconcile(t)
	ctx := context.Background()
	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	handle := core.TurnHandle{Session: sid, Turn: "t1", Store: c.Store()}
	preface := r.landReconcile(ctx, &handle, res)

	for _, f := range []struct{ name, want string }{{"a.txt", "alpha"}, {"b.txt", "bravo"}} {
		b, err := os.ReadFile(filepath.Join(r.nest.Root, f.name))
		if err != nil {
			t.Fatalf("reconciled patch did not land %s: %v", f.name, err)
		}
		if !strings.Contains(string(b), f.want) {
			t.Errorf("%s = %q, want it to contain %q", f.name, b, f.want)
		}
	}
	if !strings.Contains(preface, "already landed") {
		t.Errorf("preface does not report the landing:\n%s", preface)
	}

	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Type != event.TypeWorktreeReconciled {
				continue
			}
			var w event.WorktreeReconciled
			if err := json.Unmarshal(e.Payload, &w); err != nil {
				t.Fatal(err)
			}
			if !w.Landed {
				t.Error("reconciled event reports landed=false after a clean apply")
			}
			if len(w.Files) != 2 {
				t.Errorf("reconciled event names %d files, want 2", len(w.Files))
			}
			return
		case <-deadline:
			t.Fatal("no worktree reconciled event after landing the patch")
		}
	}
}

// TestLandReconcileReportsConflict is the collision path: a reconcile that could
// not compose lands nothing and returns an honest account naming the conflicted
// task, so the foreground reports the clash instead of guessing a merge.
func TestLandReconcileReportsConflict(t *testing.T) {
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
		t.Fatalf("want a conflict, got %+v", res.Reconcile)
	}

	handle := core.TurnHandle{Session: sid, Turn: "t1", Store: c.Store()}
	preface := r.landReconcile(ctx, &handle, res.Reconcile)
	if !strings.Contains(preface, "could not compose") {
		t.Errorf("conflict preface does not report the clash:\n%s", preface)
	}
	if !strings.Contains(preface, res.Reconcile.Conflict.TaskID) {
		t.Errorf("conflict preface does not name the conflicted task %q:\n%s", res.Reconcile.Conflict.TaskID, preface)
	}
	if !strings.Contains(preface, "do not guess a merge") {
		t.Errorf("conflict preface does not tell the worker to hold:\n%s", preface)
	}
	// A conflict lands nothing new on the tree; base.txt is untouched.
	b, err := os.ReadFile(filepath.Join(root, "base.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != "base" {
		t.Errorf("base.txt = %q, want it left at the committed base after a conflict", b)
	}
}

// TestLandReconcileGatesOnAsk is the default-mode path: a clean diff prompts the
// foreground once, lands on an allow, and leaves the tree untouched on a deny,
// so the reconcile faces the same D15 gate a single-ant edit would.
func TestLandReconcileGatesOnAsk(t *testing.T) {
	r, c, sid, res := cleanReconcile(t)
	ctx := context.Background()
	r.config.mode = "ask"
	handle := core.TurnHandle{Session: sid, Turn: "t1", Store: c.Store()}
	aPath := filepath.Join(r.nest.Root, "a.txt")

	r.asks = stubAsker{decision: core.Deny}
	deny := r.landReconcile(ctx, &handle, res)
	if !strings.Contains(deny, "declined") {
		t.Errorf("deny preface does not report the refusal:\n%s", deny)
	}
	if _, err := os.Stat(aPath); !os.IsNotExist(err) {
		t.Errorf("a.txt exists after a denied reconcile: err=%v", err)
	}

	r.asks = stubAsker{decision: core.Allow}
	allow := r.landReconcile(ctx, &handle, res)
	if !strings.Contains(allow, "already landed") {
		t.Errorf("allow preface does not report the landing:\n%s", allow)
	}
	b, err := os.ReadFile(aPath)
	if err != nil {
		t.Fatalf("a.txt did not land after an allow: %v", err)
	}
	if !strings.Contains(string(b), "alpha") {
		t.Errorf("a.txt = %q, want it to contain alpha", b)
	}
}
