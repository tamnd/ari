package core

import (
	"context"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// openColony builds a colony over temp dirs: ARI_HOME points the global
// nest at a temp dir and the project root is another.
func openColony(t *testing.T, opts ...Option) *Colony {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	c, err := Open(context.Background(), t.TempDir(), opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

// runnerFunc adapts a func to TurnRunner for scripted tests.
type runnerFunc func(ctx context.Context, h *TurnHandle) error

func (f runnerFunc) RunTurn(ctx context.Context, h *TurnHandle) error { return f(ctx, h) }

// collect drains a subscription until a turn.finished arrives or the
// deadline hits, returning everything seen.
func collect(t *testing.T, sub *Subscription, until event.Type) []event.Event {
	t.Helper()
	var out []event.Event
	deadline := time.After(5 * time.Second)
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

func TestHelloIsFirstWithSchemaAndCursor(t *testing.T) {
	c := openColony(t)
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(context.Background(), EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	select {
	case e := <-sub.C:
		if e.Type != event.TypeHello {
			t.Fatalf("first event = %s, want hello", e.Type)
		}
		var h event.Hello
		if err := e.Decode(&h); err != nil {
			t.Fatal(err)
		}
		if h.Schema != event.SchemaMajor {
			t.Errorf("hello schema = %d", h.Schema)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no hello arrived")
	}
}

func TestSubmitReturnsTurnIDAndAnswerArrivesAsEvents(t *testing.T) {
	c := openColony(t, WithRunner(runnerFunc(func(ctx context.Context, h *TurnHandle) error {
		if err := h.Emit(event.TypeTextDelta, event.TextDelta{Part: 0, Text: "hi there"}); err != nil {
			return err
		}
		return h.Emit(event.TypeTextEnd, event.TextEnd{Part: 0})
	})))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	// Subscribe before creating the session so the stream is
	// deterministic: the journal preserves order, the client sees all.
	sub, err := c.Events(ctx, EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	id, err := c.NewSession(ctx, NewSessionRequest{Title: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	turn, err := c.Submit(ctx, SubmitRequest{Session: id, Text: "say hi"})
	if err != nil {
		t.Fatal(err)
	}
	if turn == "" {
		t.Fatal("Submit returned no TurnID")
	}

	evs := collect(t, sub, event.TypeTurnFinished)
	var types []event.Type
	for _, e := range evs {
		types = append(types, e.Type)
		if e.Seq == 0 && e.Type != event.TypeHello {
			t.Errorf("%s reached a client without a journal seq", e.Type)
		}
	}
	want := []event.Type{event.TypeHello, event.TypeSessionCreated, event.TypeTurnStarted, event.TypeTextDelta, event.TypeTextEnd, event.TypeTurnFinished}
	if len(types) != len(want) {
		t.Fatalf("event types = %v, want %v", types, want)
	}
	for i := range want {
		if types[i] != want[i] {
			t.Fatalf("event %d = %s, want %s", i, types[i], want[i])
		}
	}
	var fin event.TurnFinished
	if err := evs[len(evs)-1].Decode(&fin); err != nil {
		t.Fatal(err)
	}
	if fin.Reason != "done" || fin.ID != string(turn) {
		t.Errorf("turn.finished = %+v", fin)
	}
}

func TestCancelAbortsARunningTurn(t *testing.T) {
	started := make(chan struct{})
	c := openColony(t, WithRunner(runnerFunc(func(ctx context.Context, h *TurnHandle) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	})))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := c.NewSession(ctx, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(ctx, EventFilter{Session: id})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if _, err := c.Submit(ctx, SubmitRequest{Session: id, Text: "block"}); err != nil {
		t.Fatal(err)
	}
	<-started
	if err := c.Cancel(ctx, id); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)
	var fin event.TurnFinished
	if err := evs[len(evs)-1].Decode(&fin); err != nil {
		t.Fatal(err)
	}
	if fin.Reason != "canceled" {
		t.Errorf("reason = %q, want canceled", fin.Reason)
	}
	if err := c.Cancel(ctx, id); err == nil {
		t.Error("cancel with no running turn must error")
	}
}

func TestBusySessionQueuesTheSecondTurn(t *testing.T) {
	gate := make(chan struct{})
	c := openColony(t, WithRunner(runnerFunc(func(ctx context.Context, h *TurnHandle) error {
		select {
		case <-gate:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := c.NewSession(ctx, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(ctx, EventFilter{Session: id, Types: []event.Type{event.TypeTurnStarted, event.TypeTurnFinished}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	t1, err := c.Submit(ctx, SubmitRequest{Session: id, Text: "one"})
	if err != nil {
		t.Fatal(err)
	}
	t2, err := c.Submit(ctx, SubmitRequest{Session: id, Text: "two"})
	if err != nil {
		t.Fatal(err)
	}
	gate <- struct{}{} // finish turn one
	gate <- struct{}{} // finish turn two

	var order []string
	deadline := time.After(5 * time.Second)
	for len(order) < 4 {
		select {
		case e := <-sub.C:
			if e.Type == event.TypeHello {
				continue
			}
			order = append(order, string(e.Type)+":"+e.Turn)
		case <-deadline:
			t.Fatalf("saw only %v", order)
		}
	}
	want := []string{
		"turn.started:" + string(t1), "turn.finished:" + string(t1),
		"turn.started:" + string(t2), "turn.finished:" + string(t2),
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

func TestDefaultRunnerIsHonest(t *testing.T) {
	c := openColony(t)
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := c.NewSession(ctx, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(ctx, EventFilter{Session: id})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	if _, err := c.Submit(ctx, SubmitRequest{Session: id, Text: "hello"}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)
	var sawError bool
	for _, e := range evs {
		if e.Type == event.TypeError {
			var info event.ErrorInfo
			if err := e.Decode(&info); err != nil {
				t.Fatal(err)
			}
			if info.Code != string(ErrInternal) {
				t.Errorf("error code = %q", info.Code)
			}
			sawError = true
		}
	}
	if !sawError {
		t.Error("the not-yet runner must surface an error event")
	}
}

func TestCloseIsIdempotentAndLeakFree(t *testing.T) {
	t.Setenv("ARI_HOME", t.TempDir())
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	c, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(context.Background(), EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	_ = sub
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second close: %v", err)
	}
	eval.NoLeaks(t)
}

func TestSubmitBeforeStartFails(t *testing.T) {
	c := openColony(t)
	_, err := c.Submit(context.Background(), SubmitRequest{Session: "nope", Text: "x"})
	if err == nil {
		t.Fatal("submit on an unstarted colony must fail")
	}
	if KindOf(err) != ErrInternal {
		t.Errorf("kind = %s", KindOf(err))
	}
}

func TestForkThroughTheFacade(t *testing.T) {
	c := openColony(t, WithRunner(runnerFunc(func(ctx context.Context, h *TurnHandle) error { return nil })))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	parent, err := c.NewSession(ctx, NewSessionRequest{Title: "root"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, SubmitRequest{Session: parent, Text: "first"}); err != nil {
		t.Fatal(err)
	}
	child, err := c.NewSession(ctx, NewSessionRequest{Parent: parent, Title: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	tr, err := c.Load(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if tr.Meta.Parent != parent {
		t.Errorf("child parent = %q", tr.Meta.Parent)
	}
	list, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Errorf("listed %d sessions", len(list))
	}
}

// TestFacadeIsTheAPI pins that *Colony satisfies SessionAPI, so the
// interface and the implementation cannot drift apart silently.
func TestFacadeIsTheAPI(t *testing.T) {
	var _ SessionAPI = (*Colony)(nil)
}
