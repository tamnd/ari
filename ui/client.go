package ui

import "context"

// Client is the slice of the core's session API the shell drives. It is
// declared here, not imported, because the UI never imports a core
// package (doc 02 section 1); cmd wires an adapter over the real core.
// Decisions and modes cross as their wire strings.
type Client interface {
	// NewSession creates a session in the current project.
	NewSession(ctx context.Context, title string) (string, error)

	// Sessions lists the project's sessions, newest first.
	Sessions(ctx context.Context) ([]SessionInfo, error)

	// Submit enqueues one user turn and returns its id.
	Submit(ctx context.Context, session, text string) (string, error)

	// Cancel aborts the running turn on a session.
	Cancel(ctx context.Context, session string) error

	// Respond answers a permission request: allow, allow_session, deny.
	Respond(ctx context.Context, session, requestID, decision string) error
}

// SessionInfo is one row of the session switcher.
type SessionInfo struct {
	ID    string
	Title string
}
