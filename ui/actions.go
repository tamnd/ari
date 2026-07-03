package ui

import (
	"context"
	"slices"
	"strings"

	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/perm"
	"github.com/tamnd/ari/ui/picker"
	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/theme"
)

// submitted reports a submit round trip: session creation, if one was
// needed, then the enqueued turn.
type submitted struct {
	session string
	turn    string
	err     error
}

// sessionsLoaded carries the session list for the switcher.
type sessionsLoaded struct {
	items []picker.Item
	err   error
}

// apply interprets an opaque dialog action. This is the only place
// dialog outputs become root behavior, which is what keeps every dialog
// testable with a fake action sink (doc 02 section 7.1).
func (m *Model) apply(act dialog.Action) btea.Cmd {
	switch v := act.(type) {
	case nil:
		return nil
	case perm.Decision:
		m.overlay.Pop(m.now())
		return m.perms.Resolve(v)
	case picker.Chosen:
		m.overlay.Pop(m.now())
		return m.applyChoice(v)
	case dialog.Closed:
		if m.state == StateOnboarding {
			// Escaping onboarding is allowed; the shell still works and
			// the flow returns on the next fresh start.
			m.state = StateLanding
		}
		return nil
	case splash.Picked, splash.Acknowledged:
		return m.advanceOnboarding(act)
	}
	return nil
}

// applyChoice runs a picker selection by dialog id.
func (m *Model) applyChoice(c picker.Chosen) btea.Cmd {
	switch c.ID {
	case "palette":
		return m.paletteCommand(c.Key)
	case "theme":
		m.setTheme(c.Key)
		return m.status.Notify("success", "theme: "+c.Key)
	case "model":
		m.sidebar.SetModel(c.Key)
		return m.status.Notify("info", "model preference set to "+c.Key+"; switching mid-session lands with M1")
	case "session":
		m.session = c.Key
		m.state = StateChat
		return m.status.Notify("info", "next turns go to session "+c.Key)
	}
	return nil
}

// paletteCommand runs one command-palette entry.
func (m *Model) paletteCommand(key string) btea.Cmd {
	switch key {
	case "model":
		m.overlay.Push(m.modelDialog(), m.now())
	case "session":
		return m.loadSessionsCmd()
	case "theme":
		m.overlay.Push(m.themeDialog(), m.now())
	case "debug":
		m.sidebar.ToggleDebug()
	case "help":
		m.overlay.Push(m.helpDialog(), m.now())
	case "quit":
		return btea.Quit
	}
	return nil
}

// advanceOnboarding feeds an action to the flow and applies the result.
func (m *Model) advanceOnboarding(act dialog.Action) btea.Cmd {
	if m.onboard == nil {
		return nil
	}
	m.overlay.Pop(m.now())
	next, done := m.onboard.Next(act)
	if next != nil {
		m.overlay.Push(next, m.now())
		return nil
	}
	if d, ok := done.(splash.Done); ok {
		m.state = StateLanding
		m.onboard = nil
		persist := m.opts.Onboarded
		notify := m.status.Notify("success", "ready; type a prompt to start")
		if persist == nil {
			return notify
		}
		return btea.Batch(notify, func() btea.Msg {
			if err := persist(d.Outcome); err != nil {
				return submitted{err: err}
			}
			return nil
		})
	}
	return nil
}

// submitCmd creates a session on first use, then enqueues the turn.
func (m *Model) submitCmd(text string) btea.Cmd {
	client, session := m.client, m.session
	return func() btea.Msg {
		ctx := context.Background()
		if session == "" {
			s, err := client.NewSession(ctx, "")
			if err != nil {
				return submitted{err: err}
			}
			session = s
		}
		turn, err := client.Submit(ctx, session, text)
		return submitted{session: session, turn: turn, err: err}
	}
}

// applySubmitted lands the submit result on the root.
func (m *Model) applySubmitted(s submitted) btea.Cmd {
	if s.err != nil {
		return m.status.Notify("error", s.err.Error())
	}
	if s.session != "" {
		m.session = s.session
		m.state = StateChat
	}
	return nil
}

// loadSessionsCmd fetches the session list off the update loop.
func (m *Model) loadSessionsCmd() btea.Cmd {
	client := m.client
	return func() btea.Msg {
		infos, err := client.Sessions(context.Background())
		out := sessionsLoaded{err: err}
		for _, s := range infos {
			label := s.Title
			if label == "" {
				label = s.ID
			}
			out.items = append(out.items, picker.Item{Key: s.ID, Label: label})
		}
		return out
	}
}

// applySessions opens the switcher once the list arrived.
func (m *Model) applySessions(s sessionsLoaded) btea.Cmd {
	if s.err != nil {
		return m.status.Notify("error", "sessions: "+s.err.Error())
	}
	if len(s.items) == 0 {
		return m.status.Notify("info", "no sessions yet")
	}
	m.overlay.Push(picker.New("session", "switch session", s.items, m.theme), m.now())
	return nil
}

// setTheme swaps the palette everywhere.
func (m *Model) setTheme(name string) {
	th, ok := theme.Themes()[name]
	if !ok {
		return
	}
	m.theme = th
	m.chat.SetTheme(th)
	m.sidebar.SetTheme(th)
	m.status.SetTheme(th)
	m.perms.SetTheme(th)
}

// modelDialog picks from the configured model list.
func (m *Model) modelDialog() dialog.Dialog {
	items := make([]picker.Item, 0, len(m.opts.Models))
	for _, name := range m.opts.Models {
		items = append(items, picker.Item{Key: name, Label: name})
	}
	if len(items) == 0 {
		items = append(items, picker.Item{Key: m.opts.Model, Label: m.opts.Model, Detail: "configured"})
	}
	return picker.New("model", "pick a model", items, m.theme)
}

// themeDialog picks from the theme registry.
func (m *Model) themeDialog() dialog.Dialog {
	var items []picker.Item
	for name := range theme.Themes() {
		items = append(items, picker.Item{Key: name, Label: name})
	}
	slices.SortFunc(items, func(a, b picker.Item) int { return strings.Compare(a.Key, b.Key) })
	return picker.New("theme", "pick a theme", items, m.theme)
}

// paletteDialog is the command palette: every root command, filterable.
func (m *Model) paletteDialog() dialog.Dialog {
	return picker.New("palette", "palette", []picker.Item{
		{Key: "model", Label: "switch model"},
		{Key: "session", Label: "switch session"},
		{Key: "theme", Label: "switch theme"},
		{Key: "debug", Label: "toggle debug counters"},
		{Key: "help", Label: "show key bindings"},
		{Key: "quit", Label: "quit ari"},
	}, m.theme)
}

// helpDialog lists every live binding by scope, reading the keymap so a
// rebind shows the moment it lands.
func (m *Model) helpDialog() dialog.Dialog {
	var body []string
	for _, sc := range []keys.Scope{keys.Global, keys.Editor, keys.Chat, keys.Dialog} {
		body = append(body, m.theme.S.Subtle.Render(sc.String()))
		for _, b := range m.km.Help(sc) {
			h := b.Help()
			body = append(body, "  "+m.theme.S.Base.Render(h.Key)+"  "+m.theme.S.Faint.Render(h.Desc))
		}
	}
	return splash.NewNotice("help", "keys", body, m.theme)
}
