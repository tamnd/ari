package chatlist

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

// fake is a scriptable item: fixed identity, mutable version and line
// count, and a render counter, which is how the memo and freeze rules
// become assertions instead of hopes.
type fake struct {
	id       ItemID
	version  uint64
	finished bool
	n        int // lines to render
	renders  int
}

func (f *fake) Identity() ItemID { return f.id }
func (f *fake) Version() uint64  { return f.version }
func (f *fake) Finished() bool   { return f.finished }

func (f *fake) Render(width int, _ theme.Theme) []uv.Line {
	f.renders++
	lines := make([]uv.Line, f.n)
	for i := range lines {
		text := fmt.Sprintf("%s/%d w%d", f.id, i, width)
		if len(text) > width {
			text = text[:width]
		}
		lines[i] = tea.ToLines(text)[0]
	}
	return lines
}

func draw(l *List, w, h int) *uv.Buffer {
	buf := uv.NewScreenBuffer(w, h)
	l.Draw(buf, uv.Rect(0, 0, w, h))
	return buf.Buffer
}

// screen flattens a buffer to plain text rows for asserting.
func screen(buf *uv.Buffer) []string {
	return strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
}

func items(fs ...*fake) []Item {
	out := make([]Item, len(fs))
	for i, f := range fs {
		out[i] = f
	}
	return out
}

// TestMemoServesUnchanged: a second draw over unchanged items renders
// nothing again.
func TestMemoServesUnchanged(t *testing.T) {
	a := &fake{id: "a", n: 2}
	b := &fake{id: "b", n: 2}
	l := New(theme.Dark())
	l.SetItems(items(a, b))
	draw(l, 20, 10)
	draw(l, 20, 10)
	if a.renders != 1 || b.renders != 1 {
		t.Fatalf("renders after two draws: a=%d b=%d, want 1 and 1", a.renders, b.renders)
	}
}

// TestVersionBumpRerendersOne: bumping one item's version re-renders that
// item and nothing else.
func TestVersionBumpRerendersOne(t *testing.T) {
	frozen := &fake{id: "frozen", n: 2, finished: true}
	live := &fake{id: "live", n: 2}
	l := New(theme.Dark())
	l.SetItems(items(frozen, live))
	draw(l, 20, 10)
	for range 5 {
		live.version++
		live.n++
		draw(l, 20, 10)
	}
	if frozen.renders != 1 {
		t.Errorf("frozen item rendered %d times, want exactly 1", frozen.renders)
	}
	if live.renders != 6 {
		t.Errorf("live item rendered %d times, want 6", live.renders)
	}
}

// TestWidthChangeRerendersAll: a resize is the one event that re-renders
// everything, frozen included.
func TestWidthChangeRerendersAll(t *testing.T) {
	a := &fake{id: "a", n: 2, finished: true}
	l := New(theme.Dark())
	l.SetItems(items(a))
	draw(l, 20, 10)
	draw(l, 30, 10)
	if a.renders != 2 {
		t.Fatalf("renders after width change: %d, want 2", a.renders)
	}
	draw(l, 30, 10)
	if a.renders != 2 {
		t.Fatalf("stable width re-rendered: %d, want still 2", a.renders)
	}
}

// TestOnlyVisibleRender: in follow mode over a long history, items above
// the window are never rendered at all.
func TestOnlyVisibleRender(t *testing.T) {
	fs := make([]*fake, 40)
	for i := range fs {
		fs[i] = &fake{id: ItemID(fmt.Sprintf("i%02d", i)), n: 3, finished: true}
	}
	l := New(theme.Dark())
	l.SetItems(items(fs...))
	draw(l, 20, 9) // fits 3 items of 3 lines
	rendered := 0
	for _, f := range fs {
		if f.renders > 0 {
			rendered++
		}
	}
	// Bottom-up layout renders the window plus at most the one item that
	// straddles the top edge.
	if rendered > 4 {
		t.Fatalf("%d of 40 items rendered for a 9-line window", rendered)
	}
	for _, f := range fs[:20] {
		if f.renders != 0 {
			t.Fatalf("item %s far above the window was rendered", f.id)
		}
	}
}

// TestFollowShowsBottom: follow mode pins the last content line to the
// bottom row.
func TestFollowShowsBottom(t *testing.T) {
	a := &fake{id: "a", n: 4}
	b := &fake{id: "b", n: 4}
	l := New(theme.Dark())
	l.SetItems(items(a, b))
	rows := screen(draw(l, 20, 5))
	want := []string{"a/3 w20", "b/0 w20", "b/1 w20", "b/2 w20", "b/3 w20"}
	for i, w := range want {
		if rows[i] != w {
			t.Fatalf("row %d = %q, want %q (all rows %q)", i, rows[i], w, rows)
		}
	}
}

// TestScrollStability: with follow off, an item above the window growing
// does not move what the reader is looking at.
func TestScrollStability(t *testing.T) {
	above := &fake{id: "above", n: 3}
	cur := &fake{id: "cur", n: 6}
	l := New(theme.Dark())
	l.SetItems(items(above, cur))
	draw(l, 20, 4)
	l.ScrollBy(-2) // window now starts inside cur's lines
	before := screen(draw(l, 20, 4))

	above.version++
	above.n = 30 // the item above grows a lot
	after := screen(draw(l, 20, 4))
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("row %d moved: %q then %q", i, before[i], after[i])
		}
	}
	if l.Follow() {
		t.Fatal("scrolling up should leave follow mode")
	}
}

// TestScrollBackToBottomResumesFollow: scrolling down to the end re-arms
// follow.
func TestScrollBackToBottomResumesFollow(t *testing.T) {
	a := &fake{id: "a", n: 10}
	l := New(theme.Dark())
	l.SetItems(items(a))
	draw(l, 20, 4)
	l.ScrollBy(-3)
	if l.Follow() {
		t.Fatal("still following after scrolling up")
	}
	l.ScrollBy(3)
	if !l.Follow() {
		t.Fatal("not following after scrolling back to the bottom")
	}
}

// TestTotalHeight measures through the memo.
func TestTotalHeight(t *testing.T) {
	a := &fake{id: "a", n: 3}
	b := &fake{id: "b", n: 5}
	l := New(theme.Dark())
	l.SetItems(items(a, b))
	draw(l, 20, 4)
	if got := l.TotalHeight(); got != 8 {
		t.Fatalf("TotalHeight = %d, want 8", got)
	}
}

// TestSetItemsPrunesMemo: replacing items drops cache entries for the
// ones that are gone and keeps the rest.
func TestSetItemsPrunesMemo(t *testing.T) {
	a := &fake{id: "a", n: 2, finished: true}
	b := &fake{id: "b", n: 2, finished: true}
	l := New(theme.Dark())
	l.SetItems(items(a, b))
	draw(l, 20, 10)
	l.SetItems(items(a))
	draw(l, 20, 10)
	if a.renders != 1 {
		t.Errorf("surviving item re-rendered: %d", a.renders)
	}
	if _, ok := l.memo["b"]; ok {
		t.Error("memo kept an entry for a removed item")
	}
}

// TestInBounds: the widget never writes outside its rectangle, in either
// layout mode.
func TestInBounds(t *testing.T) {
	a := &fake{id: "a", n: 8}
	b := &fake{id: "b", n: 8}
	l := New(theme.Dark())
	l.SetItems(items(a, b))
	tea.AssertInBounds(t, l, 12, 5)
	l.ScrollBy(-4)
	tea.AssertInBounds(t, l, 12, 5)
}

// TestDrawGolden pins a full frame: a scrolled window over fixed items.
func TestDrawGolden(t *testing.T) {
	fs := []*fake{
		{id: "one", n: 2, finished: true},
		{id: "two", n: 3, finished: true},
		{id: "three", n: 2},
	}
	l := New(theme.Dark())
	l.SetItems(items(fs[0], fs[1], fs[2]))
	follow := draw(l, 16, 4).String()
	l.ScrollBy(-2)
	scrolled := draw(l, 16, 4).String()
	got := "== follow ==\n" + follow + "== scrolled -2 ==\n" + scrolled
	eval.Golden(t, "draw_frames", got)
}
