// Package splash renders the ari wordmark and hosts first-run
// onboarding (doc 02 section 19). The wordmark is procedural: bitmap
// letterforms colored by a palette gradient over a faint deterministic
// background field, rendered once per theme and cached so resize never
// re-renders or jitters it.
package splash

import (
	"hash/fnv"
	"image/color"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"

	"github.com/tamnd/ari/ui/theme"
)

// Letterforms: '#' is an on cell. Lowercase ari, five rows.
var letters = [][]string{
	{ // a
		".##.",
		"...#",
		".###",
		"#..#",
		".###",
	},
	{ // r
		"....",
		"#.##",
		"##..",
		"#...",
		"#...",
	},
	{ // i
		"#",
		".",
		"#",
		"#",
		"#",
	},
}

const fieldPad = 3 // columns of background field on each side

var (
	markMu    sync.Mutex
	markCache = map[string][]string{}
)

// Wordmark returns the styled logo lines for a theme. The size is
// fixed; the caller centers it. Cached per theme, so a resize storm
// costs nothing.
func Wordmark(th theme.Theme) []string {
	markMu.Lock()
	defer markMu.Unlock()
	if lines, ok := markCache[th.Name]; ok {
		return lines
	}
	lines := renderMark(th)
	markCache[th.Name] = lines
	return lines
}

func renderMark(th theme.Theme) []string {
	// Compose the bitmap with one blank column between letters. Each
	// bitmap cell is two screen columns, so the mark reads chunky.
	rows := make([]string, len(letters[0]))
	for r := range rows {
		var b strings.Builder
		for li, l := range letters {
			if li > 0 {
				b.WriteByte('.')
			}
			b.WriteString(l[r])
		}
		rows[r] = b.String()
	}

	cells := len(rows[0])
	width := 2*cells + 2*fieldPad
	field := func(x, y int) string {
		if fieldAt(x, y) {
			return th.S.Faint.Render("·")
		}
		return " "
	}
	fieldRow := func(y int) string {
		var b strings.Builder
		for x := range width {
			b.WriteString(field(x, y))
		}
		return b.String()
	}

	// One field-only row above and below; dots stay out of the
	// letters' rows entirely so the forms stay crisp.
	out := []string{fieldRow(0)}
	for y, row := range rows {
		var b strings.Builder
		for x := range fieldPad {
			b.WriteString(field(x, y+1))
		}
		for cx := range cells {
			if row[cx] == '#' {
				c := gradient(th.P.Primary, th.P.Accent, cx, cells)
				block := lipgloss.NewStyle().Foreground(c).Render("██")
				b.WriteString(block)
			} else {
				b.WriteString("  ")
			}
		}
		for x := range fieldPad {
			b.WriteString(field(width-fieldPad+x, y+1))
		}
		out = append(out, b.String())
	}
	return append(out, fieldRow(len(rows)+1))
}

// gradient lerps between two palette colors across the width.
func gradient(from, to color.Color, x, width int) color.Color {
	fr, fg, fb, _ := from.RGBA()
	tr, tg, tb, _ := to.RGBA()
	lerp := func(a, b uint32) uint8 {
		return uint8((int(a>>8)*(width-1-x) + int(b>>8)*x) / (width - 1))
	}
	return color.RGBA{R: lerp(fr, tr), G: lerp(fg, tg), B: lerp(fb, tb), A: 0xff}
}

// fieldAt sprinkles the faint background dots deterministically, so the
// field is stable across renders and machines (golden-testable).
func fieldAt(x, y int) bool {
	h := fnv.New32a()
	h.Write([]byte{byte(x), byte(y), 0x61}) // 'a' salts the pattern
	return h.Sum32()%7 == 0
}

var (
	compactMu    sync.Mutex
	compactCache = map[string]string{}
)

// Compact is the one-line sidebar variant: the gradient over the plain
// letters, no field.
func Compact(th theme.Theme) string {
	compactMu.Lock()
	defer compactMu.Unlock()
	if s, ok := compactCache[th.Name]; ok {
		return s
	}
	const word = "ari"
	var b strings.Builder
	for i, r := range word {
		c := gradient(th.P.Primary, th.P.Accent, i, len(word))
		b.WriteString(lipgloss.NewStyle().Foreground(c).Bold(true).Render(string(r)))
	}
	b.WriteString(th.S.Faint.Render(" アリ"))
	s := b.String()
	compactCache[th.Name] = s
	return s
}
