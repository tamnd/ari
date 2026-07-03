package ui

import (
	"context"
	"time"

	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/perm"
	"github.com/tamnd/ari/ui/theme"
)

// responded reports a permission answer landing at the core.
type responded struct {
	id  string
	err error
}

// PermController turns permission events into dialogs and decisions into
// client calls. It is the only place a perm.Choice becomes a wire word.
// The session a request arrived on rides with the request id, so the
// answer lands on the right session even if the user switched away.
type PermController struct {
	client   Client
	th       theme.Theme
	sessions map[string]string // request id -> session
}

// NewPerm builds the controller.
func NewPerm(client Client, th theme.Theme) *PermController {
	return &PermController{client: client, th: th, sessions: map[string]string{}}
}

// SetTheme swaps the palette for dialogs opened after the change.
func (p *PermController) SetTheme(th theme.Theme) { p.th = th }

// Request pushes a dialog for an incoming permission request.
func (p *PermController) Request(m bus.PermissionRequestedMsg, overlay *dialog.Overlay, now time.Time) {
	p.sessions[m.ID] = m.Session
	overlay.Push(perm.New(m.PermissionRequested, p.th), now)
}

// Resolved pops the dialog when the core resolved the request some
// other way, a rule or a timeout, so a stale prompt never lingers.
func (p *PermController) Resolved(m bus.PermissionResolvedMsg, overlay *dialog.Overlay, now time.Time) {
	delete(p.sessions, m.ID)
	if d := overlay.Active(); d != nil && d.ID() == "perm:"+m.ID {
		overlay.Pop(now)
	}
}

// Resolve sends the user's decision to the core.
func (p *PermController) Resolve(d perm.Decision) btea.Cmd {
	wire := map[perm.Choice]string{
		perm.Deny:         "deny",
		perm.AllowOnce:    "allow",
		perm.AllowSession: "allow_session",
	}[d.Choice]
	client, session := p.client, p.sessions[d.ID]
	delete(p.sessions, d.ID)
	return func() btea.Msg {
		err := client.Respond(context.Background(), session, d.ID, wire)
		return responded{id: d.ID, err: err}
	}
}
