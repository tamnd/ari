package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
	"github.com/tamnd/ari/tool"
)

// TestPartition covers the doc 03 section 7 shapes: consecutive safe
// calls coalesce, unsafe calls run alone and act as barriers.
func TestPartition(t *testing.T) {
	reg := registry(t,
		&fakeTool{name: "read", safe: true},
		&fakeTool{name: "edit", safe: false},
	)
	calls := []provider.ToolCall{
		call("1", "read", `{}`), call("2", "read", `{}`), call("3", "read", `{}`),
		call("4", "edit", `{}`), call("5", "edit", `{}`),
	}
	got := partition(reg, calls)
	if len(got) != 3 {
		t.Fatalf("batches = %d, want 3 (one parallel, two serial)", len(got))
	}
	if !got[0].parallel || len(got[0].calls) != 3 {
		t.Fatalf("first batch = %+v, want parallel [0 1 2]", got[0])
	}
	if got[1].parallel || got[2].parallel {
		t.Fatalf("edit batches must be serial: %+v", got[1:])
	}

	// A safe call after a barrier starts a new parallel batch.
	calls = []provider.ToolCall{
		call("1", "read", `{}`), call("2", "edit", `{}`), call("3", "read", `{}`),
	}
	got = partition(reg, calls)
	if len(got) != 3 {
		t.Fatalf("batches = %d, want 3", len(got))
	}
}

// TestPartitionPanicFailsClosed treats a panicking classifier as
// unsafe, so the call serializes instead of crashing the loop (D7).
func TestPartitionPanicFailsClosed(t *testing.T) {
	reg := registry(t,
		&fakeTool{name: "read", safe: true},
		&fakeTool{name: "broken", panicSafe: true},
	)
	calls := []provider.ToolCall{
		call("1", "read", `{}`), call("2", "broken", `{}`), call("3", "read", `{}`),
	}
	got := partition(reg, calls)
	if len(got) != 3 || got[1].parallel {
		t.Fatalf("panicking classifier must serialize its call: %+v", got)
	}
	// An unknown tool is also unsafe by definition.
	if safeConcurrent(reg, call("x", "ghost", `{}`)) {
		t.Fatal("unknown tool must not be concurrency-safe")
	}
}

// TestReceiveOrderEmission finishes a parallel batch out of order and
// asserts tool results and events still land in receive order.
func TestReceiveOrderEmission(t *testing.T) {
	release := make(chan struct{})
	var order []string
	var mu sync.Mutex
	slow := &fakeTool{name: "slow", safe: true, call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		<-release // finishes last on purpose
		mu.Lock()
		order = append(order, "slow")
		mu.Unlock()
		return &tool.Result{Model: "slow done"}, nil
	}}
	fast := &fakeTool{name: "fast", safe: true, call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		mu.Lock()
		order = append(order, "fast")
		mu.Unlock()
		close(release)
		return &tool.Result{Model: "fast done"}, nil
	}}

	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{call("c1", "slow", `{}`), call("c2", "fast", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, slow, fast))
	out := run(t, h, "race them")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if len(order) != 2 || order[0] != "fast" {
		t.Fatalf("completion order = %v, want fast first", order)
	}

	// The events and the tool_result blocks are in receive order anyway.
	var ends []string
	for _, e := range h.events.events {
		if e.Type == event.TypeToolEnd {
			var te event.ToolEnd
			if err := e.Decode(&te); err != nil {
				t.Fatal(err)
			}
			ends = append(ends, te.Call)
		}
	}
	if len(ends) != 2 || ends[0] != "c1" || ends[1] != "c2" {
		t.Fatalf("tool end order = %v, want [c1 c2]", ends)
	}
	msgs := rec.requests[1].Messages
	resultMsg := msgs[len(msgs)-1]
	if resultMsg.Blocks[0].CallID != "c1" || resultMsg.Blocks[1].CallID != "c2" {
		t.Fatalf("tool_result order wrong: %+v", resultMsg.Blocks)
	}
}

// TestModelCorrectableFailures proves an unknown tool, a rejected
// input, and a denied call each become an is_err tool_result the model
// reads, never an error event (doc 04 section 2.2).
func TestModelCorrectableFailures(t *testing.T) {
	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{
				call("c1", "ghost", `{}`),
				call("c2", "guarded", `{}`),
			},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "adjusted", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, &fakeTool{name: "guarded"}))
	h.loop.Decide = func(_ context.Context, tl tool.Tool, _ json.RawMessage, _ string) Verdict {
		return Verdict{Allow: false, Reason: "guarded is blocked in this mode"}
	}
	out := run(t, h, "try bad calls")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	for _, e := range h.events.events {
		if e.Type == event.TypeError {
			t.Fatal("model-correctable failures must not publish error events")
		}
	}
	blocks := rec.requests[1].Messages[len(rec.requests[1].Messages)-1].Blocks
	if !blocks[0].IsErr || !strings.Contains(blocks[0].Text, "unknown tool") {
		t.Fatalf("unknown tool result: %+v", blocks[0])
	}
	if !blocks[1].IsErr || !strings.Contains(blocks[1].Text, "guarded is blocked") {
		t.Fatalf("denied result: %+v", blocks[1])
	}
}

// TestSpillOversizedResult caps a huge tool result with the head-heavy
// preview and surfaces the spill path on the tool end event.
func TestSpillOversizedResult(t *testing.T) {
	big := strings.Repeat("x", 500) + "MIDDLE" + strings.Repeat("y", 500)
	spiller := &fakeTool{name: "spiller", maxSize: 200, call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		return &tool.Result{Model: big}, nil
	}}
	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{call("c1", "spiller", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, spiller))
	h.loop.TC.Spill = tool.NewDiskSpill(t.TempDir())
	out := run(t, h, "big output")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}

	var end event.ToolEnd
	for _, e := range h.events.events {
		if e.Type == event.TypeToolEnd {
			if err := e.Decode(&end); err != nil {
				t.Fatal(err)
			}
		}
	}
	if end.Spilled == "" {
		t.Fatal("tool end must carry the spill path")
	}
	got := rec.requests[1].Messages[len(rec.requests[1].Messages)-1].Blocks[0].Text
	if len(got) >= len(big) {
		t.Fatalf("result was not capped: %d bytes", len(got))
	}
	if !strings.Contains(got, "truncated") || !strings.Contains(got, end.Spilled) {
		t.Fatalf("capped result must point at the spill file:\n%s", got)
	}
	if !strings.HasPrefix(got, "xxx") || !strings.HasSuffix(got, "yyy") {
		t.Fatal("preview must keep the head and the tail")
	}
}

// TestHardFailCancelsSiblings makes the spill store fail for one call
// in a parallel batch: the sibling is canceled, later batches never
// run, and the turn still lands as tool results.
func TestHardFailCancelsSiblings(t *testing.T) {
	big := strings.Repeat("z", 1000)
	spiller := &fakeTool{name: "spiller", safe: true, maxSize: 100, call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		return &tool.Result{Model: big}, nil
	}}
	waiter := &fakeTool{name: "waiter", safe: true, call: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
		<-ctx.Done() // only the sibling cancel releases this
		return nil, ctx.Err()
	}}
	after := &fakeTool{name: "after"}

	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{
				call("c1", "spiller", `{}`),
				call("c2", "waiter", `{}`),
				call("c3", "after", `{}`),
			},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "recovered", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, spiller, waiter, after))
	h.loop.TC.Spill = failSpill{}
	out := run(t, h, "infra failure")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	blocks := rec.requests[1].Messages[len(rec.requests[1].Messages)-1].Blocks
	if !strings.Contains(blocks[0].Text, "spill failed") {
		t.Fatalf("c1: %+v", blocks[0])
	}
	if !blocks[1].IsErr || !strings.Contains(blocks[1].Text, "canceled") {
		t.Fatalf("sibling must read canceled: %+v", blocks[1])
	}
	if !blocks[2].IsErr || !strings.Contains(blocks[2].Text, "canceled before it ran") {
		t.Fatalf("later batch must be skipped: %+v", blocks[2])
	}
}

type failSpill struct{}

func (failSpill) Put(string) (tool.SpillRef, error) {
	return tool.SpillRef{}, fmt.Errorf("disk full")
}

// TestPanickingToolBecomesResult keeps a Call panic inside the slot.
func TestPanickingToolBecomesResult(t *testing.T) {
	bomber := &fakeTool{name: "bomber", call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		panic("boom")
	}}
	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{call("c1", "bomber", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "noted", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, bomber))
	out := run(t, h, "boom")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	got := rec.requests[1].Messages[len(rec.requests[1].Messages)-1].Blocks[0]
	if !got.IsErr || !strings.Contains(got.Text, "tool panicked") {
		t.Fatalf("panic result: %+v", got)
	}
}

// TestProgressOnlySerial streams tool progress on the serial path and
// stays silent for parallel batches.
func TestProgressOnlySerial(t *testing.T) {
	progressing := func(name string, safe bool) *fakeTool {
		return &fakeTool{name: name, safe: safe, call: func(_ context.Context, _ json.RawMessage, _ *tool.ToolContext, onProgress tool.ProgressFunc) (*tool.Result, error) {
			if onProgress != nil {
				onProgress("tick")
			}
			return &tool.Result{Model: "done"}, nil
		}}
	}
	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{
				call("c1", "par", `{}`), call("c2", "par", `{}`), // parallel pair
				call("c3", "ser", `{}`), // serial
			},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	h := newHarness(p, registry(t, progressing("par", true), progressing("ser", false)))
	out := run(t, h, "progress")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	var progress []string
	for _, e := range h.events.events {
		if e.Type == event.TypeToolProgress {
			var tp event.ToolProgress
			if err := e.Decode(&tp); err != nil {
				t.Fatal(err)
			}
			progress = append(progress, tp.Call)
		}
	}
	if len(progress) != 1 || progress[0] != "c3" {
		t.Fatalf("progress events = %v, want only the serial c3", progress)
	}
}

// TestFileStateEffectApplied folds a read's effect into the map and
// tracks recency for the compaction restore.
func TestFileStateEffectApplied(t *testing.T) {
	mtime := time.Unix(1700000000, 0)
	reader := &fakeTool{name: "reader", safe: true, call: func(_ context.Context, args json.RawMessage, _ *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
		var a struct{ F string }
		if err := json.Unmarshal(args, &a); err != nil {
			return nil, err
		}
		return &tool.Result{Model: "content", StateEffect: &tool.FileStateEffect{
			Path: a.F, Hash: "h-" + a.F, Mtime: mtime, Lines: 1,
		}}, nil
	}}
	p := scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{
				call("c1", "reader", `{"F":"/a"}`),
				call("c2", "reader", `{"F":"/b"}`),
				call("c3", "reader", `{"F":"/a"}`),
			},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	h := newHarness(p, registry(t, reader))
	out := run(t, h, "read files")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if h.loop.TC.Files.Hash("/a") != "h-/a" || h.loop.TC.Files.Hash("/b") != "h-/b" {
		t.Fatal("file-state effects not applied")
	}
}
