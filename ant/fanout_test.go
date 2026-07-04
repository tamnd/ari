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
