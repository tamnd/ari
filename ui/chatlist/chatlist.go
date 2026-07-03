// Package chatlist is the lazy chat scrollback (doc 02 section 5). It is
// deliberately not a viewport: a viewport renders the whole conversation
// into one tall string on every frame, which is quadratic across a
// session. This list renders only the items the window can show, caches
// each item's lines under identity plus width plus version, and never
// re-renders a finished item, which is what keeps hour-old sessions as
// smooth as new ones (D23).
//
// The list knows nothing about ants, tools, or the core. It is a generic
// lazy list of Item; the chat controller adapts message parts into items.
package chatlist

import (
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// ItemID is an item's stable identity for the life of the session.
type ItemID string

// Item is one entry in the chat. Identity is stable for the item's life,
// Version bumps whenever its content changes, and Finished true means the
// version will never bump again, which is what lets the memo freeze it.
type Item interface {
	Identity() ItemID
	Version() uint64
	Finished() bool
	Render(width int, th theme.Theme) []uv.Line
}

type memoKey struct {
	id      ItemID
	width   int
	version uint64
}

type memoEntry struct {
	key   memoKey
	lines []uv.Line
}

// List stacks items vertically and scrolls over them. The scroll position
// is (item index, line within item), not an absolute line, so an item
// above the window growing during streaming never shifts what the reader
// is looking at.
type List struct {
	th    theme.Theme
	items []Item

	offsetIdx  int  // first at least partially visible item
	offsetLine int  // line within that item at the top of the window
	follow     bool // stick to the bottom as new content arrives

	width  int // set from the draw area; a change drops the whole memo
	height int // last drawn height, used to clamp scrolling
	memo   map[ItemID]memoEntry
}

// New builds an empty list in follow mode, the state a fresh chat is in.
func New(th theme.Theme) *List {
	return &List{th: th, follow: true, memo: map[ItemID]memoEntry{}}
}

// SetItems replaces the item slice. Memo entries for items that are gone
// are dropped; entries for items that stayed keep their cache.
func (l *List) SetItems(items []Item) {
	l.items = items
	keep := make(map[ItemID]memoEntry, len(items))
	for _, it := range items {
		if e, ok := l.memo[it.Identity()]; ok {
			keep[it.Identity()] = e
		}
	}
	l.memo = keep
}

// Append adds one item at the bottom.
func (l *List) Append(it Item) {
	l.items = append(l.items, it)
}

// Len reports how many items the list holds.
func (l *List) Len() int { return len(l.items) }

// Follow reports whether the list is stuck to the bottom.
func (l *List) Follow() bool { return l.follow }

// ScrollToBottom re-enters follow mode.
func (l *List) ScrollToBottom() {
	l.follow = true
}

// ScrollBy moves the window by delta lines, negative up. Any scroll drops
// out of follow mode unless it lands back on the bottom edge.
func (l *List) ScrollBy(delta int) {
	if l.width <= 0 {
		return // nothing drawn yet, nothing to scroll over
	}
	heights := l.heights()
	total := 0
	for _, h := range heights {
		total += h
	}
	abs := l.offsetLine
	for i := 0; i < l.offsetIdx && i < len(heights); i++ {
		abs += heights[i]
	}
	if l.follow {
		abs = total - l.height
	}
	abs = min(max(abs+delta, 0), max(total-l.height, 0))
	l.follow = abs >= total-l.height

	l.offsetIdx, l.offsetLine = 0, 0
	for i, h := range heights {
		if abs < h {
			l.offsetIdx, l.offsetLine = i, abs
			break
		}
		abs -= h
	}
}

// AtBottom reports whether the window's last line is the content's last
// line.
func (l *List) AtBottom() bool {
	if l.follow {
		return true
	}
	heights := l.heights()
	total := 0
	for _, h := range heights {
		total += h
	}
	abs := l.offsetLine
	for i := 0; i < l.offsetIdx && i < len(heights); i++ {
		abs += heights[i]
	}
	return abs >= total-l.height
}

// TotalHeight measures the whole conversation at the current width. It
// renders through the memo, so frozen items are measured from cache; the
// scrollbar and scroll math use this, the draw path never does.
func (l *List) TotalHeight() int {
	total := 0
	for _, h := range l.heights() {
		total += h
	}
	return total
}

func (l *List) heights() []int {
	hs := make([]int, len(l.items))
	for i, it := range l.items {
		hs[i] = len(l.render(it))
	}
	return hs
}

// render returns the item's lines, cached under identity, width, and
// version. A finished item's version never bumps, so its first render is
// its last until the width changes.
func (l *List) render(it Item) []uv.Line {
	id := it.Identity()
	want := memoKey{id, l.width, it.Version()}
	if e, ok := l.memo[id]; ok && e.key == want {
		return e.lines
	}
	lines := it.Render(l.width, l.th)
	l.memo[id] = memoEntry{want, lines}
	return lines
}

// Draw paints the visible window of the list into area. In follow mode it
// lays the items out bottom-up from the last one, so only the items that
// fit are ever rendered; otherwise it walks down from the scroll offset.
// Either way, items outside the window are not touched this frame.
func (l *List) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	if w := area.Dx(); w != l.width {
		// Every wrap boundary moves with the width; the memo is all
		// stale at once. Dropping the map beats leaking entries keyed by
		// widths that will never come back.
		l.width = w
		l.memo = map[ItemID]memoEntry{}
	}
	l.height = area.Dy()
	if l.width <= 0 || l.height <= 0 || len(l.items) == 0 {
		return nil
	}
	scr = tea.Clip(scr, area)
	if l.follow {
		l.drawBottomUp(scr, area)
	} else {
		l.clampOffset()
		l.drawTopDown(scr, area)
	}
	return nil
}

func (l *List) drawBottomUp(scr uv.Screen, area uv.Rectangle) {
	y := area.Max.Y
	for i := len(l.items) - 1; i >= 0 && y > area.Min.Y; i-- {
		lines := l.render(l.items[i])
		top := y - len(lines)
		for row, line := range lines {
			if yy := top + row; yy >= area.Min.Y && yy < area.Max.Y {
				drawLine(scr, area, yy, line)
			}
		}
		y = top
		// Keep the offset in sync with what is on screen, so leaving
		// follow mode continues from the same window.
		l.offsetIdx, l.offsetLine = i, max(area.Min.Y-top, 0)
	}
}

func (l *List) drawTopDown(scr uv.Screen, area uv.Rectangle) {
	y := area.Min.Y
	skip := l.offsetLine
	for i := l.offsetIdx; i < len(l.items) && y < area.Max.Y; i++ {
		lines := l.render(l.items[i])
		for row := skip; row < len(lines) && y < area.Max.Y; row++ {
			drawLine(scr, area, y, lines[row])
			y++
		}
		skip = 0
	}
}

// clampOffset repairs the scroll position after items shrank or vanished.
func (l *List) clampOffset() {
	if l.offsetIdx >= len(l.items) {
		l.offsetIdx = len(l.items) - 1
		l.offsetLine = 0
	}
	if h := len(l.render(l.items[l.offsetIdx])); l.offsetLine >= h {
		l.offsetLine = max(h-1, 0)
	}
}

// drawLine copies one cell line onto the screen row, clipped to the area.
// x advances by each cell's width, so wide glyphs land where they should.
func drawLine(scr uv.Screen, area uv.Rectangle, y int, line uv.Line) {
	x := area.Min.X
	for i := range line {
		c := line[i]
		if c.Width == 0 {
			continue // placeholder inside a wide cell, owned by its head
		}
		if x+c.Width > area.Max.X {
			break
		}
		scr.SetCell(x, y, &c)
		x += c.Width
	}
}
