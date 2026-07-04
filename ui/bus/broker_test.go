package bus

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// TestLossyDropsMustDeliverSurvives is the lane contract (doc 02
// section 3.2): with a full subscriber buffer, lossy deltas drop and
// count, while turn.finished and permission.requested always land.
func TestLossyDropsMustDeliverSurvives(t *testing.T) {
	b := NewWithDeadline[tea.Msg](2 * time.Second)
	sub := b.Subscribe(4)
	defer sub.Cancel()

	// Nobody reads yet: flood the lossy lane far past the buffer.
	for i := range 100 {
		b.Publish(Lossy, TextDeltaMsg{TextDelta: event.TextDelta{Text: fmt.Sprint(i)}})
	}
	if b.Dropped() != 96 {
		t.Errorf("dropped = %d, want 96 (100 published into a buffer of 4)", b.Dropped())
	}

	// Must-deliver publishes block until the reader catches up.
	got := make(chan tea.Msg, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := range sub.C {
			if _, isDelta := m.(TextDeltaMsg); isDelta {
				continue
			}
			got <- m
		}
	}()
	b.Publish(MustDeliver, TurnFinishedMsg{TurnFinished: event.TurnFinished{Reason: "done"}})
	b.Publish(MustDeliver, PermissionRequestedMsg{})

	if m := <-got; fmt.Sprintf("%T", m) != "bus.TurnFinishedMsg" {
		t.Errorf("first must-deliver = %T, want TurnFinishedMsg", m)
	}
	if m := <-got; fmt.Sprintf("%T", m) != "bus.PermissionRequestedMsg" {
		t.Errorf("second must-deliver = %T, want PermissionRequestedMsg", m)
	}
	close(sub.C)
	<-done
}

// TestMustDeliverGivesUpOnWedgedSubscriber: the deadline bounds how long
// a dead subscriber can hold the publisher, so the UI cannot stall the
// core (doc 02 section 3.1).
func TestMustDeliverGivesUpOnWedgedSubscriber(t *testing.T) {
	b := NewWithDeadline[tea.Msg](10 * time.Millisecond)
	sub := b.Subscribe(0) // unbuffered, never read
	defer sub.Cancel()

	start := time.Now()
	b.Publish(MustDeliver, TurnFinishedMsg{})
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("publish blocked %v; the deadline did not bound it", elapsed)
	}
}

// TestGoldenReplay drives one event of every M0 type through the ToMsg
// table and goldens the resulting message type and lane, so adding an
// event type without deciding its UI mapping fails the build (D23).
func TestGoldenReplay(t *testing.T) {
	events := []struct {
		typ     event.Type
		payload any
	}{
		{event.TypeHello, event.Hello{}},
		{event.TypeSessionCreated, event.SessionCreated{ID: "s1"}},
		{event.TypeSessionUpdated, event.SessionUpdated{ID: "s1"}},
		{event.TypeSessionForked, event.SessionForked{ID: "s2"}},
		{event.TypeTurnStarted, event.TurnStarted{ID: "t1", Ant: "worker", Prompt: "hi"}},
		{event.TypeTextDelta, event.TextDelta{Part: 0, Text: "hel"}},
		{event.TypeTextEnd, event.TextEnd{Part: 0}},
		{event.TypeThinkingDelta, event.ThinkingDelta{Part: 1}},
		{event.TypeThinkingEnd, event.ThinkingEnd{Part: 1}},
		{event.TypeToolStart, event.ToolStart{Part: 2, Call: "c1", Tool: "read"}},
		{event.TypeToolProgress, event.ToolProgress{Part: 2}},
		{event.TypeToolEnd, event.ToolEnd{Part: 2, Call: "c1"}},
		{event.TypePermissionRequested, event.PermissionRequested{Call: "c2", Tool: "sh"}},
		{event.TypePermissionResolved, event.PermissionResolved{ID: "p1", Behavior: "allow"}},
		{event.TypeLedgerTurn, event.LedgerTurn{Turn: "t1", Model: "m"}},
		{event.TypeLog, event.Log{Level: "debug", Text: "x"}},
		{event.TypeError, event.ErrorInfo{Message: "boom"}},
		{event.TypeTurnFinished, event.TurnFinished{ID: "t1", Reason: "done"}},
		{event.TypeWorkerWoke, event.WorkerWoke{Ant: "forager-0", Task: "sub-a", Tier: "cheap"}},
		{event.TypeWorkerBlocked, event.WorkerBlocked{Ant: "forager-0", Task: "sub-a", Question: "which module?"}},
		{event.TypeWorkerFinished, event.WorkerFinished{Ant: "forager-0", Task: "sub-a", OK: true}},
		{event.TypeColonyProgress, event.ColonyProgress{Ant: "forager-0", Task: "sub-a", Tokens: 1200}},
		// Defined but never emitted in M0: the table must skip, not map.
		{event.TypeAntSpawned, event.AntSpawned{}},
		{event.TypeRouteDecided, event.RouteDecided{}},
		{event.TypeMemoryFolded, event.MemoryFolded{}},
	}

	var b strings.Builder
	for _, tc := range events {
		e, err := event.New(tc.typ, "s1", "t1", tc.payload)
		if err != nil {
			t.Fatal(err)
		}
		msg, lane, ok := ToMsg(e)
		if !ok {
			fmt.Fprintf(&b, "%-24s -> (skipped)\n", tc.typ)
			continue
		}
		laneName := "must-deliver"
		if lane == Lossy {
			laneName = "lossy"
		}
		fmt.Fprintf(&b, "%-24s -> %-26T %s\n", tc.typ, msg, laneName)
	}
	eval.Golden(t, "tomsg_table", b.String())
}

// TestPumpEndToEnd wires a fake core channel through Pump and Drain and
// asserts the messages arrive typed, in order, with the meta attached.
func TestPumpEndToEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src := make(chan event.Event, 8)
	b := New[tea.Msg]()
	sub := b.Subscribe(8)
	defer sub.Cancel()
	go Pump(ctx, src, b)

	var got []tea.Msg
	done := make(chan struct{})
	go Drain(ctx, sub, func(m tea.Msg) {
		got = append(got, m)
		if len(got) == 2 {
			close(done)
		}
	})

	e1, _ := event.New(event.TypeTextDelta, "s1", "t1", event.TextDelta{Text: "hi"})
	e1.Seq = 7
	e2, _ := event.New(event.TypeTurnFinished, "s1", "t1", event.TurnFinished{Reason: "done"})
	src <- e1
	src <- e2

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("messages did not arrive")
	}
	d, ok := got[0].(TextDeltaMsg)
	if !ok || d.Text != "hi" || d.Seq != 7 || d.Session != "s1" {
		t.Errorf("first msg = %#v, want TextDeltaMsg{Seq:7 Session:s1 Text:hi}", got[0])
	}
	f, ok := got[1].(TurnFinishedMsg)
	if !ok || f.Reason != "done" {
		t.Errorf("second msg = %#v, want TurnFinishedMsg{Reason:done}", got[1])
	}
}
