package bus

import (
	"context"
	"encoding/json"

	tea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/event"
)

// Meta is the envelope every UI message carries: enough to attribute a
// payload to its session and turn and to resume from a cursor.
type Meta struct {
	Seq     uint64
	Session string
	Turn    string
}

// BusMeta exposes the envelope through one interface, so a consumer can
// read attribution off any bus message without a type switch per type.
func (m Meta) BusMeta() Meta { return m }

// Enveloped is any message carrying a Meta; every type below qualifies
// through embedding.
type Enveloped interface{ BusMeta() Meta }

// One message type per M0 event type. Each embeds the core's payload
// struct, so the UI reads the same fields the journal recorded and there
// is no second schema to drift (D2).
type (
	HelloMsg struct {
		Meta
		event.Hello
	}
	SessionCreatedMsg struct {
		Meta
		event.SessionCreated
	}
	SessionUpdatedMsg struct {
		Meta
		event.SessionUpdated
	}
	SessionForkedMsg struct {
		Meta
		event.SessionForked
	}

	TurnStartedMsg struct {
		Meta
		event.TurnStarted
	}
	TurnFinishedMsg struct {
		Meta
		event.TurnFinished
	}

	TextDeltaMsg struct {
		Meta
		event.TextDelta
	}
	TextEndMsg struct {
		Meta
		event.TextEnd
	}
	ThinkingDeltaMsg struct {
		Meta
		event.ThinkingDelta
	}
	ThinkingEndMsg struct {
		Meta
		event.ThinkingEnd
	}
	ToolStartMsg struct {
		Meta
		event.ToolStart
	}
	ToolProgressMsg struct {
		Meta
		event.ToolProgress
	}
	ToolEndMsg struct {
		Meta
		event.ToolEnd
	}

	PermissionRequestedMsg struct {
		Meta
		event.PermissionRequested
	}
	PermissionResolvedMsg struct {
		Meta
		event.PermissionResolved
	}

	MemoryFoldedMsg struct {
		Meta
		event.MemoryFolded
	}

	LedgerTurnMsg struct {
		Meta
		event.LedgerTurn
	}
	LogMsg struct {
		Meta
		event.Log
	}
	ErrorMsg struct {
		Meta
		event.ErrorInfo
	}
)

// ToMsg is the one translation table from a core event to a tea.Msg and
// its delivery lane (doc 02 section 3.3). It is the only place in the
// codebase where that mapping exists; ok is false for event types the UI
// does not render (including the M2/M3 types that are defined but never
// emitted in M0).
func ToMsg(e event.Event) (msg tea.Msg, lane Lane, ok bool) {
	m := Meta{Seq: e.Seq, Session: e.Session, Turn: e.Turn}
	switch e.Type {
	case event.TypeHello:
		var v HelloMsg
		v.Meta = m
		return build(e, &v.Hello, &v, MustDeliver)
	case event.TypeSessionCreated:
		var v SessionCreatedMsg
		v.Meta = m
		return build(e, &v.SessionCreated, &v, MustDeliver)
	case event.TypeSessionUpdated:
		var v SessionUpdatedMsg
		v.Meta = m
		return build(e, &v.SessionUpdated, &v, MustDeliver)
	case event.TypeSessionForked:
		var v SessionForkedMsg
		v.Meta = m
		return build(e, &v.SessionForked, &v, MustDeliver)
	case event.TypeTurnStarted:
		var v TurnStartedMsg
		v.Meta = m
		return build(e, &v.TurnStarted, &v, MustDeliver)
	case event.TypeTurnFinished:
		var v TurnFinishedMsg
		v.Meta = m
		return build(e, &v.TurnFinished, &v, MustDeliver)
	case event.TypeTextDelta:
		var v TextDeltaMsg
		v.Meta = m
		return build(e, &v.TextDelta, &v, Lossy)
	case event.TypeTextEnd:
		var v TextEndMsg
		v.Meta = m
		return build(e, &v.TextEnd, &v, MustDeliver)
	case event.TypeThinkingDelta:
		var v ThinkingDeltaMsg
		v.Meta = m
		return build(e, &v.ThinkingDelta, &v, Lossy)
	case event.TypeThinkingEnd:
		var v ThinkingEndMsg
		v.Meta = m
		return build(e, &v.ThinkingEnd, &v, MustDeliver)
	case event.TypeToolStart:
		var v ToolStartMsg
		v.Meta = m
		return build(e, &v.ToolStart, &v, MustDeliver)
	case event.TypeToolProgress:
		var v ToolProgressMsg
		v.Meta = m
		return build(e, &v.ToolProgress, &v, Lossy)
	case event.TypeToolEnd:
		var v ToolEndMsg
		v.Meta = m
		return build(e, &v.ToolEnd, &v, MustDeliver)
	case event.TypePermissionRequested:
		var v PermissionRequestedMsg
		v.Meta = m
		return build(e, &v.PermissionRequested, &v, MustDeliver)
	case event.TypePermissionResolved:
		var v PermissionResolvedMsg
		v.Meta = m
		return build(e, &v.PermissionResolved, &v, MustDeliver)
	case event.TypeMemoryFolded:
		var v MemoryFoldedMsg
		v.Meta = m
		return build(e, &v.MemoryFolded, &v, MustDeliver)
	case event.TypeLedgerTurn:
		var v LedgerTurnMsg
		v.Meta = m
		return build(e, &v.LedgerTurn, &v, Lossy)
	case event.TypeLog:
		var v LogMsg
		v.Meta = m
		return build(e, &v.Log, &v, Lossy)
	case event.TypeError:
		var v ErrorMsg
		v.Meta = m
		return build(e, &v.ErrorInfo, &v, MustDeliver)
	default:
		return nil, Lossy, false
	}
}

// build decodes the payload into dst, a field of *v, and returns the
// completed message by value. A payload that does not parse is dropped
// rather than rendered wrong.
func build[T any](e event.Event, dst any, v *T, lane Lane) (tea.Msg, Lane, bool) {
	if !decode(e, dst) {
		return nil, lane, false
	}
	return *v, lane, true
}

// decode unmarshals the payload into dst.
func decode(e event.Event, dst any) bool {
	if len(e.Payload) == 0 {
		return true
	}
	return json.Unmarshal(e.Payload, dst) == nil
}

// Pump reads core events from sub and republishes them onto the broker
// through the one ToMsg table. It returns when ctx ends or sub closes.
func Pump(ctx context.Context, sub <-chan event.Event, b *Broker[tea.Msg]) {
	for {
		select {
		case <-ctx.Done():
			return
		case e, open := <-sub:
			if !open {
				return
			}
			if msg, lane, ok := ToMsg(e); ok {
				b.Publish(lane, msg)
			}
		}
	}
}

// Drain feeds a subscription into send (program.Send in production) until
// ctx ends. It runs off the UI goroutine; Update is the only place model
// state mutates, so send is the single crossing point (doc 02 section 3.3).
func Drain(ctx context.Context, s *Sub[tea.Msg], send func(tea.Msg)) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.C:
			send(m)
		}
	}
}
