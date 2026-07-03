package ui

import (
	"strings"
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// noteTTL is how long a transient notification covers the help line.
const noteTTL = 4 * time.Second

// statusExpired asks the status line to re-check its deadline.
type statusExpired struct{}

// StatusController owns the bottom line: the current scope's short help,
// covered by transient notifications with a time-to-live (doc 02
// section 12.4). One line serves as help, status, and notification.
type StatusController struct {
	th       theme.Theme
	km       keys.Map
	note     string
	level    string // info, warn, error, success
	deadline time.Time
	now      func() time.Time
}

// NewStatus builds the line over the live keymap.
func NewStatus(th theme.Theme, km keys.Map, now func() time.Time) *StatusController {
	return &StatusController{th: th, km: km, now: now}
}

// SetKeymap swaps the live bindings, so a rebind shows on the next draw.
func (s *StatusController) SetKeymap(km keys.Map) { s.km = km }

// SetTheme swaps the palette.
func (s *StatusController) SetTheme(th theme.Theme) { s.th = th }

// Notify covers the help line with a message and schedules its expiry.
func (s *StatusController) Notify(level, text string) btea.Cmd {
	s.note, s.level = text, level
	s.deadline = s.now().Add(noteTTL)
	return btea.Tick(noteTTL, func(time.Time) btea.Msg { return statusExpired{} })
}

// Expire clears the note once its deadline passed.
func (s *StatusController) Expire() {
	if !s.deadline.IsZero() && !s.now().Before(s.deadline) {
		s.note, s.deadline = "", time.Time{}
	}
}

// helpLine renders the short help for a scope from the live bindings.
func (s *StatusController) helpLine(scope keys.Scope) string {
	st := s.th.S
	var b strings.Builder
	for i, bind := range s.km.Help(scope) {
		h := bind.Help()
		if i > 0 {
			b.WriteString(st.Faint.Render("  "))
		}
		b.WriteString(st.Subtle.Render(h.Key) + " " + st.Faint.Render(h.Desc))
	}
	return b.String()
}

// noteLine renders the active notification in its level color.
func (s *StatusController) noteLine() string {
	st := s.th.S
	style := st.Info
	switch s.level {
	case "warn":
		style = st.Warning
	case "error":
		style = st.Error
	case "success":
		style = st.Success
	}
	return style.Render(s.note)
}

// Draw paints one line: the note while one is live, else scope help.
func (s *StatusController) Draw(scr uv.Screen, area uv.Rectangle, scope keys.Scope) *tea.Cursor {
	line := s.helpLine(scope)
	if s.note != "" && s.now().Before(s.deadline) {
		line = s.noteLine()
	}
	row := uv.Rect(area.Min.X, area.Min.Y, area.Dx(), 1)
	tea.DrawStyled(scr, row, ansi.Truncate(line, area.Dx(), "…"))
	return nil
}
