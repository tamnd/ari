package ant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// TestForegroundSurveyFansOutAndSynthesizes is the live fan-out path end to
// end: a survey request that names two files decomposes into a surveyor per
// file, the gate approves the read-only split, the two surveys run, and their
// findings ride the foreground worker's prompt tail so it synthesizes them
// instead of reading both files itself. The colony is pre-warmed with one
// survey outcome so the gate's cost model has a class mean to project against,
// the way an ordinary colony accrues one from earlier foreground surveys.
func TestForegroundSurveyFansOutAndSynthesizes(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.go", "server.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := scripted.Response{Text: "the answer lives in the file", Usage: provider.Usage{Input: 10, Output: 5}, Stop: "end_turn"}
	rec := &recorder{inner: scripted.New(done, done, done)}
	r, c := openDispatchColony(t, root, rec)
	ctx := context.Background()

	// One folded survey outcome gives the gate a class mean to project on, the
	// history a fresh colony would earn from its first single-ant survey.
	if err := r.trails.Update(ctx, colony.Outcome{Ant: "worker", Class: colony.ClassSurvey, Success: true, Tokens: 1000}); err != nil {
		t.Fatal(err)
	}

	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, core.SubmitRequest{
		Session: sid,
		Text:    "explain how config.go and server.go work together",
		Mode:    core.ModeFullAuto,
	}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	var approved *event.FanOutApproved
	for _, e := range evs {
		if e.Type != event.TypeFanOutApproved {
			continue
		}
		var fa event.FanOutApproved
		if err := json.Unmarshal(e.Payload, &fa); err != nil {
			t.Fatal(err)
		}
		approved = &fa
	}
	if approved == nil {
		t.Fatal("no colony.fanout.approved event for a two-file survey")
	}
	if approved.Subtasks != 2 {
		t.Errorf("fan-out width = %d, want 2", approved.Subtasks)
	}
	if approved.IndependenceBy != "declared-independent" {
		t.Errorf("independence by %q, want declared-independent for read-only surveys", approved.IndependenceBy)
	}
	if approved.Workload != "read-heavy" {
		t.Errorf("workload %q, want read-heavy", approved.Workload)
	}

	// The surveys' findings reached the foreground worker on its prompt tail.
	if !anyRequestContains(rec.Requests(), "Parallel surveys") {
		t.Error("the foreground worker's prompt carried no survey findings preface")
	}
}

// TestForegroundSurveyRefusalNamesTest is the quiet half of D5 on the live
// path: a two-file survey on a fresh colony decomposes, but with no survey cost
// history the gate refuses on the budget test. The refusal stays off the normal
// stream, but a colony.fanout.refused event names the failing test on the debug
// lane, and no colony.fanout.approved fires, so the turn ran single-ant.
func TestForegroundSurveyRefusalNamesTest(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"config.go", "server.go"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package main\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	done := scripted.Response{Text: "answered single-ant", Usage: provider.Usage{Input: 10, Output: 5}, Stop: "end_turn"}
	_, c := openDispatchColony(t, root, scripted.New(done, done, done))
	ctx := context.Background()

	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, core.SubmitRequest{
		Session: sid,
		Text:    "explain how config.go and server.go work together",
		Mode:    core.ModeFullAuto,
	}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	var refused *event.FanOutRefused
	for _, e := range evs {
		if e.Type == event.TypeFanOutApproved {
			t.Fatal("a fresh colony with no survey cost history must not approve a fan-out")
		}
		if e.Type != event.TypeFanOutRefused {
			continue
		}
		var fr event.FanOutRefused
		if err := json.Unmarshal(e.Payload, &fr); err != nil {
			t.Fatal(err)
		}
		refused = &fr
	}
	if refused == nil {
		t.Fatal("no colony.fanout.refused event for a survey the gate declined")
	}
	if refused.Failed != "budget" {
		t.Errorf("failed test = %q, want budget for a colony with no cost history", refused.Failed)
	}
	if refused.Reason == "" {
		t.Error("a refusal names the failing test but carries no reason")
	}
}

// TestDecomposeSplitsEditIntoWriters is the seam this slice adds: an edit brief
// that names two files splits into one worker per file, each producing a patch
// over a single disjoint anchor, so the writer fan-out has real subtasks to run
// instead of only surveys.
func TestDecomposeSplitsEditIntoWriters(t *testing.T) {
	r := &Runner{}
	brief := colony.TaskBrief{
		Header:  colony.Header{TaskID: "edit-1", SessionID: "s1"},
		Goal:    "fix the leak",
		Class:   colony.ClassEdit,
		Budget:  colony.Budget{Tokens: 60000},
		Anchors: []colony.Anchor{{Kind: colony.AnchorFile, Value: "a.go"}, {Kind: colony.AnchorFile, Value: "b.go"}},
	}
	subs := r.decompose(brief)
	if len(subs) != 2 {
		t.Fatalf("an edit over two files split into %d subtasks, want 2", len(subs))
	}
	for _, s := range subs {
		if s.Deliverable != colony.KindPatch {
			t.Errorf("writer subtask %s produces %q, want a patch", s.TaskID, s.Deliverable)
		}
		if s.DirectedTo != "worker" {
			t.Errorf("writer subtask %s directed to %q, want worker", s.TaskID, s.DirectedTo)
		}
		if s.Class != colony.ClassEdit {
			t.Errorf("writer subtask %s carries class %q, want the parent's edit", s.TaskID, s.Class)
		}
		if len(s.Anchors) != 1 {
			t.Errorf("writer subtask %s has %d anchors, want one file to keep the split disjoint", s.TaskID, len(s.Anchors))
		}
		if !strings.Contains(s.Goal, "focus:") {
			t.Errorf("writer subtask %s goal does not scope to its file: %q", s.TaskID, s.Goal)
		}
	}
	if subs[0].Anchors[0].Value == subs[1].Anchors[0].Value {
		t.Errorf("two writers share the anchor %q; the split is not disjoint", subs[0].Anchors[0].Value)
	}
}

// TestDecomposeLeavesNonSplitClassSingleAnt guards the shape: a class that is
// not a per-file parallel task (here a debug brief) does not decompose even when
// it names several files, so the turn stays single-ant.
func TestDecomposeLeavesNonSplitClassSingleAnt(t *testing.T) {
	r := &Runner{}
	brief := colony.TaskBrief{
		Header:  colony.Header{TaskID: "dbg-1", SessionID: "s1"},
		Goal:    "why does a.go panic",
		Class:   colony.ClassDebug,
		Budget:  colony.Budget{Tokens: 60000},
		Anchors: []colony.Anchor{{Kind: colony.AnchorFile, Value: "a.go"}, {Kind: colony.AnchorFile, Value: "b.go"}},
	}
	if subs := r.decompose(brief); subs != nil {
		t.Errorf("a debug brief decomposed into %d subtasks, want single-ant", len(subs))
	}
}

// TestForegroundEditFansOutToWriters is the live writer path end to end: an edit
// request that names two files decomposes into a worker per file, a proven edit
// specialist carries the gate's workload test, the two writers edit in isolated
// worktrees, and reconcile composes their patches into one diff that lands on the
// working tree, so both files carry the change with a single approval.
func TestForegroundEditFansOutToWriters(t *testing.T) {
	root := t.TempDir()
	initRepo(t, root)
	for _, f := range []struct{ name, body string }{{"a.go", "package a\n"}, {"b.go", "package b\n"}} {
		if err := os.WriteFile(filepath.Join(root, f.name), []byte(f.body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-q", "-m", "two packages")

	r, c := openDispatchColony(t, root, focusEditor{})
	ctx := context.Background()

	// A proven edit specialist gives the gate's workload test its specialist limb,
	// the way an evolved colony would carry a card with a real edge on edits.
	spec := colony.WorkerCard()
	spec.ID = "editor"
	spec.Name = "editor"
	spec.State.Namespace = "editor/main"
	spec.Discovery.Prefers = []colony.TaskClass{colony.ClassEdit}
	if err := r.queen.Register(ctx, spec); err != nil {
		t.Fatal(err)
	}
	for range 9 {
		if err := r.trails.Update(ctx, colony.Outcome{Ant: "editor", Class: colony.ClassEdit, Success: true, Tokens: 2000}); err != nil {
			t.Fatal(err)
		}
	}

	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, core.SubmitRequest{
		Session: sid,
		Text:    "fix the leak in a.go and b.go",
		Mode:    core.ModeFullAuto,
	}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	var approved *event.FanOutApproved
	reconciled := false
	for _, e := range evs {
		switch e.Type {
		case event.TypeFanOutApproved:
			var fa event.FanOutApproved
			if err := json.Unmarshal(e.Payload, &fa); err != nil {
				t.Fatal(err)
			}
			approved = &fa
		case event.TypeWorktreeReconciled:
			reconciled = true
		}
	}
	if approved == nil {
		t.Fatal("no colony.fanout.approved event for a two-file edit")
	}
	if approved.Subtasks != 2 {
		t.Errorf("fan-out width = %d, want 2", approved.Subtasks)
	}
	if approved.Workload != "specialist-advantage" || approved.Specialist != "editor" {
		t.Errorf("workload %q specialist %q, want specialist-advantage/editor", approved.Workload, approved.Specialist)
	}
	if approved.IndependenceBy != "disjoint-anchors" {
		t.Errorf("independence by %q, want disjoint-anchors for two writers", approved.IndependenceBy)
	}
	if !reconciled {
		t.Error("no colony.worktree.reconciled event; the combined patch did not land")
	}
	for _, name := range []string{"a.go", "b.go"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "EDITEDBYWRITER") {
			t.Errorf("%s did not receive the writer's change:\n%s", name, b)
		}
	}
}

// focusEditor is a writer stand-in for the edit fan-out: on a turn whose prompt
// carries a "focus: <path>" line it appends a marker to that file with sh, and on
// its next turn, seeing the command already ran, it finishes. A turn with no
// focus (the foreground worker) just replies, so the same provider backs both the
// writers and the foreground.
type focusEditor struct{}

func (focusEditor) Name() string { return "focus-editor" }

func (focusEditor) Caps() provider.Capabilities {
	return provider.Capabilities{PromptCache: true, UsageReport: true}
}

func (focusEditor) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	if err := ctx.Err(); err != nil {
		return provider.Result{}, err
	}
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Kind == "tool_result" {
				sink.OnText("edited")
				u := provider.Usage{Input: 8, Output: 3}
				sink.OnUsage(u)
				return provider.Result{Usage: u, StopReason: "end_turn", Model: req.Model}, nil
			}
		}
	}
	file := focusFrom(req)
	if file == "" {
		sink.OnText("handled")
		u := provider.Usage{Input: 5, Output: 2}
		sink.OnUsage(u)
		return provider.Result{Usage: u, StopReason: "end_turn", Model: req.Model}, nil
	}
	cmd := "echo EDITEDBYWRITER >> " + file
	call := provider.ToolCall{ID: "c1", Name: "sh", Input: `{"command":` + jsonString(cmd) + `}`}
	sink.OnToolCall(call)
	u := provider.Usage{Input: 12, Output: 6}
	sink.OnUsage(u)
	return provider.Result{Usage: u, StopReason: "tool_use", Model: req.Model}, nil
}

// focusFrom pulls the path out of the first "focus: <path>)" a decomposed goal
// carries, so each writer edits only the file its subtask was scoped to.
func focusFrom(req provider.Request) string {
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			if b.Kind != "text" {
				continue
			}
			i := strings.Index(b.Text, "focus: ")
			if i < 0 {
				continue
			}
			rest := b.Text[i+len("focus: "):]
			if j := strings.IndexByte(rest, ')'); j >= 0 {
				return strings.TrimSpace(rest[:j])
			}
		}
	}
	return ""
}

// anyRequestContains reports whether any recorded request carries the substring
// in any of its message blocks, so a test can assert the fan-out preface
// reached the foreground worker's prompt.
func anyRequestContains(reqs []provider.Request, want string) bool {
	for _, req := range reqs {
		for _, m := range req.Messages {
			for _, b := range m.Blocks {
				if strings.Contains(b.Text, want) {
					return true
				}
			}
		}
	}
	return false
}
