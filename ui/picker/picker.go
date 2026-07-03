// Package picker is the one filterable list dialog: the model picker,
// the session switcher, and the command palette are all a Dialog with
// different items and a different ID (doc 02 section 7.1). Selecting
// produces an opaque Chosen action; the controller that pushed the
// dialog interprets the key, so the picker knows nothing about models,
// sessions, or commands.
package picker

import (
	"strings"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// Item is one selectable row. Key is what Chosen carries; Label is what
// the user reads; Detail is a dim annotation on the same row.
type Item struct {
	Key    string
	Label  string
	Detail string
}

// Chosen is the opaque action a selection produces.
type Chosen struct {
	ID  string // the dialog's id, so the applier knows which picker fired
	Key string // the selected item's key
}

// visibleCap bounds the list so a long session history stays a dialog,
// not a full-screen takeover.
const visibleCap = 8

// Dialog is a filter-as-you-type list. Printable keys narrow the list,
// backspace widens it, up and down move, enter selects.
type Dialog struct {
	id     string
	title  string
	items  []Item
	th     theme.Theme
	filter string
	focus  int // index into the filtered view
}

// New builds a picker over items.
func New(id, title string, items []Item, th theme.Theme) *Dialog {
	return &Dialog{id: id, title: title, items: items, th: th}
}

// ID names the dialog instance; the overlay uses it for reopen grace.
func (d *Dialog) ID() string { return d.id }

// filtered returns the items matching the filter, case-insensitive over
// key and label.
func (d *Dialog) filtered() []Item {
	if d.filter == "" {
		return d.items
	}
	q := strings.ToLower(d.filter)
	var out []Item
	for _, it := range d.items {
		if strings.Contains(strings.ToLower(it.Key), q) ||
			strings.Contains(strings.ToLower(it.Label), q) {
			out = append(out, it)
		}
	}
	return out
}

// HandleMsg narrows, moves, or selects. Anything the picker does not
// understand returns nil and stays put.
func (d *Dialog) HandleMsg(msg btea.Msg) any {
	k, ok := msg.(btea.KeyPressMsg)
	if !ok {
		return nil
	}
	vis := d.filtered()
	switch k.Code {
	case btea.KeyUp:
		if len(vis) > 0 {
			d.focus = (d.focus + len(vis) - 1) % len(vis)
		}
	case btea.KeyDown:
		if len(vis) > 0 {
			d.focus = (d.focus + 1) % len(vis)
		}
	case btea.KeyEnter:
		if len(vis) == 0 {
			return nil
		}
		return Chosen{ID: d.id, Key: vis[min(d.focus, len(vis)-1)].Key}
	case btea.KeyBackspace:
		if d.filter != "" {
			d.filter = d.filter[:len(d.filter)-1]
			d.focus = 0
		}
	default:
		if k.Text != "" {
			d.filter += k.Text
			d.focus = 0
		}
	}
	return nil
}

// Draw paints the picker centered in area.
func (d *Dialog) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	s := d.th.S
	w := min(max(area.Dx()-8, 24), 56)

	vis := d.filtered()
	if d.focus >= len(vis) {
		d.focus = max(len(vis)-1, 0)
	}
	body := []string{
		s.Title.Render(d.title),
		s.Muted.Render("› ") + s.Base.Render(d.filter) + s.Muted.Render("_"),
		"",
	}
	// Keep the focused row inside the capped window.
	start := 0
	if d.focus >= visibleCap {
		start = d.focus - visibleCap + 1
	}
	for i := start; i < len(vis) && i < start+visibleCap; i++ {
		row := vis[i].Label
		if vis[i].Detail != "" {
			row += "  " + s.Faint.Render(vis[i].Detail)
		}
		row = ansi.Truncate(row, w-6, "…")
		if i == d.focus {
			body = append(body, s.Selected.Render("▸ ")+row)
		} else {
			body = append(body, "  "+row)
		}
	}
	if len(vis) == 0 {
		body = append(body, s.Faint.Render("no matches"))
	}
	if n := len(vis) - start - visibleCap; n > 0 {
		body = append(body, s.Faint.Render(strings.Repeat(" ", 2)+"…"))
	}

	lines := tea.Frame(body, w, s.Border)
	x := area.Min.X + max((area.Dx()-w)/2, 0)
	y := area.Min.Y + max((area.Dy()-len(lines))/2, 0)
	for i, l := range lines {
		tea.DrawStyled(scr, uv.Rect(x, y+i, w, 1), l)
	}
	return nil
}
