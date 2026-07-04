package memory

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// IndexCap bounds the pinned index so it always fits in the cached prompt
// prefix and stays cheap enough to afford every turn. The caps force the
// consolidator to compress rather than accrete: a colony that would overflow
// them must fold its pins tighter, not spend more of the window (D14,
// research/memory_swarm.md section 2, recommendation 3).
type IndexCap struct {
	// Lines is the hard ceiling on rendered lines, the Claude Code auto-memory
	// shape. Pins past it are summarized in one overflow line, never dropped
	// silently.
	Lines int
	// PerLine is the hard ceiling on a single line's length in bytes; a longer
	// line is truncated with an ellipsis rather than allowed to overflow.
	PerLine int
}

// DefaultIndexCap is the Claude Code auto-memory shape: a hundred lines of
// roughly a hundred and sixty characters, small enough to always afford in the
// cached prefix.
var DefaultIndexCap = IndexCap{Lines: 100, PerLine: 160}

// Row is one pinned memory as the index renders it, decoupled from the store's
// row so the renderer stays a pure function of its inputs. Label is the pin's
// short handle; Anchors are its file, symbol, or command references, rendered as
// a trailing hint so the ant knows what a pin is about without a recall.
type Row struct {
	Label   string
	Anchors []string
}

// RenderIndex builds the pinned index markdown for a namespace from its pinned
// rows. It is pure and deterministic: same rows in, same bytes out, so the
// prompt prefix is stable between folds and the cache_control breakpoint holds
// (D14). The index is rebuilt only by the consolidator at a fold boundary,
// never on a turn. An empty row set renders the empty string; the prompt
// assembler owns the "no pins yet" wording.
func RenderIndex(rows []Row, cap IndexCap) string {
	if cap.Lines <= 0 {
		cap.Lines = DefaultIndexCap.Lines
	}
	if cap.PerLine <= 0 {
		cap.PerLine = DefaultIndexCap.PerLine
	}
	if len(rows) == 0 {
		return ""
	}

	// Reserve the last line for an overflow marker when there are more pins than
	// the cap allows, so the truncation is visible rather than silent.
	shown := rows
	overflow := 0
	if len(rows) > cap.Lines {
		shown = rows[:cap.Lines-1]
		overflow = len(rows) - len(shown)
	}

	var b strings.Builder
	for i, r := range shown {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(truncateLine(indexLine(r), cap.PerLine))
	}
	if overflow > 0 {
		b.WriteByte('\n')
		fmt.Fprintf(&b, "- (+%d more pinned, compressed at the next fold)", overflow)
	}
	return b.String()
}

// indexLine renders one pin as a bullet: its label, then its anchors in
// parentheses when it has any, so a line reads "- run make gen (file:schema.go)".
func indexLine(r Row) string {
	line := "- " + strings.TrimSpace(r.Label)
	if len(r.Anchors) > 0 {
		line += " (" + strings.Join(r.Anchors, ", ") + ")"
	}
	return line
}

// truncateLine caps a line at max bytes, cutting on a rune boundary and marking
// the cut with an ellipsis so a long line never overflows the budget nor splits
// a character.
func truncateLine(line string, max int) string {
	if len(line) <= max {
		return line
	}
	// Leave room for the ellipsis rune, then back off to a rune boundary.
	cut := max - len("…")
	if cut < 0 {
		cut = 0
	}
	for cut > 0 && !utf8.RuneStart(line[cut]) {
		cut--
	}
	return strings.TrimRight(line[:cut], " ") + "…"
}
