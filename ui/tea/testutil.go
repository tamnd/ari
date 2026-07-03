package tea

import (
	"fmt"

	uv "github.com/charmbracelet/ultraviolet"
)

// TB is the testing surface AssertInBounds needs, matching kernel/eval.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// AssertInBounds paints w into an area surrounded by sentinel cells and
// fails if any cell outside the area changed (doc 02 section 2.5). Every
// widget package runs its widgets through this, so a layout bug is a
// test failure here, not a smeared screen in production.
func AssertInBounds(t TB, w Widget, width, height int) {
	t.Helper()
	const pad = 3
	buf := uv.NewScreenBuffer(width+2*pad, height+2*pad)
	sentinel := &uv.Cell{Content: "·", Width: 1}
	buf.Fill(sentinel)

	area := uv.Rect(pad, pad, width, height)
	w.Draw(buf, area)

	b := buf.Bounds()
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if inRect(x, y, area) {
				continue
			}
			if c := buf.CellAt(x, y); c == nil || !c.Equal(sentinel) {
				t.Fatalf("widget escaped its rectangle: cell (%d,%d) outside %v changed to %q",
					x, y, area, cellContent(c))
			}
		}
	}
}

func cellContent(c *uv.Cell) string {
	if c == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s (style %v)", c.Content, c.Style)
}
