package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
	"github.com/tamnd/ari/session"
)

func textMsg(role, text string) provider.Message {
	return provider.Message{Role: role, Blocks: []provider.MsgBlock{{Kind: "text", Text: text}}}
}

func resultMsg(id, text string) provider.Message {
	return provider.Message{Role: "user", Blocks: []provider.MsgBlock{{
		Kind: "tool_result", CallID: id, Text: text,
	}}}
}

// TestRungOneClearsOldToolResults reclaims context without a model call
// and keeps the most recent results intact.
func TestRungOneClearsOldToolResults(t *testing.T) {
	h := newHarness(scripted.New(), registry(t))
	h.loop.Limits.KeepToolResults = 2
	st := &State{msgs: []provider.Message{
		textMsg("user", "go"),
		resultMsg("c1", "old one"),
		resultMsg("c2", "old two"),
		resultMsg("c3", "recent one"),
		resultMsg("c4", "recent two"),
	}}
	if !h.loop.clearOldToolResults(st) {
		t.Fatal("expected results to be cleared")
	}
	if st.msgs[1].Blocks[0].Text != clearedResult || st.msgs[2].Blocks[0].Text != clearedResult {
		t.Fatal("old results must be placeholders")
	}
	if st.msgs[3].Blocks[0].Text != "recent one" || st.msgs[4].Blocks[0].Text != "recent two" {
		t.Fatal("recent results must survive")
	}
	// Idempotent: nothing new to clear on the second pass.
	if h.loop.clearOldToolResults(st) {
		t.Fatal("second pass must be a no-op")
	}
}

// TestSummarizeMovesBoundaryAndRestores is the full rung three: the
// boundary moves, the summary becomes the tail, the recent files come
// back, and the file-state map covers exactly what was re-read (D8).
func TestSummarizeMovesBoundaryAndRestores(t *testing.T) {
	dir := t.TempDir()
	fileA := filepath.Join(dir, "a.go")
	fileB := filepath.Join(dir, "b.go")
	if err := os.WriteFile(fileA, []byte("package a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fileB, []byte("package b\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	h := newHarness(scripted.New(
		scripted.Response{Text: "the dense summary", Usage: provider.Usage{Input: 50, Output: 10}},
	), registry(t))
	st := &State{
		msgs: []provider.Message{
			textMsg("user", "go"),
			textMsg("assistant", "worked a lot"),
		},
		recentReads: []string{fileA, fileB},
	}
	// A stale entry that must not survive the restore rebuild.
	h.loop.TC.Files.Set("/stale", "oldhash", h.loop.now(), 1)

	h.loop.compact(t.Context(), st)

	if st.next != transAssemble {
		t.Fatalf("next = %d, want assemble", st.next)
	}
	if st.compactions != 1 || !st.compactedThisTurn {
		t.Fatalf("counters: compactions=%d compactedThisTurn=%v", st.compactions, st.compactedThisTurn)
	}
	if st.boundaryIdx == 0 {
		t.Fatal("boundary did not move")
	}
	tail := st.msgs[st.boundaryIdx:]
	if !strings.Contains(tail[0].Blocks[0].Text, "history compacted") {
		t.Fatalf("tail[0] = %+v, want the marker", tail[0])
	}
	if !strings.Contains(tail[1].Blocks[0].Text, "the dense summary") {
		t.Fatalf("tail[1] = %+v, want the summary", tail[1])
	}
	// Both files were restored, most recent first in the tail.
	rest := tail[2:]
	if len(rest) != 2 {
		t.Fatalf("restored messages = %d, want 2", len(rest))
	}
	if !strings.Contains(rest[0].Blocks[0].Text, fileB) || !strings.Contains(rest[1].Blocks[0].Text, fileA) {
		t.Fatalf("restore order wrong:\n%s\n%s", rest[0].Blocks[0].Text, rest[1].Blocks[0].Text)
	}
	// The file-state map covers exactly the re-read files (D8).
	if h.loop.TC.Files.Hash(fileA) == "" || h.loop.TC.Files.Hash(fileB) == "" {
		t.Fatal("restored files must be readable-for-write again")
	}
	if h.loop.TC.Files.Hash("/stale") != "" {
		t.Fatal("stale entries must not survive the rebuild")
	}
	// The boundary is a session entry, so resume sees it (D9).
	var compacts int
	for _, e := range *h.entries {
		if e.Type == session.EntryCompact {
			compacts++
		}
	}
	if compacts != 1 {
		t.Fatalf("compact entries = %d, want 1", compacts)
	}
}

// TestAutoCompactionMidRun drives compaction through the real loop: a
// low threshold trips assemble into the ladder, the run completes, and
// the model's second request sees the summary, not the raw history.
func TestAutoCompactionMidRun(t *testing.T) {
	long := strings.Repeat("words ", 200)
	p := scripted.New(
		scripted.Response{
			Text:  long,
			Calls: []provider.ToolCall{call("c1", "reader", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "a tight summary", Usage: provider.Usage{Input: 1, Output: 1}}, // the compaction call
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, &fakeTool{name: "reader", safe: true}))
	h.loop.Limits.AutoCompactAt = 100
	out := run(t, h, "start")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	// Request 3 is the post-compaction model turn: its history starts at
	// the marker, so the raw long text is gone.
	final := rec.requests[2].Messages
	if !strings.Contains(final[0].Blocks[0].Text, "history compacted") {
		t.Fatalf("post-compaction request must start at the boundary, got: %.60s", final[0].Blocks[0].Text)
	}
	for _, m := range final {
		for _, b := range m.Blocks {
			if strings.Contains(b.Text, long) {
				t.Fatal("raw history leaked past the boundary")
			}
		}
	}
	if len(*h.rows) != 3 {
		t.Fatalf("ledger rows = %d, want 3 (two turns + compaction)", len(*h.rows))
	}
}

// TestCompactionCircuitBreaker fails the summarize call on three
// separate squeezes and asserts the breaker opens with the clean
// terminal reason instead of looping (D6).
func TestCompactionCircuitBreaker(t *testing.T) {
	fail := scripted.Response{Fail: &provider.Error{Class: provider.ClassTransient, Message: "summarize down"}}
	turn := scripted.Response{
		Calls: []provider.ToolCall{call("c", "reader", `{}`)},
		Usage: provider.Usage{Input: 1, Output: 1},
	}
	// compact fail, model turn, compact fail, model turn, compact fail:
	// three consecutive compaction failures with no success between.
	p := scripted.New(fail, turn, fail, turn, fail)
	h := newHarness(p, registry(t, &fakeTool{name: "reader", safe: true}))
	h.loop.Limits.AutoCompactAt = 1 // every assemble wants compaction
	out := run(t, h, "squeeze")
	if out.Reason != TermCompactionFailed {
		t.Fatalf("reason = %s, want compaction_failed", out.Reason)
	}
	var errs int
	for _, e := range h.events.events {
		if e.Type == event.TypeError {
			errs++
		}
	}
	if errs != 1 {
		t.Fatalf("error events = %d, want exactly 1", errs)
	}
}

// TestReactiveCompaction routes a prompt-too-long provider error into
// the ladder once, then terminates cleanly if it happens again.
func TestReactiveCompaction(t *testing.T) {
	tooLong := scripted.Response{Fail: &provider.Error{Class: provider.ClassPromptTooLong, Message: "413", Status: 413}}
	p := scripted.New(
		tooLong,
		scripted.Response{Text: "summary", Usage: provider.Usage{Input: 1, Output: 1}}, // the reactive compaction
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	h := newHarness(p, registry(t))
	out := run(t, h, "huge prompt")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s, want completed after reactive compaction", out.Reason)
	}
	// The boundary entry records the reactive trigger.
	var trigger string
	for _, e := range *h.entries {
		if e.Type == session.EntryCompact {
			var body CompactBody
			if err := json.Unmarshal(e.Body, &body); err != nil {
				t.Fatal(err)
			}
			trigger = body.Trigger
		}
	}
	if trigger != "reactive" {
		t.Fatalf("trigger = %q, want reactive", trigger)
	}
}

// TestSecondPromptTooLongTerminates: the ladder ran for a 413 and the
// prompt still does not fit, which is the symptom's terminal reason.
func TestSecondPromptTooLongTerminates(t *testing.T) {
	tooLong := scripted.Response{Fail: &provider.Error{Class: provider.ClassPromptTooLong, Message: "413", Status: 413}}
	p := scripted.New(
		tooLong,
		scripted.Response{Text: "summary", Usage: provider.Usage{Input: 1, Output: 1}},
		tooLong, // still does not fit
	)
	h := newHarness(p, registry(t))
	out := run(t, h, "hopeless prompt")
	if out.Reason != TermPromptTooLong {
		t.Fatalf("reason = %s, want prompt_too_long", out.Reason)
	}
}
