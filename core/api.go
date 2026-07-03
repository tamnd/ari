package core

import (
	"context"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/session"
)

// SessionID and TurnID are the handles clients hold. Plain strings on the
// wire, so the in-process API and a future RPC share them (D2).
type SessionID = session.ID

// TurnID identifies one submitted user turn.
type TurnID string

// PermMode is the session permission mode (doc 05). Only the names cross
// this boundary in M0; the pipeline lands in slice 7.
type PermMode string

const (
	ModeAsk      PermMode = "ask"
	ModeAutoEdit PermMode = "auto-edit"
	ModeFullAuto PermMode = "full-auto"
	ModePlan     PermMode = "plan"
)

// SessionAPI is the client-facing contract. It is deliberately small; the
// event stream carries the rich detail, this interface carries the
// commands (doc 01 section 4.2).
type SessionAPI interface {
	// NewSession creates an empty session in the current project and
	// returns its id.
	NewSession(ctx context.Context, req NewSessionRequest) (SessionID, error)

	// ListSessions returns session summaries for the project, newest first.
	ListSessions(ctx context.Context) ([]SessionSummary, error)

	// Submit enqueues a user turn and returns immediately with a TurnID.
	// Progress arrives on the event stream, not as a return value. A busy
	// session queues the turn behind the running one.
	Submit(ctx context.Context, req SubmitRequest) (TurnID, error)

	// Cancel aborts the running turn on a session via context cancellation.
	Cancel(ctx context.Context, s SessionID) error

	// Respond answers an outstanding permission request or elicitation.
	Respond(ctx context.Context, req RespondRequest) error

	// Events returns a subscription to the JSON event stream, optionally
	// filtered to one session. The first event on a fresh subscription is
	// a hello carrying the schema version and a resume cursor.
	Events(ctx context.Context, filter EventFilter) (*Subscription, error)
}

// NewSessionRequest creates or forks a session.
type NewSessionRequest struct {
	Parent SessionID `json:"parent,omitempty"`  // "" for a root session
	AtTurn string    `json:"at_turn,omitempty"` // fork point entry id; "" is the leaf
	Title  string    `json:"title,omitempty"`   // optional, may be filled later
	Agent  string    `json:"agent,omitempty"`   // "" means route; a name pins an ant
}

// SubmitRequest is one user turn.
type SubmitRequest struct {
	Session SessionID    `json:"session"`
	Text    string       `json:"text"`
	Attach  []Attachment `json:"attach,omitempty"`
	Mode    PermMode     `json:"mode,omitempty"` // "" inherits the session's
}

// Attachment is a file, image, or pasted blob riding a submit (doc 12).
type Attachment struct {
	Path string `json:"path,omitempty"`
	Name string `json:"name,omitempty"`
	Mime string `json:"mime,omitempty"`
}

// RespondChoice is a permission answer.
type RespondChoice string

const (
	Allow        RespondChoice = "allow"
	AllowSession RespondChoice = "allow_session"
	Deny         RespondChoice = "deny"
)

// RespondRequest answers a permission request or elicitation.
type RespondRequest struct {
	Session   SessionID     `json:"session"`
	RequestID string        `json:"request_id"`
	Decision  RespondChoice `json:"decision"`
	Value     string        `json:"value,omitempty"` // for elicitation answers
}

// EventFilter narrows a subscription.
type EventFilter struct {
	Session SessionID    `json:"session,omitempty"` // "" means every session
	Types   []event.Type `json:"types,omitempty"`   // nil means every type
}

// SessionSummary is one row of a session list.
type SessionSummary = session.Summary
