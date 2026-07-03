package ui

import (
	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/sidebar"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// SidebarController projects bus messages into the sidebar's state. The
// numbers it shows come from the ledger events, never from client-side
// recounting (D5, D14).
type SidebarController struct {
	panel  *sidebar.Sidebar
	st     sidebar.State
	window int64         // the model's context window, for the fill figure
	drops  func() uint64 // the broker's drop counter, read at draw time
}

// NewSidebar seeds the panel with what the shell knows before any event
// arrives.
func NewSidebar(th theme.Theme, o Options) *SidebarController {
	s := &SidebarController{
		panel:  sidebar.New(th),
		window: o.ContextWindow,
		drops:  o.Drops,
	}
	s.st.Cwd = o.Cwd
	s.st.Model = o.Model
	s.st.Provider = o.Provider
	s.st.Effort = o.Effort
	return s
}

// Apply folds one bus message into the state.
func (s *SidebarController) Apply(msg btea.Msg) {
	switch m := msg.(type) {
	case bus.LedgerTurnMsg:
		if m.Model != "" {
			s.st.Model = m.Model
		}
		s.st.CostUSD += m.CostUSD
		if s.window > 0 {
			s.st.ContextPct = float64(m.Input+m.CacheRead+m.CacheWrite) / float64(s.window)
		}
	case bus.TurnStartedMsg:
		s.st.Colony = "one ant, working"
	case bus.TurnFinishedMsg:
		s.st.Colony = "one ant, idle"
	}
}

// ToggleDebug flips the drop-counter surface.
func (s *SidebarController) ToggleDebug() { s.st.Debug = !s.st.Debug }

// SetModel records a picked model name.
func (s *SidebarController) SetModel(name string) { s.st.Model = name }

// SetTheme swaps the palette.
func (s *SidebarController) SetTheme(th theme.Theme) { s.panel.SetTheme(th) }

// Draw refreshes the drop counter and paints the panel.
func (s *SidebarController) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	if s.drops != nil {
		s.st.Drops = s.drops()
	}
	s.panel.SetState(s.st)
	return s.panel.Draw(scr, area)
}
