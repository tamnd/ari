// Package memory is the memory panel (doc 07, plan 03 slice 11): a modal
// dialog that shows what the colony remembers. It renders the live pinned
// index the ant carries every turn, searches archival memory with the same
// ranking the loop uses, and tails the fold log so a developer watches
// consolidation happen. A forget from the panel is not a privileged delete: it
// returns a Forget action the controller routes through the permission
// pipeline, exactly like a forget the model asks for.
//
// The panel renders State and nothing more; the controller in package ui feeds
// it facts off the client and the event stream, so this package never imports
// core, the store, or the fold engine (D2, the import-graph guard).
package memory

import (
	"fmt"
	"strings"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// resultCap and foldCap bound the two scrolling regions so the panel stays a
// dialog, not a full-screen takeover; the search itself is capped by the client.
const (
	resultCap = 8
	foldCap   = 5
)

// Hit is one row a memory search returned: the id a forget names, the label and
// body a human reads, and whether a file under it changed.
type Hit struct {
	ID    string
	Label string
	Body  string
	Stale bool
}

// Fold is one consolidation the panel tailed off the stream: the namespace it
// covered and the net effect on live memory.
type Fold struct {
	Namespace   string
	Merged      int
	Reflections int
	Archived    int
	Candidates  int
}

// State is everything the panel shows. The controller updates it; the panel
// only renders it and edits the query and selection as the user types.
type State struct {
	Namespace string
	Index     string // the rendered pinned index, one pin per line
	Query     string
	Results   []Hit
	Folds     []Fold
	Selected  int
	Searched  bool // a search has run, so an empty Results means no matches
}

// Search asks the controller to run a recall for the current query.
type Search struct{ Query string }

// Forget asks the controller to archive the selected row through permissions.
type Forget struct{ ID string }

// Panel renders State as a centered dialog. It implements ui/dialog.Dialog.
type Panel struct {
	th theme.Theme
	st State
}

// New builds a panel.
func New(th theme.Theme) *Panel { return &Panel{th: th} }

// SetState replaces the whole state, for the golden tests and a full refresh.
func (p *Panel) SetState(st State) { p.st = st }

// SetTheme swaps the palette.
func (p *Panel) SetTheme(th theme.Theme) { p.th = th }

// SetNamespace records which namespace the panel is showing.
func (p *Panel) SetNamespace(ns string) { p.st.Namespace = ns }

// SetIndex replaces the rendered pinned index.
func (p *Panel) SetIndex(index string) { p.st.Index = index }

// SetResults lands a completed search: it marks the panel searched and resets
// the selection to the top so the highlight never points past the new list.
func (p *Panel) SetResults(hits []Hit) {
	p.st.Results = hits
	p.st.Searched = true
	p.st.Selected = 0
}

// AddFold prepends a folded namespace to the log, newest first, capped so the
// tail stays short.
func (p *Panel) AddFold(f Fold) {
	p.st.Folds = append([]Fold{f}, p.st.Folds...)
	if len(p.st.Folds) > foldCap {
		p.st.Folds = p.st.Folds[:foldCap]
	}
}

// ID names the dialog instance; the overlay uses it for reopen grace.
func (p *Panel) ID() string { return "memory" }

// HandleMsg edits the query, moves the selection, and answers with a Search or
// a Forget. Escape is the overlay's to handle, so the panel never sees it.
func (p *Panel) HandleMsg(msg btea.Msg) any {
	k, ok := msg.(btea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch k.Code {
	case btea.KeyUp:
		if p.st.Selected > 0 {
			p.st.Selected--
		}
	case btea.KeyDown:
		if p.st.Selected < len(p.st.Results)-1 {
			p.st.Selected++
		}
	case btea.KeyEnter:
		return Search{Query: strings.TrimSpace(p.st.Query)}
	case btea.KeyBackspace:
		if p.st.Query != "" {
			r := []rune(p.st.Query)
			p.st.Query = string(r[:len(r)-1])
		}
	default:
		if k.String() == "ctrl+d" {
			if id, ok := p.selectedID(); ok {
				return Forget{ID: id}
			}
			return nil
		}
		if k.Text != "" {
			p.st.Query += k.Text
		}
	}
	return nil
}

// selectedID returns the id of the highlighted result, if there is one.
func (p *Panel) selectedID() (string, bool) {
	if p.st.Selected < 0 || p.st.Selected >= len(p.st.Results) {
		return "", false
	}
	return p.st.Results[p.st.Selected].ID, true
}

// Draw centers the panel in area and paints its three regions.
func (p *Panel) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	s := p.th.S
	w := min(max(area.Dx()-8, 40), 72)
	inner := w - 4
	fit := func(l string) string { return ansi.Truncate(l, inner, "…") }

	title := "memory"
	if p.st.Namespace != "" {
		title += " · " + p.st.Namespace
	}
	body := []string{s.Title.Render(title), ""}

	// Pinned index: the lines the ant carries for free every turn.
	body = append(body, s.Subtle.Render("pinned index"))
	if strings.TrimSpace(p.st.Index) == "" {
		body = append(body, fit(s.Faint.Render("no pins yet")))
	} else {
		for _, l := range strings.Split(strings.TrimRight(p.st.Index, "\n"), "\n") {
			body = append(body, fit(s.Base.Render(l)))
		}
	}
	body = append(body, "")

	// Search: a query line and the ranked hits, the selected one highlighted.
	body = append(body, s.Subtle.Render("search"))
	body = append(body, fit(s.Muted.Render("› ")+s.Base.Render(p.st.Query)+s.Muted.Render("_")))
	body = append(body, p.results(inner)...)
	body = append(body, "")

	// Folds: the consolidation tail, newest first.
	body = append(body, s.Subtle.Render("recent folds"))
	if len(p.st.Folds) == 0 {
		body = append(body, fit(s.Faint.Render("no folds yet")))
	} else {
		for _, f := range p.st.Folds {
			body = append(body, fit(s.Muted.Render(foldLine(f))))
		}
	}

	lines := tea.Frame(body, w, s.Border)
	x := area.Min.X + max((area.Dx()-w)/2, 0)
	y := area.Min.Y + max((area.Dy()-len(lines))/2, 0)
	for i, l := range lines {
		tea.DrawStyled(scr, uv.Rect(x, y+i, w, 1), l)
	}
	return nil
}

// results renders the search hits, capped, with the selected row highlighted
// and a freshness marker so a stale memory reads differently from a fresh one.
func (p *Panel) results(inner int) []string {
	s := p.th.S
	if len(p.st.Results) == 0 {
		if p.st.Searched {
			return []string{ansi.Truncate(s.Faint.Render("no matches"), inner, "…")}
		}
		return []string{ansi.Truncate(s.Faint.Render("type a query and press enter"), inner, "…")}
	}
	var out []string
	for i, h := range p.st.Results {
		if i >= resultCap {
			out = append(out, s.Faint.Render(fmt.Sprintf("  … %d more", len(p.st.Results)-resultCap)))
			break
		}
		mark := s.Success.Render("fresh")
		if h.Stale {
			mark = s.Warning.Render("stale")
		}
		row := "[" + mark + "] " + h.Label
		if snip := snippet(h.Body); snip != "" {
			row += "  " + s.Faint.Render(snip)
		}
		row = ansi.Truncate(row, inner-2, "…")
		if i == p.st.Selected {
			out = append(out, s.Selected.Render("▸ ")+row)
		} else {
			out = append(out, "  "+row)
		}
	}
	return out
}

// snippet is the first line of a body, for the one-line hit render.
func snippet(body string) string {
	body = strings.TrimSpace(body)
	if i := strings.IndexByte(body, '\n'); i >= 0 {
		body = body[:i]
	}
	return body
}

// foldLine renders one fold as its net effect: rows written, reflections among
// them, rows archived, and candidates weighed.
func foldLine(f Fold) string {
	return fmt.Sprintf("%s  +%d (%d refl), %d archived, %d seen",
		f.Namespace, f.Merged, f.Reflections, f.Archived, f.Candidates)
}
