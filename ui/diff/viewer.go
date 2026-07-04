package diff

import (
	"fmt"

	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/theme"
)

// Viewer is the stateful view over a single diff: the pure Render draws
// the cells, and the Viewer adds what a person needs on top of them,
// namely a live unified/split toggle, per-hunk navigation with a counter,
// and horizontal scroll for lines too wide to fit (doc 02 section 9, plan
// 02 slice 2). It holds no terminal state of its own beyond a size, a
// mode, a focused hunk, and a horizontal offset, so the same Viewer with
// the same fields yields the same frame, which is what the goldens pin.
type Viewer struct {
	diffText string
	th       theme.Theme

	width  int
	height int
	mode   Mode

	focus int // index into the current layout's hunk anchors
	hcol  int // horizontal scroll offset in cells
}

// NewViewer builds a viewer over diffText in Auto layout. Size defaults
// to something usable until SetSize arrives from the first resize.
func NewViewer(diffText string, th theme.Theme) Viewer {
	return Viewer{diffText: diffText, th: th, width: 80, height: 20, mode: Auto}
}

// SetSize records the space the viewer draws into. Width feeds the
// Auto layout threshold; height bounds the visible window.
func (v *Viewer) SetSize(width, height int) {
	if width > 0 {
		v.width = width
	}
	if height > 0 {
		v.height = height
	}
}

// SetMode forces a layout; Auto lets width decide.
func (v *Viewer) SetMode(m Mode) { v.mode = m; v.hcol = 0 }

// Toggle flips between unified and split, the live toggle the DoD asks
// for. From Auto it commits to the opposite of what Auto would pick at
// the current width, so the first press always visibly changes layout.
func (v *Viewer) Toggle() {
	switch v.mode {
	case Split:
		v.mode = Unified
	case Unified:
		v.mode = Split
	default: // Auto
		if v.width >= splitThreshold {
			v.mode = Unified
		} else {
			v.mode = Split
		}
	}
	v.hcol = 0
}

// Mode reports the layout the viewer will draw with.
func (v Viewer) Mode() Mode { return v.mode }

// Hunks is how many hunks the current layout has.
func (v Viewer) Hunks() int { return len(v.anchors()) }

// Focus is the zero-based index of the focused hunk, clamped into range.
func (v Viewer) Focus() int {
	n := len(v.anchors())
	if n == 0 {
		return 0
	}
	return clamp(v.focus, 0, n-1)
}

// NextHunk and PrevHunk move the focus and reset horizontal scroll, so a
// jump always lands with the hunk's left edge in view.
func (v *Viewer) NextHunk() {
	if n := len(v.anchors()); n > 0 {
		v.focus = clamp(v.Focus()+1, 0, n-1)
		v.hcol = 0
	}
}

func (v *Viewer) PrevHunk() {
	if n := len(v.anchors()); n > 0 {
		v.focus = clamp(v.Focus()-1, 0, n-1)
		v.hcol = 0
	}
}

// ScrollRight and ScrollLeft pan a wide line horizontally rather than
// wrapping it into noise.
func (v *Viewer) ScrollRight() { v.hcol += hStep }
func (v *Viewer) ScrollLeft()  { v.hcol = max(v.hcol-hStep, 0) }

const hStep = 8

// View renders the visible window: a counter frame line, then the diff
// body scrolled so the focused hunk sits at the top and panned by the
// horizontal offset, each line clipped to width.
func (v Viewer) View() []string {
	lines, anchors := layout(v.diffText, v.width, v.th, v.mode)
	counter := v.counter(len(anchors))
	body := max(v.height-1, 1)

	top := 0
	if len(anchors) > 0 {
		top = anchors[clamp(v.focus, 0, len(anchors)-1)]
	}
	// Never scroll past the end: keep the last window full when possible.
	if maxTop := max(len(lines)-body, 0); top > maxTop {
		top = maxTop
	}
	end := min(top+body, len(lines))

	out := make([]string, 0, body+1)
	out = append(out, counter)
	for _, l := range lines[top:end] {
		out = append(out, v.pan(l))
	}
	return out
}

// counter is the frame line: which hunk of how many, and the live layout.
func (v Viewer) counter(n int) string {
	layoutName := "unified"
	if v.mode == Split || (v.mode == Auto && v.width >= splitThreshold) {
		layoutName = "split"
	}
	var label string
	if n == 0 {
		label = fmt.Sprintf("no hunks · %s", layoutName)
	} else {
		label = fmt.Sprintf("hunk %d/%d · %s", v.Focus()+1, n, layoutName)
	}
	return ansi.Truncate(v.th.S.Faint.Render(label), v.width, "…")
}

// pan clips one styled line to the visible horizontal window, dropping
// hcol cells from the left when the user has scrolled right.
func (v Viewer) pan(line string) string {
	if v.hcol <= 0 {
		return ansi.Truncate(line, v.width, "…")
	}
	return ansi.Truncate(ansi.Cut(line, v.hcol, ansi.StringWidth(line)), v.width, "…")
}

// anchors is the hunk-header line index list for the current layout.
func (v Viewer) anchors() []int {
	_, a := layout(v.diffText, v.width, v.th, v.mode)
	return a
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}
