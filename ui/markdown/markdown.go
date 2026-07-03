// Package markdown renders assistant prose. It owns two things: the
// one-shot Render every frozen message goes through, and the
// stable-prefix StreamCache that makes the one streaming message cheap
// (doc 02 section 6). Both paths share one normalizer, which is what
// makes "streamed render equals full render" a testable byte equality
// instead of a hope.
package markdown

import (
	"strings"

	"charm.land/glamour/v2"
	"charm.land/glamour/v2/ansi"
)

// Render is the one-shot path: the whole document through glamour, then
// normalized. Frozen chat items use this, and it is the oracle the
// stream cache is tested against.
func Render(source string, width int, cfg ansi.StyleConfig) string {
	r, err := glamour.NewTermRenderer(glamour.WithStyles(cfg), glamour.WithWordWrap(width))
	if err != nil {
		return source
	}
	return renderWith(r, source)
}

func renderWith(r *glamour.TermRenderer, source string) string {
	// Blank means blank to markdown: spaces, tabs, and newlines only.
	// TrimSpace would be wrong here; a lone \v is paragraph content to
	// goldmark and has to reach the renderer.
	if strings.Trim(source, " \t\n") == "" {
		return ""
	}
	out, err := r.Render(source)
	if err != nil {
		// A parse failure falls back to the raw text: wrong-looking
		// beats invisible.
		return strings.TrimRight(source, "\n")
	}
	return normalize(out)
}

// normalize strips the full-width padding glamour paints to the right
// of every line, collapses invisible style churn, and drops blank edge
// lines, so a fragment render ends exactly where its content does and
// carries no context-dependent no-op escapes. That is the property that
// lets the stream cache glue fragment renders with a plain blank line
// and still byte-match a whole-document render.
func normalize(out string) string {
	lines := strings.Split(out, "\n")
	// Interior blank runs collapse to one blank line. Glamour separates
	// blocks with a single blank, but degenerate constructs (an empty
	// heading, say) render to extra blanks that would otherwise differ
	// between a whole-document render, where they are interior, and a
	// fragment render, where they land on a trimmed edge.
	kept := lines[:0]
	for _, l := range lines {
		l = normalizeLine(l)
		if l == "" && len(kept) > 0 && kept[len(kept)-1] == "" {
			continue
		}
		kept = append(kept, l)
	}
	start, end := 0, len(kept)
	for start < end && kept[start] == "" {
		start++
	}
	for end > start && kept[end-1] == "" {
		end--
	}
	return strings.Join(kept[start:end], "\n")
}

// normalizeLine rewrites one line of glamour output into a canonical
// form: SGR sequences that never color a visible rune are dropped (a
// whole-document render leaks the previous block's style as a set
// immediately followed by a reset; a fragment render does not), and the
// trailing run of padding spaces is cut, with a reset closing whatever
// style is left open.
func normalizeLine(line string) string {
	var b strings.Builder
	b.Grow(len(line))
	var pending []string // styles set since the last visible rune
	seenText := false
	end := 0 // b length just past the last visible non-space rune

	flush := func() {
		for _, s := range pending {
			b.WriteString(s)
		}
		pending = pending[:0]
	}
	i := 0
	for i < len(line) {
		if line[i] == 0x1b && i+1 < len(line) && line[i+1] == '[' {
			j := i + 2
			for j < len(line) && line[j] != 'm' {
				j++
			}
			if j < len(line) {
				seq := line[i : j+1]
				params := line[i+2 : j]
				if params == "" || params == "0" {
					// A reset nullifies every unflushed set, and at
					// line start it is itself a no-op.
					pending = pending[:0]
					if seenText {
						pending = append(pending, seq)
					}
				} else {
					pending = append(pending, seq)
				}
				i = j + 1
				continue
			}
		}
		r := i + 1
		for r < len(line) && line[r]&0xC0 == 0x80 {
			r++
		}
		flush()
		b.WriteString(line[i:r])
		if line[i] != ' ' {
			seenText = true
			end = b.Len()
		}
		i = r
	}
	if end == 0 {
		return ""
	}
	kept := b.String()[:end]
	if strings.Contains(kept, "\x1b[") {
		return kept + "\x1b[m"
	}
	return kept
}
