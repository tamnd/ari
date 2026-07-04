package ant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// recorder wraps the scripted provider and keeps every request it saw,
// so the cache-alignment test can compare wire bytes across turns.
type recorder struct {
	inner *scripted.Provider

	mu       sync.Mutex
	requests []provider.Request
}

func (r *recorder) Name() string                { return r.inner.Name() }
func (r *recorder) Caps() provider.Capabilities { return r.inner.Caps() }

func (r *recorder) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	r.mu.Unlock()
	return r.inner.Stream(ctx, req, sink)
}

func (r *recorder) Requests() []provider.Request {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]provider.Request(nil), r.requests...)
}

// openWorkerColony opens a colony rooted at root with the ant runner as
// its TurnRunner and the given provider behind the frontier tier.
func openWorkerColony(t *testing.T, root string, p provider.Provider, mem Memory) *core.Colony {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())

	reg := provider.NewRegistry()
	reg.AddProvider(p)
	if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner()
	r.Memory = mem
	r.GitStatus = func(string) string { return "## main" }

	ctx := context.Background()
	c, err := core.Open(ctx, root,
		core.WithRunner(r),
		core.WithRegistry(reg),
		core.WithConfig(&config.Config{Mode: "ask"}),
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
	return c
}

// collect drains a subscription until the given type arrives.
func collect(t *testing.T, sub *core.Subscription, until event.Type) []event.Event {
	t.Helper()
	var out []event.Event
	deadline := time.After(10 * time.Second)
	for {
		select {
		case e := <-sub.C:
			out = append(out, e)
			if e.Type == until {
				return out
			}
		case <-sub.Done():
			return out
		case <-deadline:
			t.Fatalf("timed out waiting for %s; saw %d events", until, len(out))
		}
	}
}

// TestWorkerReadEditVerify runs the one M0 ant end to end against the
// scripted provider: read the file, edit it, verify with sh, finish
// completed, with every model turn metered (plan/01 slice 9).
func TestWorkerReadEditVerify(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(target, []byte("this line has the old marker in it\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// The card's V section names the fixture; loading it through the card
	// is what makes the card a test fixture, not just routing data (D4).
	s, err := eval.LoadScript(colony.WorkerCard().Verify.Fixtures[0])
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]json.RawMessage, len(s.Responses))
	for i, r := range s.Responses {
		raw[i] = json.RawMessage(strings.ReplaceAll(string(r), "%ROOT%", root))
	}
	p, err := scripted.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}

	c := openWorkerColony(t, root, p, nil)
	ctx := context.Background()
	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: s.Prompt, Mode: core.ModeFullAuto}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	// The edit landed on disk.
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "this line has the new marker in it\n"; got != want {
		t.Errorf("hello.txt = %q, want %q", got, want)
	}

	// The event stream shows the read-edit-verify shape, in order.
	var tools []string
	var ledgerTurns int
	for _, e := range evs {
		switch e.Type {
		case event.TypeToolStart:
			var ts event.ToolStart
			if err := json.Unmarshal(e.Payload, &ts); err != nil {
				t.Fatal(err)
			}
			tools = append(tools, ts.Tool)
		case event.TypeLedgerTurn:
			ledgerTurns++
		case event.TypeError:
			t.Errorf("unexpected error event: %s", e.Payload)
		}
	}
	if got, want := strings.Join(tools, " "), "read edit sh"; got != want {
		t.Errorf("tool order = %q, want %q", got, want)
	}

	// Every model turn was metered (D22): four scripted responses, four
	// ledger rows.
	if ledgerTurns != 4 {
		t.Errorf("ledger.turn events = %d, want 4", ledgerTurns)
	}

	// The turn finished clean.
	last := evs[len(evs)-1]
	var fin event.TurnFinished
	if err := json.Unmarshal(last.Payload, &fin); err != nil {
		t.Fatal(err)
	}
	if fin.Reason != "completed" {
		t.Errorf("turn.finished reason = %q, want completed", fin.Reason)
	}
}

// TestBindWiresColonyJournal proves the first seam of the M3 integration:
// Bind constructs the governor against core's Emit through the journal
// closure, and a plain-string kernel record lands on the stream as its
// typed event. It drives the closure directly because dispatch does not
// route through the governor yet; that arrives when the queen decides
// single versus fan-out.
func TestBindWiresColonyJournal(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ARI_HOME", t.TempDir())

	reg := provider.NewRegistry()
	p := scripted.New()
	reg.AddProvider(p)
	if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
		t.Fatal(err)
	}

	r := NewRunner()
	r.GitStatus = func(string) string { return "## main" }
	ctx := context.Background()
	c, err := core.Open(ctx, root,
		core.WithRunner(r),
		core.WithRegistry(reg),
		core.WithConfig(&config.Config{Mode: "ask"}),
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

	if r.governor == nil {
		t.Fatal("Bind did not construct the governor")
	}
	if r.journal == nil {
		t.Fatal("Bind did not wire the journal closure")
	}

	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}

	// A throttle record the governor would raise at max_awake lands as a
	// typed colony.throttle event carrying the deferred task ids.
	r.journal(colony.EventThrottle, []string{"task-7"})
	evs := collect(t, sub, event.TypeColonyThrottle)

	last := evs[len(evs)-1]
	var th event.ColonyThrottle
	if err := json.Unmarshal(last.Payload, &th); err != nil {
		t.Fatal(err)
	}
	if len(th.Tasks) != 1 || th.Tasks[0] != "task-7" {
		t.Errorf("throttle tasks = %v, want [task-7]", th.Tasks)
	}
}

// TestPromptPrefixStableAcrossTurns is the D14 cache-alignment test at
// the session level: across ten turns of one session, the system blocks,
// the tool definitions, and the block-two message serialize identically;
// the only variance is the task tail.
func TestPromptPrefixStableAcrossTurns(t *testing.T) {
	const turns = 10
	responses := make([]scripted.Response, turns)
	for i := range responses {
		responses[i] = scripted.Response{
			Text:  "ok",
			Usage: provider.Usage{Input: 10, Output: 2},
			Stop:  "end_turn",
		}
	}
	rec := &recorder{inner: scripted.New(responses...)}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "ARI.md"), []byte("Always run make check.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mem := stubMemory{index: "- [worker/main] the pinned index rides block two"}
	c := openWorkerColony(t, root, rec, mem)

	ctx := context.Background()
	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	prompts := make([]string, turns)
	for i := range prompts {
		prompts[i] = "question " + string(rune('a'+i))
		if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: prompts[i], Mode: core.ModeFullAuto}); err != nil {
			t.Fatal(err)
		}
		collect(t, sub, event.TypeTurnFinished)
	}

	reqs := rec.Requests()
	if len(reqs) != turns {
		t.Fatalf("provider saw %d requests, want %d", len(reqs), turns)
	}

	system := mustJSON(t, reqs[0].System)
	toolDefs := mustJSON(t, reqs[0].Tools)
	blockTwo := mustJSON(t, reqs[0].Messages[0])
	for i, req := range reqs {
		if got := mustJSON(t, req.System); got != system {
			t.Errorf("turn %d: system blocks changed", i+1)
		}
		if got := mustJSON(t, req.Tools); got != toolDefs {
			t.Errorf("turn %d: tool definitions changed", i+1)
		}
		if got := mustJSON(t, req.Messages[0]); got != blockTwo {
			t.Errorf("turn %d: block two changed mid-session, breaking the cache (D14)", i+1)
		}
		// The task tail is the only variance: the last message is this
		// turn's prompt, and the tail grows by one exchange per turn.
		lastMsg := req.Messages[len(req.Messages)-1]
		if got := lastMsg.Blocks[0].Text; got != prompts[i] {
			t.Errorf("turn %d: last message = %q, want %q", i+1, got, prompts[i])
		}
		if got, want := len(req.Messages), 1+2*i+1; got != want {
			t.Errorf("turn %d: %d messages, want %d (block two plus the growing tail)", i+1, got, want)
		}
	}

	// Both prefix breakpoints are set: after the tools array and at the
	// end of block two (doc 03 section 8).
	last := reqs[turns-1]
	if n := len(last.Tools); n == 0 || !last.Tools[n-1].Cache {
		t.Error("the last tool definition must carry a cache breakpoint")
	}
	b2 := last.Messages[0]
	if !b2.Blocks[len(b2.Blocks)-1].Cache {
		t.Error("block two's last block must carry a cache breakpoint")
	}
	for _, m := range last.Messages[1:] {
		for _, b := range m.Blocks {
			if b.Cache {
				t.Error("the task tail must not carry cache breakpoints")
			}
		}
	}

	// The memory seam and ARI.md actually reached block two.
	text := last.Messages[0].Blocks[0].Text
	if !strings.Contains(text, "the pinned index rides block two") {
		t.Error("the Memory seam's pinned index is missing from block two")
	}
	if !strings.Contains(text, "Always run make check.") {
		t.Error("ARI.md is missing from block two")
	}
}

// TestProjectMemoryRidesBlockTwoNotBlockOne proves the D21/D14 split: the
// merged project memory is injected as the system-reminder-wrapped block
// two, and block one's bytes do not move when memory is present, so the
// cache prefix stays stable no matter what a repo's ARI.md holds.
func TestProjectMemoryRidesBlockTwoNotBlockOne(t *testing.T) {
	const rule = "Always run make check before you push."
	// One root for both runs, so block one's environment line is fixed and
	// the only variable is whether ARI.md exists on disk.
	root := t.TempDir()
	run := func(t *testing.T, withMemory bool) provider.Request {
		t.Helper()
		rec := &recorder{inner: scripted.New(scripted.Response{
			Text:  "ok",
			Usage: provider.Usage{Input: 10, Output: 2},
			Stop:  "end_turn",
		})}
		ari := filepath.Join(root, "ARI.md")
		if withMemory {
			if err := os.WriteFile(ari, []byte(rule+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else if err := os.Remove(ari); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		c := openWorkerColony(t, root, rec, nil)
		ctx := context.Background()
		sub, err := c.Events(ctx, core.EventFilter{})
		if err != nil {
			t.Fatal(err)
		}
		sid, err := c.NewSession(ctx, core.NewSessionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: "go", Mode: core.ModeFullAuto}); err != nil {
			t.Fatal(err)
		}
		collect(t, sub, event.TypeTurnFinished)
		reqs := rec.Requests()
		if len(reqs) == 0 {
			t.Fatal("provider saw no requests")
		}
		return reqs[0]
	}

	with := run(t, true)
	without := run(t, false)

	// Block one is byte-identical whether or not the repo carries memory.
	if a, b := mustJSON(t, with.System), mustJSON(t, without.System); a != b {
		t.Errorf("block one moved with project memory present, breaking the cache (D14):\nwith:    %s\nwithout: %s", a, b)
	}

	// The rule reached block two, wrapped as a system-reminder with the
	// override framing, and never leaked into block one.
	b2 := with.Messages[0].Blocks[0].Text
	if !strings.Contains(b2, rule) {
		t.Errorf("ARI.md rule missing from block two:\n%s", b2)
	}
	if !strings.Contains(b2, "<system-reminder>") {
		t.Errorf("block two is not wrapped as a system-reminder:\n%s", b2)
	}
	if !strings.Contains(b2, "override default behavior") {
		t.Errorf("block two is missing the override framing:\n%s", b2)
	}
	for _, blk := range with.System {
		if strings.Contains(blk.Text, rule) {
			t.Errorf("project memory leaked into block one:\n%s", blk.Text)
		}
	}
}

// stubMemory is the M0 stand-in proving the seam is threaded: the runner
// asks it for the pinned index and renders what it returns.
type stubMemory struct{ index string }

func (s stubMemory) PinnedIndex(ctx context.Context, namespace string) (string, error) {
	return s.index, nil
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
