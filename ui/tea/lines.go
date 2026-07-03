package tea

import (
	uv "github.com/charmbracelet/ultraviolet"
)

// ToLines decomposes an ANSI-styled multi-line string into cell lines. It
// is the seam between the string-producing renderers (parts, markdown)
// and the cell-consuming chat list.
//
// It draws through a correctly-sized buffer rather than calling
// StyledString.Lines, which in the pinned ultraviolet build stops after
// the first cell (its internal print loop breaks on an empty bounds
// rectangle).
func ToLines(s string) []uv.Line {
	ss := uv.NewStyledString(s)
	buf := uv.NewScreenBuffer(max(ss.WcWidth(), 1), ss.Height())
	ss.Draw(buf, buf.Bounds())
	return buf.Lines
}
