package ui

import (
	"context"
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/input"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// State is where the shell is in its life: first-run onboarding, the
// empty landing screen, or a live chat (doc 02 section 18.3).
type State int

const (
	StateOnboarding State = iota
	StateLanding
	StateChat
)

// Focus is which pane owns non-dialog input.
type Focus int

const (
	FocusEditor Focus = iota
	FocusChat
	FocusNone
)

// Options wires the root to everything it must not construct itself.
type Options struct {
	Client        Client
	Theme         theme.Theme
	Keys          keys.Map
	FirstRun      bool                       // no config and no nest: run onboarding
	Cwd           string                     // shown in the sidebar
	Model         string                     // the configured model, pre-ledger
	Provider      string                     // the configured provider
	Effort        string                     // reasoning effort, empty when unset
	Models        []string                   // pickable models for the picker
	ContextWindow int64                      // tokens, for the context fill figure
	Session       string                     // resume: next turns go here, "" starts fresh
	Namespace     string                     // the worker ant's memory namespace, for the panel
	Drops         func() uint64              // the broker's lossy-lane drop counter
	Onboarded     func(splash.Outcome) error // persists first-run choices
	Now           func() time.Time           // tests pin this
}

// Model is the root program: the only tea.Model in the tree. It owns
// size, state, focus, the overlay, and the theme, and fans everything
// else out to controllers (doc 02 section 18.1).
type Model struct {
	opts    Options
	client  Client
	km      keys.Map
	theme   theme.Theme
	now     func() time.Time
	router  Router
	overlay *dialog.Overlay

	width, height int
	state         State
	focus         Focus
	session       string
	turnLive      bool

	chat    *ChatController
	editor  *EditorController
	sidebar *SidebarController
	status  *StatusController
	perms   *PermController
	memory  *MemoryController
	colony  *ColonyController
	onboard *splash.Flow
}

// New assembles the root and its controllers.
func New(o Options) *Model {
	if o.Now == nil {
		o.Now = time.Now
	}
	m := &Model{
		opts:    o,
		client:  o.Client,
		km:      o.Keys,
		theme:   o.Theme,
		now:     o.Now,
		overlay: &dialog.Overlay{},
		state:   StateLanding,
		focus:   FocusEditor,
	}
	m.chat = NewChat(o.Theme, o.Now)
	m.editor = NewEditor(o.Theme, m.submitCmd)
	m.sidebar = NewSidebar(o.Theme, o)
	m.status = NewStatus(o.Theme, o.Keys, o.Now)
	m.perms = NewPerm(o.Client, o.Theme)
	m.memory = NewMemory(o.Client, o.Theme, o.Namespace)
	m.colony = NewColony(o.Theme)
	if o.Session != "" {
		m.session = o.Session
		m.state = StateChat
	}
	if o.FirstRun {
		m.state = StateOnboarding
		m.onboard = splash.NewFlow(o.Theme)
	}
	return m
}

// SetKeymap swaps the live bindings. The next keypress matches against
// the new map and the help line reads it, no restart involved.
func (m *Model) SetKeymap(km keys.Map) {
	m.km = km
	m.status.SetKeymap(km)
}

// Init starts the editor cursor and, on first run, the onboarding flow.
func (m *Model) Init() btea.Cmd {
	if m.onboard != nil {
		m.overlay.Push(m.onboard.Start(), m.now())
	}
	return m.editor.Focus()
}

// Update is small on purpose: route input, fan events, apply actions.
func (m *Model) Update(msg btea.Msg) (btea.Model, btea.Cmd) {
	switch v := msg.(type) {
	case btea.WindowSizeMsg:
		m.width, m.height = v.Width, v.Height
		return m, nil
	case btea.KeyPressMsg:
		return m, m.handleKey(v)
	case input.CoalescedWheel:
		m.chat.Scroll(-v.Delta)
		return m, nil
	case input.EditorClosed:
		if v.Err == nil {
			m.editor.SetValue(v.Content)
		} else {
			return m, m.status.Notify("error", "external editor: "+v.Err.Error())
		}
		return m, nil
	case statusExpired:
		m.status.Expire()
		return m, nil
	case submitted:
		return m, m.applySubmitted(v)
	case responded:
		if v.err != nil {
			return m, m.status.Notify("error", "permission response failed: "+v.err.Error())
		}
		return m, nil
	case sessionsLoaded:
		return m, m.applySessions(v)
	case memIndexLoaded:
		if v.err != nil {
			return m, m.status.Notify("error", "memory: "+v.err.Error())
		}
		m.memory.OnIndex(v)
		return m, nil
	case memResults:
		if v.err != nil {
			return m, m.status.Notify("error", "memory search: "+v.err.Error())
		}
		m.memory.OnResults(v)
		return m, nil
	case memForgot:
		if v.err != nil {
			return m, m.status.Notify("error", "forget: "+v.err.Error())
		}
		return m, m.memory.OnForgot(v)
	case bus.PermissionRequestedMsg:
		m.perms.Request(v, m.overlay, m.now())
		return m, nil
	case bus.PermissionResolvedMsg:
		m.perms.Resolved(v, m.overlay, m.now())
		return m, nil
	case bus.TurnStartedMsg:
		m.turnLive = true
	case bus.TurnFinishedMsg:
		m.turnLive = false
	case bus.ErrorMsg:
		m.chat.Apply(msg)
		m.sidebar.Apply(msg)
		return m, m.status.Notify("error", v.Message)
	}
	// Everything else is projection: the chat and sidebar read what
	// they care about and ignore the rest. Events from a session the
	// user switched away from never touch this view.
	if e, ok := msg.(bus.Enveloped); ok && m.session != "" &&
		e.BusMeta().Session != "" && e.BusMeta().Session != m.session {
		return m, nil
	}
	m.chat.Apply(msg)
	m.sidebar.Apply(msg)
	m.memory.Apply(msg)
	m.colony.Apply(msg)
	if m.state == StateLanding && !m.chat.Empty() {
		m.state = StateChat
	}
	return m, nil
}

// handleKey runs the router: dialog first, then the focused pane's
// scope, then global, then raw typing into the editor.
func (m *Model) handleKey(k btea.KeyPressMsg) btea.Cmd {
	scope, ctrl := m.router.Route(m)
	if scope == keys.Dialog {
		act, _ := m.overlay.HandleMsg(k, m.now())
		return m.apply(act)
	}
	ks := k.String()
	if a, ok := m.km.Lookup(scope, ks); ok && ctrl != nil {
		if cmd := ctrl.Handle(a, k); cmd != nil {
			return cmd
		}
		return nil
	}
	if a, ok := m.km.Lookup(keys.Global, ks); ok {
		return m.global(a)
	}
	if m.focus == FocusEditor {
		return m.editor.Type(k)
	}
	return nil
}

// global runs a global-scope action on the root itself.
func (m *Model) global(a keys.Action) btea.Cmd {
	switch a {
	case keys.Quit:
		return btea.Quit
	case keys.Cancel:
		return m.cancelCmd()
	case keys.FocusNext:
		return m.toggleFocus()
	case keys.HelpToggle:
		m.overlay.Push(m.helpDialog(), m.now())
	case keys.Palette:
		m.overlay.Push(m.paletteDialog(), m.now())
	case keys.ThemePick:
		m.overlay.Push(m.themeDialog(), m.now())
	case keys.MemoryPanel:
		return m.memory.Open(m.overlay, m.now())
	case keys.ColonyPanel:
		return m.colony.Open(m.overlay, m.now())
	}
	return nil
}

func (m *Model) toggleFocus() btea.Cmd {
	if m.focus == FocusEditor {
		m.focus = FocusChat
		m.editor.Blur()
		return nil
	}
	m.focus = FocusEditor
	return m.editor.Focus()
}

// cancelCmd aborts the running turn, if there is one to abort.
func (m *Model) cancelCmd() btea.Cmd {
	if !m.turnLive || m.session == "" {
		return nil
	}
	client, session := m.client, m.session
	return func() btea.Msg {
		if err := client.Cancel(context.Background(), session); err != nil {
			return submitted{err: err}
		}
		return nil
	}
}

// View composites every widget onto one cell buffer and hands bubbletea
// the rendered frame; ultraviolet damage-diffs it against the terminal.
func (m *Model) View() btea.View {
	if m.width <= 0 || m.height <= 0 {
		v := btea.NewView("")
		v.AltScreen = true
		return v
	}
	l := ComputeLayout(m.width, m.height, m.editor.Lines(), false)
	buf := uv.NewScreenBuffer(m.width, m.height)

	if l.Compact {
		m.drawHeader(buf, l.Header)
	} else {
		m.sidebar.Draw(buf, l.Sidebar)
	}
	m.chat.Draw(buf, l.Main)
	m.editor.Draw(buf, l.Editor)
	scope, _ := m.router.Route(m)
	m.status.Draw(buf, l.Status, scope)
	m.overlay.Draw(buf, l.Area)

	v := btea.NewView(buf.Render())
	v.AltScreen = true
	v.MouseMode = btea.MouseModeCellMotion
	v.WindowTitle = "ari"
	return v
}

// drawHeader is compact mode's one-line stand-in for the sidebar.
func (m *Model) drawHeader(scr uv.Screen, area uv.Rectangle) {
	line := splash.Compact(m.theme)
	if m.session != "" {
		line += m.theme.S.Faint.Render("  ·  session " + m.session)
	}
	tea.DrawStyled(scr, area, line)
}
