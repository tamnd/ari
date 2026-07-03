package core

import (
	"context"
	"testing"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
)

// TestReplayThroughTheColony is the first real consumer of eval.Replay: a
// scripted runner drives the colony and the produced event sequence must
// match the recorded one after Seq/Time normalization, which is the loop
// determinism gate in miniature (D6, D23).
func TestReplayThroughTheColony(t *testing.T) {
	c := openColony(t, WithRunner(runnerFunc(func(ctx context.Context, h *TurnHandle) error {
		if err := h.Emit(event.TypeTextDelta, event.TextDelta{Part: 0, Text: "scripted answer"}); err != nil {
			return err
		}
		return h.Emit(event.TypeTextEnd, event.TextEnd{Part: 0})
	})))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	id, err := c.NewSession(ctx, NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}

	drive := func(s eval.Script) ([]event.Event, error) {
		sub, err := c.Events(ctx, EventFilter{Session: id})
		if err != nil {
			return nil, err
		}
		defer sub.Cancel()
		turn, err := c.Submit(ctx, SubmitRequest{Session: id, Text: s.Prompt})
		if err != nil {
			return nil, err
		}
		var out []event.Event
		for e := range sub.C {
			if e.Type == event.TypeHello || e.Turn != string(turn) {
				continue
			}
			// The turn id is fresh per run; blank it like Seq and Time so
			// the recorded script stays comparable.
			e.Turn = ""
			var norm map[string]any
			if err := e.Decode(&norm); err == nil {
				delete(norm, "id")
				ne, err := event.New(e.Type, e.Session, "", norm)
				if err != nil {
					return nil, err
				}
				ne.Seq = e.Seq
				e = ne
			}
			out = append(out, e)
			if e.Type == event.TypeTurnFinished {
				return out, nil
			}
		}
		return out, nil
	}

	mk := func(typ event.Type, payload any) event.Event {
		e, err := event.New(typ, string(id), "", payload)
		if err != nil {
			t.Fatal(err)
		}
		return e
	}
	script := eval.Script{
		Name:   "one-scripted-turn",
		Prompt: "say the line",
		Want: []event.Event{
			mk(event.TypeTurnStarted, map[string]any{"ant": "worker", "prompt": "say the line"}),
			mk(event.TypeTextDelta, map[string]any{"part": float64(0), "text": "scripted answer"}),
			mk(event.TypeTextEnd, map[string]any{"part": float64(0)}),
			mk(event.TypeTurnFinished, map[string]any{"reason": "done"}),
		},
	}
	eval.Replay(t, script, drive)
}
