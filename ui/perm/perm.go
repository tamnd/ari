// Package perm is the permission dialog (doc 02 section 9). It renders
// the core's PermissionRequested event: the consequence preview is
// shown exactly as the core rendered it, by kind, and never re-derived
// from tool input (D2, D15). Deny has the default focus, so the lowest
// energy answer is the safe one.
package perm

import (
	"fmt"
	"strings"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/ui/diff"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// Choice is one of the three answers.
type Choice int

const (
	Deny Choice = iota
	AllowOnce
	AllowSession
)

func (c Choice) label() string {
	switch c {
	case AllowOnce:
		return "allow once"
	case AllowSession:
		return "allow for session"
	default:
		return "deny"
	}
}

// Decision is the action this dialog returns through the overlay. The
// root maps it onto the core's permission reply; the dialog itself
// never touches the bus.
type Decision struct {
	ID     string // the request ID from the event
	Choice Choice
}

// Dialog renders one permission request. It implements ui/dialog.Dialog.
type Dialog struct {
	req   event.PermissionRequested
	th    theme.Theme
	focus Choice
}

// New builds the dialog for a request. Focus starts on deny.
func New(req event.PermissionRequested, th theme.Theme) *Dialog {
	return &Dialog{req: req, th: th, focus: Deny}
}

// ID keys grace arming to this exact request, so only a literal reopen
// of the same question skips the arming delay.
func (d *Dialog) ID() string { return "perm:" + d.req.ID }

// HandleMsg moves focus with arrows or tab and answers on enter, with
// d, a, and s as direct answers.
func (d *Dialog) HandleMsg(msg btea.Msg) any {
	key, ok := msg.(btea.KeyPressMsg)
	if !ok {
		return nil
	}
	switch key.String() {
	case "left", "shift+tab":
		d.focus = (d.focus + 2) % 3
	case "right", "tab":
		d.focus = (d.focus + 1) % 3
	case "enter":
		return Decision{ID: d.req.ID, Choice: d.focus}
	case "d", "n":
		return Decision{ID: d.req.ID, Choice: Deny}
	case "a", "y":
		return Decision{ID: d.req.ID, Choice: AllowOnce}
	case "s":
		return Decision{ID: d.req.ID, Choice: AllowSession}
	}
	return nil
}

// Draw centers the dialog box in area and paints it.
func (d *Dialog) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	lines := d.render(area.Dx(), area.Dy())
	w := 0
	for _, l := range lines {
		w = max(w, ansi.StringWidth(l))
	}
	h := len(lines)
	x := area.Min.X + max((area.Dx()-w)/2, 0)
	y := area.Min.Y + max((area.Dy()-h)/2, 0)
	box := uv.Rect(x, y, min(w, area.Dx()), min(h, area.Dy()))
	tea.DrawStyled(scr, box, strings.Join(lines, "\n"))
	return nil
}

// render lays the dialog out as styled lines for a viewport of the
// given size. The box takes most of the width, wider when the
// consequence is a diff so the split layout gets room.
func (d *Dialog) render(maxW, maxH int) []string {
	s := d.th.S
	w := min(maxW-4, 100)
	if d.req.Consequence.Kind == "diff" {
		w = maxW - 4
	}
	w = max(w, 20)
	inner := w - 4 // border and padding

	var body []string

	title := fmt.Sprintf("%s wants to run %s", d.req.Tool, d.req.Call)
	body = append(body, ansi.Truncate(s.Title.Render(title), inner, "…"))
	if d.req.Reason != "" {
		body = append(body, ansi.Truncate(s.Muted.Render(d.req.Reason), inner, "…"))
	}
	if d.req.Mode != "" && d.req.Mode != "default" {
		warn := fmt.Sprintf("mode %s asked anyway: this action needs an explicit answer", d.req.Mode)
		body = append(body, ansi.Truncate(s.Warning.Render("! "+warn), inner, "…"))
	}
	body = append(body, "")

	// The consequence, capped so the choices always stay on screen.
	budget := maxH - len(body) - 7 // border 2, blank 1, suggestions worst case 2, choices 1, spare 1
	body = append(body, d.consequence(inner, max(budget, 3))...)
	body = append(body, "")

	for _, sug := range d.req.Suggestions {
		body = append(body, ansi.Truncate(s.Info.Render("↪ "+sug), inner, "…"))
	}

	var choices []string
	for c := Deny; c <= AllowSession; c++ {
		if c == d.focus {
			choices = append(choices, s.Selected.Render("▸ "+c.label()+" "))
		} else {
			choices = append(choices, s.Subtle.Render("  "+c.label()+" "))
		}
	}
	body = append(body, ansi.Truncate(strings.Join(choices, " "), inner, "…"))

	return tea.Frame(body, w, s.Border)
}

// consequence renders the preview by kind. Every kind arrives
// pre-rendered as text from the core; this only styles and clips it.
func (d *Dialog) consequence(width, maxLines int) []string {
	s := d.th.S
	c := d.req.Consequence
	var out []string
	switch c.Kind {
	case "diff":
		out = diff.Render(c.Content, width, d.th, diff.Auto)
	case "command":
		for l := range strings.SplitSeq(strings.TrimRight(c.Content, "\n"), "\n") {
			out = append(out, ansi.Truncate(s.ToolInput.Render("$ "+l), width, "…"))
		}
	case "url":
		out = append(out, ansi.Truncate(s.Info.Underline(true).Render(c.Content), width, "…"))
	default: // json and anything future
		for l := range strings.SplitSeq(strings.TrimRight(c.Content, "\n"), "\n") {
			out = append(out, ansi.Truncate(s.ToolOutput.Render(l), width, "…"))
		}
	}
	if len(out) > maxLines {
		kept := out[:maxLines-1]
		note := fmt.Sprintf("… %d more lines", len(out)-len(kept))
		kept = append(kept, s.Faint.Render(note))
		return kept
	}
	return out
}
