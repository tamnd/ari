// Package event is the shared vocabulary between the core and every client.
//
// It imports nothing outside the standard library, and a test enforces
// that, because a shared vocabulary with dependencies is a coupling leak.
//
// Versioning discipline: SchemaMajor bumps only when an existing field is
// renamed, retyped, or removed, or when an event type changes meaning.
// Adding a new event type or a new optional field is not a major bump.
// The golden schema test pins every payload's JSON keys, so any change
// here is a visible, deliberate diff in review.
package event

import (
	"encoding/json"
	"time"
)

// SchemaMajor is the wire schema major version carried on every event.
const SchemaMajor = 1

// Type names an event. Types are dotted, lowercase, and stable.
type Type string

// The M0 event types. The colony and memory types at the bottom are
// defined but never emitted in M0; the features that produce them arrive
// with M2 and M3, and the seam-without-the-feature rule applies to the
// wire schema too.
const (
	TypeHello Type = "hello"

	TypeSessionCreated Type = "session.created"
	TypeSessionUpdated Type = "session.updated"
	TypeSessionForked  Type = "session.forked"

	TypeTurnStarted  Type = "turn.started"
	TypeTurnFinished Type = "turn.finished"

	TypeTextDelta     Type = "part.text.delta"
	TypeTextEnd       Type = "part.text.end"
	TypeThinkingDelta Type = "part.thinking.delta"
	TypeThinkingEnd   Type = "part.thinking.end"
	TypeToolStart     Type = "part.tool.start"
	TypeToolProgress  Type = "part.tool.progress"
	TypeToolEnd       Type = "part.tool.end"

	TypePermissionRequested Type = "permission.requested"
	TypePermissionResolved  Type = "permission.resolved"

	TypeLedgerTurn Type = "ledger.turn"
	TypeLog        Type = "log"
	TypeError      Type = "error"

	// Defined, never emitted in M0.
	TypeAntSpawned   Type = "ant.spawned"
	TypeRouteDecided Type = "route.decided"
	TypeMemoryFolded Type = "memory.folded"

	// The colony vocabulary. Defined here for the wire schema; the colony
	// runner in M3 emits them. The colony package itself never imports this
	// package (it is kernel and dependency-light), so it names these events
	// as plain strings through the JournalFunc seam and the values match the
	// constants below to the byte.
	TypeFanOutApproved     Type = "colony.fanout.approved"
	TypeFanOutRefused      Type = "colony.fanout.refused"
	TypeColonyThrottle     Type = "colony.throttle"
	TypeWorkerWoke         Type = "colony.worker.woke"
	TypeWorkerBlocked      Type = "colony.worker.blocked"
	TypeWorkerFinished     Type = "colony.worker.finished"
	TypeColonyProgress     Type = "colony.progress"
	TypeQuestionUnresolved Type = "colony.question.unresolved"
	TypeWorktreeConflict   Type = "colony.worktree.conflict"
	TypeArbitrationOpened  Type = "colony.arbitration.opened"
	TypeArbitrationClosed  Type = "colony.arbitration.closed"
)

// Event is the envelope every payload travels in. Seq is assigned by the
// journal's single writer and is total and gap-free within a colony run,
// which is what makes resume-from-cursor possible for every client.
type Event struct {
	V       int             `json:"v"`
	Seq     uint64          `json:"seq"`
	Type    Type            `json:"type"`
	Session string          `json:"session,omitempty"`
	Turn    string          `json:"turn,omitempty"`
	Time    time.Time       `json:"time"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// New wraps a payload in an envelope. Seq stays zero until the journal
// assigns it; a zero Seq means the event has not been journaled.
func New(t Type, session, turn string, payload any) (Event, error) {
	var raw json.RawMessage
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return Event{}, err
		}
		raw = b
	}
	return Event{
		V:       SchemaMajor,
		Type:    t,
		Session: session,
		Turn:    turn,
		Time:    time.Now().UTC(),
		Payload: raw,
	}, nil
}

// Decode unmarshals the payload into dst.
func (e Event) Decode(dst any) error {
	return json.Unmarshal(e.Payload, dst)
}
