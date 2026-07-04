// Package colonyview is the live colony panel (doc 09, doc 02 section 10.5):
// an ant list with each ant's glyph, accent color, status, and running token
// spend, and a read-only drill-in into any ant's sidechain transcript. It is a
// pure projection of typed colony events the root feeds it, exactly like the
// sidebar: no section reads core directly and nothing here backpressures an
// agent (D2, D18). Same state in, same frame out, which is what makes the
// golden tests sound.
package colonyview

import (
	"fmt"
	"slices"
	"strings"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/parts"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// Status is where an ant sits in its lifecycle, projected from the colony's
// must-deliver lifecycle events (woke, blocked, finished) so the list is never
// wrong about who is alive (doc 02 section 10.5, D18).
type Status int

const (
	Dormant Status = iota // known to the colony but not awake
	Awake                 // running a turn
	Blocked               // stopped on a Question, wants the user's attention
	Done                  // finished its task
)

// rank orders the list: a blocked ant floats to the top because it wants
// attention, then the awake ants above the dormant ones, and the finished ants
// settle to the bottom.
func (s Status) rank() int {
	switch s {
	case Blocked:
		return 0
	case Awake:
		return 1
	case Dormant:
		return 2
	default: // Done
		return 3
	}
}

func (s Status) label() string {
	switch s {
	case Awake:
		return "awake"
	case Blocked:
		return "blocked"
	case Done:
		return "done"
	default:
		return "dormant"
	}
}

// Ant is one row of the list: who it is, where it is, what it has spent, and
// the Question it is stuck on when blocked. Tokens is the ledger's per-ant
// spend, shown to the token so what the user sees is what the core counted.
type Ant struct {
	ID       string
	Name     string
	Status   Status
	Tokens   int64
	Question string // the pending ask, shown inline when Blocked
}

// State is everything the view renders. The root projects it from broker
// events; the view only draws it. When Focused names an ant, the drill-in pane
// shows that ant's sidechain Transcript instead of the list.
type State struct {
	Ants       []Ant
	Selected   string       // ant id the list cursor sits on, "" for none
	Focused    string       // ant id whose transcript is open, "" for the list
	Transcript []parts.Part // the focused ant's sidechain, rendered read-only
}

// Order sorts a copy of the ants the way the list draws them: a blocked ant
// floats to the top because it wants attention, then the awake ants above the
// dormant ones, and the finished ants settle to the bottom, ties broken by
// name. The controller shares this so its cursor walks the ants in the same
// order the user sees, never a hidden insertion order.
func Order(ants []Ant) []Ant {
	out := slices.Clone(ants)
	slices.SortStableFunc(out, func(a, b Ant) int {
		if r := a.Status.rank() - b.Status.rank(); r != 0 {
			return r
		}
		return strings.Compare(a.Name, b.Name)
	})
	return out
}

// View renders State into its area.
type View struct {
	th theme.Theme
	st State
}

// New builds a colony view.
func New(th theme.Theme) *View { return &View{th: th} }

// SetState replaces the projected state.
func (v *View) SetState(st State) { v.st = st }

// SetTheme swaps the palette.
func (v *View) SetTheme(th theme.Theme) { v.th = th }

// Draw paints the list, or the drill-in transcript when an ant is focused.
func (v *View) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	lines := v.render(area.Dx(), area.Dy())
	for i, l := range lines {
		if i >= area.Dy() {
			break
		}
		row := uv.Rect(area.Min.X, area.Min.Y+i, area.Dx(), 1)
		tea.DrawStyled(scr, row, l)
	}
	return nil
}

func (v *View) render(width, height int) []string {
	if v.st.Focused != "" {
		return v.renderDrill(width, height)
	}
	return v.renderList(width, height)
}

// renderList draws the sorted ant list. Each row is one ant: its glyph in its
// accent color, its name, its status, and its running token count; a blocked
// ant carries its pending Question on the line below so the user can answer
// from the list without drilling in.
func (v *View) renderList(width, height int) []string {
	th := v.th.S
	fit := func(l string) string { return ansi.Truncate(l, width, "…") }

	ants := Order(v.st.Ants)

	out := []string{fit(th.Subtle.Render("colony"))}
	for _, a := range ants {
		acc := v.th.Accent(a.ID)
		glyph := th.Base.Foreground(acc.Color).Render(string(acc.Glyph))
		status := v.statusStyle(a.Status).Render(a.Status.label())
		tokens := th.Muted.Render(fmt.Sprintf("%d tok", a.Tokens))
		right := status + "  " + tokens
		nameStyle := th.Base
		if a.ID == v.st.Selected {
			nameStyle = th.Selected
		}
		name := nameStyle.Render(a.Name)
		pad := max(width-2-ansi.StringWidth(a.Name)-ansi.StringWidth(status)-ansi.StringWidth(tokens)-2, 1)
		out = append(out, fit(glyph+" "+name+fmt.Sprintf("%*s", pad, "")+right))
		if a.Status == Blocked && a.Question != "" {
			out = append(out, fit(th.Warning.Render("  ? "+a.Question)))
		}
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// renderDrill draws the focused ant's sidechain as a read-only transcript,
// using the same message-part renderer the main chat uses, so a colony
// transcript looks like a session because it is one.
func (v *View) renderDrill(width, height int) []string {
	th := v.th.S
	fit := func(l string) string { return ansi.Truncate(l, width, "…") }

	name := v.st.Focused
	for _, a := range v.st.Ants {
		if a.ID == v.st.Focused {
			name = a.Name
			break
		}
	}
	acc := v.th.Accent(v.st.Focused)
	head := th.Base.Foreground(acc.Color).Render(string(acc.Glyph)) + " " +
		th.Title.Render(name) + " " + th.Faint.Render("(read-only)")

	out := []string{fit(head), ""}
	for _, p := range v.st.Transcript {
		for _, l := range parts.Render(p, width, v.th) {
			out = append(out, fit(l))
		}
	}
	if len(out) > height {
		out = out[:height]
	}
	return out
}

// statusStyle colors a status so a blocked ant is visually distinct because it
// wants attention, an awake ant reads as live, and a finished one recedes.
func (v *View) statusStyle(s Status) interface{ Render(...string) string } {
	th := v.th.S
	switch s {
	case Awake:
		return th.Info
	case Blocked:
		return th.Warning
	case Done:
		return th.Faint
	default:
		return th.Muted
	}
}
