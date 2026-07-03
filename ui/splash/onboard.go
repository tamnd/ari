// Onboarding: a short sequence of dialogs on the overlay stack (doc 02
// section 19.2). Each dialog returns an action, Flow decides what
// opens next, and the root applies the outcome. Credentials are named,
// never captured: the flow tells the user which environment variable
// to set or which keychain item to create, and no dialog ever reads a
// secret value (D16).
package splash

import (
	"fmt"
	"strings"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// Picked is the action a Pick dialog returns.
type Picked struct {
	ID     string
	Option string
}

// Acknowledged is the action a Notice dialog returns.
type Acknowledged struct{ ID string }

// Pick is a minimal list-choice dialog: up/down move, enter picks.
type Pick struct {
	id      string
	title   string
	detail  string
	options []string
	focus   int
	th      theme.Theme
}

// NewPick builds a choice dialog.
func NewPick(id, title, detail string, options []string, th theme.Theme) *Pick {
	return &Pick{id: id, title: title, detail: detail, options: options, th: th}
}

func (p *Pick) ID() string { return p.id }

func (p *Pick) HandleMsg(msg btea.Msg) any {
	key, ok := msg.(btea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "up", "k", "shift+tab":
		p.focus = (p.focus + len(p.options) - 1) % len(p.options)
	case "down", "j", "tab":
		p.focus = (p.focus + 1) % len(p.options)
	case "enter":
		return Picked{ID: p.id, Option: p.options[p.focus]}
	}
	return nil
}

func (p *Pick) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	s := p.th.S
	var body []string
	body = append(body, s.Title.Render(p.title))
	if p.detail != "" {
		body = append(body, s.Muted.Render(p.detail))
	}
	body = append(body, "")
	for i, opt := range p.options {
		if i == p.focus {
			body = append(body, s.Selected.Render("▸ "+opt+" "))
		} else {
			body = append(body, s.Subtle.Render("  "+opt+" "))
		}
	}
	drawCentered(scr, area, body, p.th)
	return nil
}

// Notice shows text and waits for enter.
type Notice struct {
	id    string
	title string
	body  []string
	th    theme.Theme
}

// NewNotice builds an acknowledge-only dialog.
func NewNotice(id, title string, body []string, th theme.Theme) *Notice {
	return &Notice{id: id, title: title, body: body, th: th}
}

func (n *Notice) ID() string { return n.id }

func (n *Notice) HandleMsg(msg btea.Msg) any {
	if key, ok := msg.(btea.KeyPressMsg); ok && key.String() == "enter" {
		return Acknowledged{ID: n.id}
	}
	return nil
}

func (n *Notice) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	s := n.th.S
	body := []string{s.Title.Render(n.title), ""}
	for _, l := range n.body {
		body = append(body, s.Base.Render(l))
	}
	body = append(body, "", s.Selected.Render("▸ continue "))
	drawCentered(scr, area, body, n.th)
	return nil
}

func drawCentered(scr uv.Screen, area uv.Rectangle, body []string, th theme.Theme) {
	w := 0
	for _, l := range body {
		w = max(w, ansi.StringWidth(l))
	}
	w = min(w+4, area.Dx())
	lines := tea.Frame(body, w, th.S.Border)
	h := len(lines)
	x := area.Min.X + max((area.Dx()-w)/2, 0)
	y := area.Min.Y + max((area.Dy()-h)/2, 0)
	box := uv.Rect(x, y, min(w, area.Dx()), min(h, area.Dy()))
	tea.DrawStyled(scr, box, strings.Join(lines, "\n"))
}

// credentialVar names the environment variable per provider. Names
// only; no value is ever read or shown by onboarding (D16).
var credentialVar = map[string]string{
	"anthropic":  "ANTHROPIC_API_KEY",
	"openai":     "OPENAI_API_KEY",
	"openrouter": "OPENROUTER_API_KEY",
}

// Outcome is what onboarding decided; the root writes config from it.
type Outcome struct {
	Provider    string
	Tier        string
	InitProject bool
}

// Done wraps the finished outcome as the flow's final action.
type Done struct{ Outcome Outcome }

// Flow drives the onboarding dialog sequence: provider, credential
// notice, tier, project init. Feed each dialog action to Next; it
// returns the next dialog to push, or a Done action when finished.
type Flow struct {
	th      theme.Theme
	outcome Outcome
}

// NewFlow starts onboarding.
func NewFlow(th theme.Theme) *Flow { return &Flow{th: th} }

// Start returns the first dialog.
func (f *Flow) Start() *Pick {
	return NewPick("onboard-provider", "welcome to ari",
		"pick the provider this colony will think with",
		[]string{"anthropic", "openai", "openrouter"}, f.th)
}

// Next consumes a dialog action and returns the next dialog, or a Done
// action carrying the outcome. A nil dialog with a nil action means
// the action was not one of ours.
func (f *Flow) Next(act any) (next dialog.Dialog, done any) {
	switch a := act.(type) {
	case Picked:
		switch a.ID {
		case "onboard-provider":
			f.outcome.Provider = a.Option
			v := credentialVar[a.Option]
			return NewNotice("onboard-credential", "credentials",
				[]string{
					fmt.Sprintf("ari reads %s from your environment or the OS keychain.", v),
					"The value never touches a file ari writes or a model context.",
					fmt.Sprintf("Set %s before your first prompt.", v),
				}, f.th), nil
		case "onboard-tier":
			f.outcome.Tier = a.Option
			return NewPick("onboard-init", "project memory",
				"survey this repo and write ARI.md so the colony starts oriented?",
				[]string{"initialize now", "skip for now"}, f.th), nil
		case "onboard-init":
			f.outcome.InitProject = a.Option == "initialize now"
			return nil, Done{Outcome: f.outcome}
		}
	case Acknowledged:
		if a.ID == "onboard-credential" {
			return NewPick("onboard-tier", "model tier",
				"queen plans, workers execute; pick the default tier",
				[]string{"best", "balanced", "fast"}, f.th), nil
		}
	}
	return nil, nil
}
