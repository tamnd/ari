// Package tea holds the one rendering contract every widget in the UI
// implements: paint yourself onto cells within a rectangle (doc 02
// section 2.5). Widgets never return strings; ultraviolet damage-diffs
// the cell buffer, rectangles compose without reflow, and a layout bug
// stays contained inside its area instead of smearing across the screen.
package tea

import (
	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
)

// Cursor is re-exported so widget packages depend on this contract
// package alone.
type Cursor = btea.Cursor

// Widget paints itself into area on scr and returns where the cursor
// should sit, or nil if it does not own the cursor this frame.
type Widget interface {
	Draw(scr uv.Screen, area uv.Rectangle) *Cursor
}

// DrawStyled paints an ANSI-styled string into area, clipped to it.
// It is the one helper for putting lipgloss-styled content onto cells,
// so no widget hand-rolls the string-to-cell conversion.
func DrawStyled(scr uv.Screen, area uv.Rectangle, s string) {
	ss := uv.NewStyledString(s)
	ss.Wrap = false
	ss.Draw(&clipped{scr: scr, area: area}, area)
}

// clipped wraps a screen and drops writes outside area, enforcing the
// stay-inside-your-rectangle rule at the seam instead of trusting every
// caller's arithmetic.
type clipped struct {
	scr  uv.Screen
	area uv.Rectangle
}

func (c *clipped) Bounds() uv.Rectangle { return c.area }

func (c *clipped) CellAt(x, y int) *uv.Cell { return c.scr.CellAt(x, y) }

func (c *clipped) WidthMethod() uv.WidthMethod { return c.scr.WidthMethod() }

func (c *clipped) SetCell(x, y int, cell *uv.Cell) {
	if !inRect(x, y, c.area) {
		return
	}
	c.scr.SetCell(x, y, cell)
}

// Clip returns a screen that silently drops writes outside area.
func Clip(scr uv.Screen, area uv.Rectangle) uv.Screen {
	return &clipped{scr: scr, area: area}
}

func inRect(x, y int, r uv.Rectangle) bool {
	return x >= r.Min.X && x < r.Max.X && y >= r.Min.Y && y < r.Max.Y
}
