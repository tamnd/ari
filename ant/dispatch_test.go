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

// TestDispatchRunsSubtasksConcurrently proves the fan-out execution path: given
// an approved plan of two independent read-only subtasks, dispatch posts them,
// runs a forager per subtask, and gathers the two findings the surveys post to
// the board. Each subtask is directed at the surveyor, so the cards are
// deterministic and the scripted provider answers each with one finishing turn.
func TestDispatchRunsSubtasksConcurrently(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.go", "server.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// Two identical finishing responses, one per subtask: the surveyor reads
	// nothing and reports a one-line finding, so its loop completes in a turn.
	done := scripted.Response{Text: "the answer lives in the file", Usage: provider.Usage{Input: 10, Output: 5}, Stop: "end_turn"}
	p := scripted.New(done, done)

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

	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
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

	findings, err := r.dispatch(ctx, c.Store(), sid, parent, plan)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if len(findings) != 2 {
		t.Fatalf("got %d findings, want 2", len(findings))
	}
	for _, f := range findings {
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
