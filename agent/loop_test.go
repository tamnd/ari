package agent

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
)

func TestMain(m *testing.M) {
	eval.Main(m)
}

// collector gathers emitted events with zeroed clocks, so two runs of
// the same script marshal byte-identically.
type collector struct {
	mu     sync.Mutex
	events []event.Event
}

func (c *collector) emit(t event.Type, payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, event.Event{V: event.SchemaMajor, Type: t, Payload: b})
	return nil
}

// harness wires a Loop to in-memory recorders.
type harness struct {
	loop    *Loop
	events  *collector
	entries *[]session.Entry
	rows    *[]ledger.Row
	sleeps  *[]time.Duration
}

func newHarness(p provider.Provider, tools *tool.Registry) *harness {
	events := &collector{}
	entries := &[]session.Entry{}
	rows := &[]ledger.Row{}
	sleeps := &[]time.Duration{}
	var mu sync.Mutex
	l := &Loop{
		Provider: p,
		Model:    "primary",
		Tools:    tools,
		TC:       &tool.ToolContext{Files: tool.NewFileState()},
		Emit:     events.emit,
		Append: func(e session.Entry) error {
			mu.Lock()
			defer mu.Unlock()
			*entries = append(*entries, e)
			return nil
		},
		Record: func(r ledger.Row) {
			mu.Lock()
			defer mu.Unlock()
			*rows = append(*rows, r)
		},
		Session: "s1",
		Turn:    "t1",
		Limits: Limits{
			Sleep: func(d time.Duration) {
				mu.Lock()
				defer mu.Unlock()
				*sleeps = append(*sleeps, d)
			},
		},
		Now: func() time.Time { return time.Unix(1700000000, 0) },
	}
	return &harness{loop: l, events: events, entries: entries, rows: rows, sleeps: sleeps}
}

// fakeTool is the configurable test tool.
type fakeTool struct {
	tool.Base
	name      string
	safe      bool
	panicSafe bool // IsConcurrencySafe panics
	maxSize   int
	call      func(ctx context.Context, args json.RawMessage, tc *tool.ToolContext, onProgress tool.ProgressFunc) (*tool.Result, error)
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Schema() tool.Schema {
	return tool.Schema{Name: f.name, Description: "test tool", Params: json.RawMessage(`{"type":"object"}`)}
}
func (f *fakeTool) ValidateInput(context.Context, json.RawMessage, *tool.ToolContext) error {
	return nil
}
func (f *fakeTool) IsConcurrencySafe(json.RawMessage) bool {
	if f.panicSafe {
		panic("classifier bug")
	}
	return f.safe
}
func (f *fakeTool) MaxResultSize() int { return f.maxSize }
func (f *fakeTool) Call(ctx context.Context, args json.RawMessage, tc *tool.ToolContext, onProgress tool.ProgressFunc) (*tool.Result, error) {
	if f.call != nil {
		return f.call(ctx, args, tc, onProgress)
	}
	return &tool.Result{Model: "ok:" + f.name}, nil
}

func registry(t *testing.T, tools ...tool.Tool) *tool.Registry {
	t.Helper()
	reg := tool.NewRegistry()
	for _, tl := range tools {
		if err := reg.Register(tl); err != nil {
			t.Fatalf("register %s: %v", tl.Name(), err)
		}
	}
	return reg
}

func call(id, name, input string) provider.ToolCall {
	return provider.ToolCall{ID: id, Name: name, Input: input}
}

func run(t *testing.T, h *harness, prompt string) Outcome {
	t.Helper()
	out, err := h.loop.Run(context.Background(), prompt)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

// TestRunCompleted is the happy path: text, one tool turn, final text.
func TestRunCompleted(t *testing.T) {
	p := scripted.New(
		scripted.Response{
			Text:  "reading",
			Calls: []provider.ToolCall{call("c1", "reader", `{}`)},
			Usage: provider.Usage{Input: 10, Output: 5},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 20, Output: 3}},
	)
	h := newHarness(p, registry(t, &fakeTool{name: "reader", safe: true}))
	out := run(t, h, "do the thing")

	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s, want completed", out.Reason)
	}
	if out.Turns != 2 {
		t.Fatalf("turns = %d, want 2", out.Turns)
	}
	if len(*h.rows) != 2 {
		t.Fatalf("ledger rows = %d, want 2", len(*h.rows))
	}
	// The transcript recorded both assistant messages and the tool result.
	var ants, tools int
	for _, e := range *h.entries {
		switch e.Type {
		case session.EntryAnt:
			ants++
		case session.EntryTool:
			tools++
		}
	}
	if ants != 2 || tools != 1 {
		t.Fatalf("entries: %d ant, %d tool, want 2 and 1", ants, tools)
	}
}

// TestReplayDeterminism runs the same multi-tool script twice through
// fresh loops and asserts a byte-identical event sequence (D23).
func TestReplayDeterminism(t *testing.T) {
	script := func() *scripted.Provider {
		return scripted.New(
			scripted.Response{
				Thinking: "plan",
				Text:     "two reads",
				Calls: []provider.ToolCall{
					call("c1", "reader", `{"f":"a"}`),
					call("c2", "reader", `{"f":"b"}`),
				},
				Usage: provider.Usage{Input: 10, Output: 5},
			},
			scripted.Response{Text: "done", Usage: provider.Usage{Input: 20, Output: 3}},
		)
	}
	reader := func() *tool.Registry {
		return registry(t, &fakeTool{name: "reader", safe: true})
	}

	runOnce := func() []event.Event {
		h := newHarness(script(), reader())
		out := run(t, h, "go")
		if out.Reason != TermCompleted {
			t.Fatalf("reason = %s", out.Reason)
		}
		return h.events.events
	}

	a, b := runOnce(), runOnce()
	if len(a) != len(b) {
		t.Fatalf("event counts differ: %d vs %d", len(a), len(b))
	}
	for i := range a {
		ja, _ := json.Marshal(a[i])
		jb, _ := json.Marshal(b[i])
		if string(ja) != string(jb) {
			t.Fatalf("event %d diverged:\n%s\n%s", i, ja, jb)
		}
	}
	// And the stream has the expected shape, in receive order.
	types := make([]event.Type, len(a))
	for i, e := range a {
		types[i] = e.Type
	}
	want := []event.Type{
		event.TypeThinkingDelta, event.TypeTextDelta, event.TypeTextDelta,
		event.TypeThinkingEnd, event.TypeTextEnd,
		event.TypeToolStart, event.TypeToolStart,
		event.TypeToolEnd, event.TypeToolEnd,
		event.TypeTextDelta, event.TypeTextDelta, event.TypeTextEnd,
	}
	if len(types) != len(want) {
		t.Fatalf("types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d = %s, want %s (all: %v)", i, types[i], want[i], types)
		}
	}
}

// TestMaxTurns stops a tool-calling model at the ceiling.
func TestMaxTurns(t *testing.T) {
	responses := make([]scripted.Response, 3)
	for i := range responses {
		responses[i] = scripted.Response{
			Calls: []provider.ToolCall{call("c", "reader", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		}
	}
	h := newHarness(scripted.New(responses...), registry(t, &fakeTool{name: "reader", safe: true}))
	h.loop.Limits.MaxTurns = 2
	out := run(t, h, "loop forever")
	if out.Reason != TermMaxTurns || out.Turns != 2 {
		t.Fatalf("got %s after %d turns, want max_turns after 2", out.Reason, out.Turns)
	}
}

// TestBudgetExhausted proves the gate is between turns: the turn in
// flight lands, the next one is refused, and the client sees one error.
func TestBudgetExhausted(t *testing.T) {
	p := scripted.New(scripted.Response{
		Calls: []provider.ToolCall{call("c", "reader", `{}`)},
		Usage: provider.Usage{Input: 100, Output: 100},
	})
	h := newHarness(p, registry(t, &fakeTool{name: "reader", safe: true}))
	spent := false
	h.loop.OverBudget = func() bool { return spent }
	h.loop.Record = func(ledger.Row) { spent = true }
	out := run(t, h, "expensive")
	if out.Reason != TermBudgetExhausted || out.Turns != 1 {
		t.Fatalf("got %s after %d turns, want budget_exhausted after 1", out.Reason, out.Turns)
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

// TestCanceledBeforeStart is the clean between-turn cancel.
func TestCanceledBeforeStart(t *testing.T) {
	h := newHarness(scripted.New(), registry(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := h.loop.Run(ctx, "never starts")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Reason != TermCanceled {
		t.Fatalf("reason = %s, want canceled", out.Reason)
	}
	eval.NoLeaks(t)
}

// TestCanceledMidTools cancels while a tool runs: the tool unwinds via
// its context, the abort is recorded, and no goroutine leaks.
func TestCanceledMidTools(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	blocking := &fakeTool{
		name: "blocker",
		call: func(ctx context.Context, _ json.RawMessage, _ *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
			cancel() // the user hit escape while this tool ran
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	p := scripted.New(scripted.Response{
		Calls: []provider.ToolCall{call("c1", "blocker", `{}`), call("c2", "blocker", `{}`)},
		Usage: provider.Usage{Input: 1, Output: 1},
	})
	h := newHarness(p, registry(t, blocking))
	out, err := h.loop.Run(ctx, "cancel me")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.Reason != TermToolsCanceled {
		t.Fatalf("reason = %s, want tools_canceled", out.Reason)
	}
	// Both calls got tool results: one canceled while running, one
	// canceled before it ran, so the transcript shows what was abandoned.
	var toolEnds int
	for _, e := range h.events.events {
		if e.Type == event.TypeToolEnd {
			toolEnds++
		}
	}
	if toolEnds != 2 {
		t.Fatalf("tool end events = %d, want 2", toolEnds)
	}
	eval.NoLeaks(t)
}

// TestSubmitQueueDrained folds a mid-turn prompt in after tool results.
func TestSubmitQueueDrained(t *testing.T) {
	h := newHarness(nil, nil)
	rec := &recordingProvider{inner: scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{call("c1", "reader", `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)}
	h.loop.Provider = rec
	h.loop.Tools = registry(t, &fakeTool{
		name: "reader",
		safe: true,
		call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
			h.loop.Submit("also check the docs")
			return &tool.Result{Model: "ok"}, nil
		},
	})
	out := run(t, h, "first ask")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	// The second request must carry the queued prompt after the tool
	// results, in submission order.
	second := rec.requests[1].Messages
	last := second[len(second)-1]
	if last.Role != "user" || last.Blocks[0].Text != "also check the docs" {
		t.Fatalf("queued prompt not drained; last message: %+v", last)
	}
}

// recordingProvider captures every request for prompt assertions.
type recordingProvider struct {
	mu       sync.Mutex
	inner    provider.Provider
	requests []provider.Request
}

func (r *recordingProvider) Name() string                { return "recording" }
func (r *recordingProvider) Caps() provider.Capabilities { return r.inner.Caps() }
func (r *recordingProvider) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return r.inner.Stream(ctx, req, sink)
}

// TestTransitionsInIsolation drives each handler alone and asserts the
// transition it sets, so the loop reads as a checked state diagram (D6).
func TestTransitionsInIsolation(t *testing.T) {
	h := newHarness(scripted.New(), registry(t))

	t.Run("start", func(t *testing.T) {
		st := &State{next: transStart}
		h.loop.start(context.Background(), st)
		if st.next != transAssemble {
			t.Fatalf("next = %d", st.next)
		}
	})
	t.Run("assemble to callModel", func(t *testing.T) {
		st := &State{next: transAssemble}
		h.loop.assemble(context.Background(), st)
		if st.next != transCallModel {
			t.Fatalf("next = %d", st.next)
		}
	})
	t.Run("assemble at max turns", func(t *testing.T) {
		st := &State{next: transAssemble, turn: 100}
		h.loop.assemble(context.Background(), st)
		if st.next != transTerminate || st.term != TermMaxTurns {
			t.Fatalf("next = %d term = %s", st.next, st.term)
		}
	})
	t.Run("assemble over threshold", func(t *testing.T) {
		hh := newHarness(scripted.New(), registry(t))
		hh.loop.Limits.AutoCompactAt = 1
		st := &State{next: transAssemble, msgs: []provider.Message{{
			Role: "user", Blocks: []provider.MsgBlock{{Kind: "text", Text: "long enough"}},
		}}}
		hh.loop.assemble(context.Background(), st)
		if st.next != transCompact {
			t.Fatalf("next = %d, want compact", st.next)
		}
	})
	t.Run("stopHooks", func(t *testing.T) {
		st := &State{}
		h.loop.stopHooks(context.Background(), st)
		if st.term != TermCompleted || st.next != transTerminate {
			t.Fatalf("term = %s next = %d", st.term, st.next)
		}
	})
	t.Run("retryModel", func(t *testing.T) {
		st := &State{}
		h.loop.retryModel(st)
		if st.modelRetries != 1 || st.next != transCallModel {
			t.Fatalf("retries = %d next = %d", st.modelRetries, st.next)
		}
	})
	t.Run("fallbackModel", func(t *testing.T) {
		hh := newHarness(scripted.New(), registry(t))
		hh.loop.Fallback = "backup"
		st := &State{model: "primary", modelRetries: 4}
		hh.loop.fallbackModel(st)
		if !st.fellBack || st.model != "backup" || st.modelRetries != 0 || st.next != transCallModel {
			t.Fatalf("state after fallback: %+v", st)
		}
		hh.loop.fallbackModel(st)
		if st.term != TermModelError {
			t.Fatalf("second fallback must terminate, term = %s", st.term)
		}
	})
	t.Run("recoverOutput first escalates", func(t *testing.T) {
		st := &State{maxOut: 8000}
		h.loop.recoverOutput(st)
		if st.maxOut != escalatedMaxOutput || st.next != transCallModel {
			t.Fatalf("maxOut = %d next = %d", st.maxOut, st.next)
		}
	})
	t.Run("recoverOutput exhausted", func(t *testing.T) {
		st := &State{outputRetries: maxOutputRetries}
		h.loop.recoverOutput(st)
		if st.term != TermModelError {
			t.Fatalf("term = %s", st.term)
		}
	})
	t.Run("openCircuit", func(t *testing.T) {
		st := &State{}
		h.loop.openCircuit(st)
		if st.term != TermCompactionFailed || st.next != transTerminate {
			t.Fatalf("term = %s next = %d", st.term, st.next)
		}
	})
	t.Run("drainQueue empty", func(t *testing.T) {
		st := &State{}
		h.loop.drainQueue(st)
		if st.next != transAssemble {
			t.Fatalf("next = %d", st.next)
		}
	})
}

// TestConversationPersistsAcrossRuns proves the ant owns one context
// window for the session: a second Run on the same Loop sees the first
// exchange in its request, and per-run recovery counters reset.
func TestConversationPersistsAcrossRuns(t *testing.T) {
	p := scripted.New(
		scripted.Response{Text: "first answer", Usage: provider.Usage{Input: 5, Output: 2}},
		scripted.Response{Text: "second answer", Usage: provider.Usage{Input: 9, Output: 2}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t))

	if out := run(t, h, "first question"); out.Reason != TermCompleted {
		t.Fatalf("run 1 reason = %s", out.Reason)
	}
	if out := run(t, h, "second question"); out.Reason != TermCompleted {
		t.Fatalf("run 2 reason = %s", out.Reason)
	}

	second := rec.requests[1].Messages
	var texts []string
	for _, m := range second {
		for _, b := range m.Blocks {
			texts = append(texts, b.Text)
		}
	}
	want := []string{"first question", "first answer", "second question"}
	if len(texts) != len(want) {
		t.Fatalf("second request carries %v, want %v", texts, want)
	}
	for i := range want {
		if texts[i] != want[i] {
			t.Fatalf("second request carries %v, want %v", texts, want)
		}
	}
}

// TestPrefixPrependedAndStable pins the block-two contract (D14): the
// prefix rides before the tail on every request, byte-identical across
// turns and across runs, and only the tail varies.
func TestPrefixPrependedAndStable(t *testing.T) {
	p := scripted.New(
		scripted.Response{
			Text:  "looking",
			Calls: []provider.ToolCall{call("c1", "reader", `{}`)},
			Usage: provider.Usage{Input: 5, Output: 2},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 9, Output: 2}},
		scripted.Response{Text: "again", Usage: provider.Usage{Input: 9, Output: 2}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t, &fakeTool{name: "reader", safe: true}))
	h.loop.Prefix = []provider.Message{{
		Role: "user",
		Blocks: []provider.MsgBlock{
			{Kind: "text", Text: "<system-reminder>the pinned index</system-reminder>", Cache: true},
		},
	}}

	run(t, h, "task one")
	run(t, h, "task two")

	if len(rec.requests) != 3 {
		t.Fatalf("requests = %d, want 3", len(rec.requests))
	}
	first, _ := json.Marshal(rec.requests[0].Messages[0])
	for i, req := range rec.requests {
		got, _ := json.Marshal(req.Messages[0])
		if string(got) != string(first) {
			t.Fatalf("request %d prefix drifted:\n%s\nwant\n%s", i, got, first)
		}
		if !req.Messages[0].Blocks[0].Cache {
			t.Fatalf("request %d lost the block-two breakpoint", i)
		}
		if req.Messages[1].Blocks[0].Text != "task one" {
			t.Fatalf("request %d tail does not start at the task", i)
		}
	}
}
