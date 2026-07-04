package ant

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// surveySubtask builds a read-only survey subtask directed at the surveyor,
// anchored to a real file so its finding has a citation to carry.
func surveySubtask(taskID, goal, anchor string) colony.TaskBrief {
	return colony.TaskBrief{
		Header: colony.Header{
			ID:     "brief-" + taskID,
			Kind:   colony.KindTaskBrief,
			From:   "queen",
			TaskID: taskID,
		},
		Goal:        goal,
		Deliverable: colony.KindFinding,
		Class:       colony.ClassSurvey,
		DirectedTo:  "surveyor",
		Budget:      colony.Budget{Tokens: 50000},
		Anchors:     []colony.Anchor{{Kind: colony.AnchorFile, Value: anchor}},
	}
}

// openDispatchColony stands up a bound, started colony with the ant runner and
// the given provider behind both the frontier and cheap tiers, so a foreground
// worker and a surveyor detachment both resolve. It returns the runner and the
// colony so a test can drive dispatch directly and read the board and trails.
func openDispatchColony(t *testing.T, root string, p provider.Provider) (*Runner, *core.Colony) {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())
	reg := provider.NewRegistry()
	reg.AddProvider(p)
	for _, tier := range []string{"frontier", "cheap"} {
		if err := reg.AddTier(tier, []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
			t.Fatal(err)
		}
	}

	r := NewRunner()
	r.GitStatus = func(string) string { return "## main" }
	ctx := context.Background()
	c, err := core.Open(ctx, root,
		core.WithRunner(r),
		core.WithRegistry(reg),
		core.WithConfig(&config.Config{Mode: "full-auto"}),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.Bind(c)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if err := r.ensureBuiltins(ctx); err != nil {
		t.Fatal(err)
	}
	return r, c
}

// surveyPlan writes two package files under root and returns the parent brief
// and the approved plan of two independent read-only surveys over them.
func surveyPlan(t *testing.T, root string) (colony.TaskBrief, *colony.FanOutPlan) {
	t.Helper()
	for _, name := range []string{"config.go", "server.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	parent := colony.TaskBrief{
		Header:      colony.Header{ID: "brief-parent", Kind: colony.KindTaskBrief, From: "queen", TaskID: "parent"},
		Goal:        "survey the two files",
		Deliverable: colony.KindFinding,
		Class:       colony.ClassSurvey,
		Budget:      colony.Budget{Tokens: 200000},
	}
	plan := &colony.FanOutPlan{
		Subtasks: []colony.TaskBrief{
			surveySubtask("sub-a", "what does config.go configure", filepath.Join(root, "config.go")),
			surveySubtask("sub-b", "what does server.go serve", filepath.Join(root, "server.go")),
		},
	}
	return parent, plan
}

// twoFinishes plays two identical finishing turns, one per subtask: the
// surveyor reads nothing and reports a one-line finding, so each loop completes
// in a turn. The usage is what the trail fold meters.
func twoFinishes() *scripted.Provider {
	done := scripted.Response{Text: "the answer lives in the file", Usage: provider.Usage{Input: 10, Output: 5}, Stop: "end_turn"}
	return scripted.New(done, done)
}

// TestDispatchRunsSubtasksConcurrently proves the fan-out execution path: given
// an approved plan of two independent read-only subtasks, dispatch posts them,
// runs a forager per subtask, and gathers the two findings the surveys post to
// the board. Each subtask is directed at the surveyor, so the cards are
// deterministic and the scripted provider answers each with one finishing turn.
func TestDispatchRunsSubtasksConcurrently(t *testing.T) {
	root := t.TempDir()
	r, c := openDispatchColony(t, root, twoFinishes())
	ctx := context.Background()
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := surveyPlan(t, root)

	res, err := r.dispatch(ctx, c.Store(), sid, parent, plan)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(res.Findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(res.Findings))
	}
	for _, f := range res.Findings {
		if f.Summary == "" {
			t.Errorf("finding %s has no summary", f.ID)
		}
		if len(f.Evidence) == 0 {
			t.Errorf("finding %s has no evidence", f.ID)
		}
	}

	// Both subtask goals ended done, not left claimed or failed.
	for _, task := range []string{"sub-a", "sub-b"} {
		fs, err := r.board.Findings(ctx, task)
		if err != nil {
			t.Fatal(err)
		}
		if len(fs) != 1 {
			t.Errorf("subtask %s posted %d findings, want 1", task, len(fs))
		}
	}
}

// TestDispatchFoldsTrailFitness proves the fitness feedback edge: when a
// fan-out's surveys finish, each subtask's cost and outcome fold into the
// trails, so a fresh colony that starts with no cost history has a survey mean
// afterward. Without this the fan-out gate never approves, because its cost
// model refuses a class it has never seen run.
func TestDispatchFoldsTrailFitness(t *testing.T) {
	root := t.TempDir()
	r, c := openDispatchColony(t, root, twoFinishes())
	ctx := context.Background()

	// A fresh colony has no survey cost history: the gate would refuse on it.
	if _, ok, err := r.trails.MeanTokens(ctx, colony.ClassSurvey); err != nil {
		t.Fatal(err)
	} else if ok {
		t.Fatal("survey has a token mean before any survey ran")
	}

	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	parent, plan := surveyPlan(t, root)
	if _, err := r.dispatch(ctx, c.Store(), sid, parent, plan); err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// The two finished surveys leave a positive survey mean the gate can project.
	mean, ok, err := r.trails.MeanTokens(ctx, colony.ClassSurvey)
	if err != nil {
		t.Fatal(err)
	}
	if !ok || mean <= 0 {
		t.Fatalf("survey mean = (%d, %v), want a positive mean after two surveys ran", mean, ok)
	}

	// The surveyor's own trail on the class shows the successes it earned.
	tr, err := r.trails.Load(ctx, "surveyor", colony.ClassSurvey)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Success <= 0 {
		t.Errorf("surveyor survey success = %v, want a positive count", tr.Success)
	}
}
