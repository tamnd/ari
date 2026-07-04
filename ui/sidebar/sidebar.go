// Package sidebar is the live ops panel (doc 02 section 13.1): a fixed
// width column of stacked sections reading from state the root
// projects out of core events. The cost and context numbers come from
// the ledger, so what the user sees spending is what the core counted
// (D5, D14). No section computes anything; the root feeds it facts.
package sidebar

import (
	"fmt"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// Width is the column the layout reserves for the sidebar.
const Width = 30

// FileChange is one modified file with its line delta.
type FileChange struct {
	Path           string
	Added, Removed int
}

// ServerDiag is one language server's live diagnostic tally, projected
// from the LSP service status so the human sees the same error count the
// model is being handed (doc 04 section 13.1).
type ServerDiag struct {
	Name             string
	Errors, Warnings int
}

// State is everything the sidebar shows. The root updates it from
// events; the sidebar only renders it.
type State struct {
	Cwd         string
	Model       string
	Provider    string
	Effort      string  // reasoning effort, empty when unset
	ContextPct  float64 // 0..1 context window fill, from the ledger
	CostUSD     float64 // running session cost, from the ledger
	Files       []FileChange
	Colony      string       // one line at M0: the single ant's state
	Diagnostics []ServerDiag // per-server error and warning counts
	Drops       uint64       // bus drop counter (lossy lane)
	Debug       bool         // drops show only when toggled on
}

// Sidebar renders State into its column.
type Sidebar struct {
	th theme.Theme
	st State
}

// New builds a sidebar.
func New(th theme.Theme) *Sidebar { return &Sidebar{th: th} }

// SetState replaces the projected state.
func (s *Sidebar) SetState(st State) { s.st = st }

// SetTheme swaps the palette.
func (s *Sidebar) SetTheme(th theme.Theme) { s.th = th }

// Draw paints the sections top down. Sections are ordered by priority;
// when height runs short the file list gives way first and whole
// sections drop from the bottom, so a short terminal keeps the
// essentials.
func (s *Sidebar) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	lines := s.render(area.Dx(), area.Dy())
	for i, l := range lines {
		if i >= area.Dy() {
			break
		}
		row := uv.Rect(area.Min.X, area.Min.Y+i, area.Dx(), 1)
		tea.DrawStyled(scr, row, l)
	}
	return nil
}

// render assembles the sections into at most height lines.
func (s *Sidebar) render(width, height int) []string {
	th := s.th.S
	fit := func(l string) string { return ansi.Truncate(l, width, "…") }

	// Fixed sections first, in priority order.
	var out []string
	out = append(out, splash.Compact(s.th), "")
	out = append(out, fit(th.Faint.Render(shortenPath(s.st.Cwd, width))), "")

	model := s.st.Model
	if s.st.Effort != "" {
		model += " (" + s.st.Effort + ")"
	}
	out = append(out,
		fit(th.Base.Render(model)),
		fit(th.Muted.Render(s.st.Provider)),
		fit(th.Muted.Render(fmt.Sprintf("context %3.0f%%  $%.2f", s.st.ContextPct*100, s.st.CostUSD))),
		"")

	colony := s.st.Colony
	if colony == "" {
		colony = "one ant, awake"
	}
	out = append(out, fit(th.Info.Render("◆ "+colony)), "")

	// Language servers, one line each with their live tally, so a build
	// going red shows here the moment an edit reports it.
	if len(s.st.Diagnostics) > 0 {
		out = append(out, fit(th.Subtle.Render("language servers")))
		for _, d := range s.st.Diagnostics {
			tally := th.Success.Render("clean")
			if d.Errors > 0 || d.Warnings > 0 {
				tally = th.Error.Render(fmt.Sprintf("%d err", d.Errors)) + " " +
					th.Muted.Render(fmt.Sprintf("%d warn", d.Warnings))
			}
			pad := max(width-ansi.StringWidth(d.Name)-ansi.StringWidth(tally), 1)
			out = append(out, fit(th.Base.Render(d.Name)+fmt.Sprintf("%*s", pad, "")+tally))
		}
		out = append(out, "")
	}

	// The file list takes whatever room is left, minus the debug line.
	tail := 0
	if s.st.Debug {
		tail = 1
	}
	if len(s.st.Files) > 0 {
		out = append(out, fit(th.Subtle.Render("modified")))
		room := height - len(out) - tail
		files := s.st.Files
		if len(files) > room && room >= 1 {
			files = files[:room-1]
		}
		for _, f := range files {
			delta := th.Success.Render(fmt.Sprintf("+%d", f.Added)) + " " +
				th.Error.Render(fmt.Sprintf("-%d", f.Removed))
			name := shortenPath(f.Path, width-ansi.StringWidth(delta)-1)
			pad := max(width-ansi.StringWidth(name)-ansi.StringWidth(delta), 1)
			out = append(out, fit(th.Base.Render(name)+fmt.Sprintf("%*s", pad, "")+delta))
		}
		if n := len(s.st.Files) - len(files); n > 0 {
			out = append(out, fit(th.Faint.Render(fmt.Sprintf("… %d more", n))))
		}
	}

	if s.st.Debug {
		out = append(out, fit(th.Faint.Render(fmt.Sprintf("bus drops %d", s.st.Drops))))
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// shortenPath keeps a path readable in a narrow column: the tail wins.
func shortenPath(p string, width int) string {
	if ansi.StringWidth(p) <= width || width < 2 {
		return p
	}
	r := []rune(p)
	return "…" + string(r[len(r)-(width-1):])
}
