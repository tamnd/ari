package tea

import (
	"testing"

	uv "github.com/charmbracelet/ultraviolet"
)

// TestToLinesRoundTrip: every rune of a plain multi-line string survives
// the conversion, one cell per column. This exists because the upstream
// StyledString.Lines silently truncates to one cell; if a dependency bump
// fixes that and ToLines moves back onto it, this still holds.
func TestToLinesRoundTrip(t *testing.T) {
	lines := ToLines("a/3 w20\nsecond")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	if got := lines[0].String(); got != "a/3 w20" {
		t.Errorf("line 0 = %q", got)
	}
	if got := lines[1].String(); got != "second" {
		t.Errorf("line 1 = %q", got)
	}
}

// TestToLinesStyled: SGR styling lands on the cells instead of leaking
// into their content.
func TestToLinesStyled(t *testing.T) {
	lines := ToLines("\x1b[1mbold\x1b[m plain")
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1", len(lines))
	}
	if got := lines[0].String(); got != "bold plain" {
		t.Fatalf("content = %q, want the text without escapes", got)
	}
	if lines[0].At(0).Style.Attrs == 0 {
		t.Error("first cell lost its bold attribute")
	}
}

// TestToLinesWide: a double-width rune occupies one cell of width 2.
func TestToLinesWide(t *testing.T) {
	lines := ToLines("蔵x")
	c := lines[0].At(0)
	if c.Width != 2 || c.Content != "蔵" {
		t.Fatalf("wide cell = %q width %d", c.Content, c.Width)
	}
}

// TestClip: writes outside the clip area are dropped, writes inside pass.
func TestClip(t *testing.T) {
	buf := uv.NewScreenBuffer(6, 6)
	area := uv.Rect(2, 2, 2, 2)
	scr := Clip(buf, area)
	in := &uv.Cell{Content: "x", Width: 1}
	scr.SetCell(2, 2, in)
	scr.SetCell(0, 0, in)
	scr.SetCell(4, 4, in)
	if got := buf.CellAt(2, 2); got == nil || got.Content != "x" {
		t.Error("write inside the clip area was dropped")
	}
	for _, p := range [][2]int{{0, 0}, {4, 4}} {
		if got := buf.CellAt(p[0], p[1]); got != nil && got.Content == "x" {
			t.Errorf("write outside the clip area at %v landed", p)
		}
	}
}
