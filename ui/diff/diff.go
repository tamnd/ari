// Package diff renders unified diff text into styled lines, shared by
// the edit-result chat renderer and the permission prompt (doc 02
// section 9). The input is the diff the core already computed and
// shipped in the event's consequence; this package never re-derives a
// change from raw tool input (D2, D15). It layers chroma syntax colors
// under the diff backgrounds, marks the intra-line spans that actually
// changed, and offers unified and side-by-side layouts.
package diff

import (
	"fmt"
	"hash/fnv"
	"strconv"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/ui/theme"
)

// Mode selects the layout.
type Mode int

const (
	// Auto renders side-by-side past the width threshold, unified below.
	Auto Mode = iota
	Unified
	Split
)

// splitThreshold is the width past which Auto goes side-by-side.
const splitThreshold = 140

// Render turns unified diff text into styled lines at the given width.
// Same input, width, theme, and mode give the same lines; results are
// cached because the same edit renders twice, once in the permission
// prompt and once in the chat result.
func Render(diffText string, width int, th theme.Theme, mode Mode) []string {
	key := cacheKey(diffText, width, th.Name, mode)
	cacheMu.Lock()
	if lines, ok := cache[key]; ok {
		cacheMu.Unlock()
		return lines
	}
	cacheMu.Unlock()

	lines := render(diffText, width, th, mode)

	cacheMu.Lock()
	if len(cache) > cacheCap {
		cache = map[uint64][]string{} // simple bound; a diff render is cheap to redo
	}
	cache[key] = lines
	cacheMu.Unlock()
	return lines
}

const cacheCap = 256

var (
	cacheMu sync.Mutex
	cache   = map[uint64][]string{}
)

func cacheKey(text string, width int, themeName string, mode Mode) uint64 {
	h := fnv.New64a()
	h.Write([]byte(text))
	h.Write([]byte{0})
	h.Write([]byte(themeName))
	h.Write([]byte{0})
	h.Write([]byte(strconv.Itoa(width)))
	h.Write([]byte{byte(mode)})
	return h.Sum64()
}

func render(diffText string, width int, th theme.Theme, mode Mode) []string {
	if width < 8 {
		width = 8
	}
	doc := parse(diffText)
	split := mode == Split || (mode == Auto && width >= splitThreshold)
	if split {
		return renderSplit(doc, width, th)
	}
	return renderUnified(doc, width, th)
}

// lineKind classifies one diff line.
type lineKind int

const (
	kindContext lineKind = iota
	kindDel
	kindAdd
	kindHunk
	kindMeta // ---/+++ and anything else outside hunks
)

// line is one parsed diff line, with the text stripped of its marker,
// its old and new line numbers (zero when absent on that side), and the
// intra-line span that changed, when a del/add pair could be matched.
type line struct {
	kind       lineKind
	text       string
	oldN, newN int
	emphFrom   int // byte offsets into text; emphFrom == emphTo means none
	emphTo     int
}

type document struct {
	path  string // from the +++ header, for lexer selection
	lines []line
}

// parse walks unified diff text, tracking line numbers from hunk
// headers and pairing deletion runs with the additions that follow them
// for intra-line emphasis.
func parse(diffText string) document {
	var doc document
	oldN, newN := 0, 0
	inHunk := false
	for raw := range strings.SplitSeq(strings.TrimRight(diffText, "\n"), "\n") {
		switch {
		case strings.HasPrefix(raw, "@@"):
			inHunk = true
			oldN, newN = hunkStarts(raw)
			doc.lines = append(doc.lines, line{kind: kindHunk, text: raw})
		case strings.HasPrefix(raw, "+++"):
			doc.path = strings.TrimPrefix(strings.TrimPrefix(raw, "+++ "), "b/")
			doc.lines = append(doc.lines, line{kind: kindMeta, text: raw})
		case !inHunk:
			doc.lines = append(doc.lines, line{kind: kindMeta, text: raw})
		case strings.HasPrefix(raw, "-"):
			doc.lines = append(doc.lines, line{kind: kindDel, text: raw[1:], oldN: oldN})
			oldN++
		case strings.HasPrefix(raw, "+"):
			doc.lines = append(doc.lines, line{kind: kindAdd, text: raw[1:], newN: newN})
			newN++
		default:
			text := raw
			if strings.HasPrefix(raw, " ") {
				text = raw[1:]
			}
			doc.lines = append(doc.lines, line{kind: kindContext, text: text, oldN: oldN, newN: newN})
			oldN++
			newN++
		}
	}
	emphasize(doc.lines)
	return doc
}

func hunkStarts(header string) (oldN, newN int) {
	// @@ -12,3 +14,4 @@ optional context
	oldN, newN = 1, 1
	for f := range strings.FieldsSeq(header) {
		switch {
		case strings.HasPrefix(f, "-"):
			oldN = atoiPrefix(f[1:])
		case strings.HasPrefix(f, "+"):
			newN = atoiPrefix(f[1:])
		}
	}
	return oldN, newN
}

func atoiPrefix(s string) int {
	if i := strings.IndexByte(s, ','); i >= 0 {
		s = s[:i]
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 {
		return 1
	}
	return n
}

// emphasize pairs each run of deletions with the additions that follow
// it and marks, per pair, the span between the common prefix and the
// common suffix, so a one-word edit reads as a one-word edit instead of
// a whole-line rewrite.
func emphasize(lines []line) {
	i := 0
	for i < len(lines) {
		if lines[i].kind != kindDel {
			i++
			continue
		}
		delFrom := i
		for i < len(lines) && lines[i].kind == kindDel {
			i++
		}
		addFrom := i
		for i < len(lines) && lines[i].kind == kindAdd {
			i++
		}
		pairs := min(addFrom-delFrom, i-addFrom)
		for p := range pairs {
			d, a := &lines[delFrom+p], &lines[addFrom+p]
			d.emphFrom, d.emphTo, a.emphFrom, a.emphTo = changedSpans(d.text, a.text)
		}
	}
}

// changedSpans returns the byte spans of old and new that remain after
// trimming the common prefix and common suffix.
func changedSpans(old, new string) (of, ot, nf, nt int) {
	p := 0
	for p < len(old) && p < len(new) && old[p] == new[p] {
		p++
	}
	s := 0
	for s < len(old)-p && s < len(new)-p && old[len(old)-1-s] == new[len(new)-1-s] {
		s++
	}
	return p, len(old) - s, p, len(new) - s
}

// renderUnified stacks the diff with a two-column line-number gutter.
func renderUnified(doc document, width int, th theme.Theme) []string {
	st := th.S.Diff
	lex := lexerFor(doc.path)
	var out []string
	for _, l := range doc.lines {
		switch l.kind {
		case kindHunk:
			out = append(out, ansi.Truncate(st.Header.Render(l.text), width, "…"))
		case kindMeta:
			out = append(out, ansi.Truncate(st.LineNo.Render(l.text), width, "…"))
		case kindDel:
			g := st.GutterDel.Render(fmt.Sprintf("%4d      - ", l.oldN))
			out = append(out, compose(g, l, lex, st.Del, st.DelEmph, th, width))
		case kindAdd:
			g := st.GutterAdd.Render(fmt.Sprintf("     %4d + ", l.newN))
			out = append(out, compose(g, l, lex, st.Add, st.AddEmph, th, width))
		default:
			g := st.LineNo.Render(fmt.Sprintf("%4d %4d   ", l.oldN, l.newN))
			out = append(out, compose(g, l, lex, st.Context, st.Context, th, width))
		}
	}
	return out
}

// renderSplit puts old on the left and new on the right. Paired del and
// add lines share a row; unpaired ones face a blank half.
func renderSplit(doc document, width int, th theme.Theme) []string {
	st := th.S.Diff
	lex := lexerFor(doc.path)
	half := (width - 1) / 2
	blank := strings.Repeat(" ", half)
	div := th.S.Border.Render("│")

	side := func(l *line, gutter lipgloss.Style, base, emph lipgloss.Style, n int) string {
		if l == nil {
			return blank
		}
		g := gutter.Render(fmt.Sprintf("%4d ", n))
		s := compose(g, *l, lex, base, emph, th, half)
		return s + base.Render(strings.Repeat(" ", max(half-ansi.StringWidth(s), 0)))
	}

	var out []string
	lines := doc.lines
	i := 0
	for i < len(lines) {
		l := lines[i]
		switch l.kind {
		case kindHunk:
			out = append(out, ansi.Truncate(st.Header.Render(l.text), width, "…"))
			i++
		case kindMeta:
			out = append(out, ansi.Truncate(st.LineNo.Render(l.text), width, "…"))
			i++
		case kindContext:
			row := side(&l, st.LineNo, st.Context, st.Context, l.oldN) + div +
				side(&l, st.LineNo, st.Context, st.Context, l.newN)
			out = append(out, row)
			i++
		default:
			// A change block: dels then adds, paired row by row.
			delFrom := i
			for i < len(lines) && lines[i].kind == kindDel {
				i++
			}
			addFrom := i
			for i < len(lines) && lines[i].kind == kindAdd {
				i++
			}
			dels, adds := lines[delFrom:addFrom], lines[addFrom:i]
			for r := range max(len(dels), len(adds)) {
				var left, right string
				if r < len(dels) {
					left = side(&dels[r], st.GutterDel, st.Del, st.DelEmph, dels[r].oldN)
				} else {
					left = blank
				}
				if r < len(adds) {
					right = side(&adds[r], st.GutterAdd, st.Add, st.AddEmph, adds[r].newN)
				} else {
					right = blank
				}
				out = append(out, left+div+right)
			}
		}
	}
	return out
}

// compose builds one styled diff line: gutter, then the code tokenized
// by chroma for foreground color, painted over the line's diff
// background, with the changed span on the emphasis background.
func compose(gutter string, l line, lex chroma.Lexer, base, emph lipgloss.Style, th theme.Theme, width int) string {
	var b strings.Builder
	b.WriteString(gutter)
	for _, seg := range segments(l, lex, th) {
		style := base
		if seg.emph {
			style = emph
		}
		if seg.fg != "" {
			style = style.Foreground(lipgloss.Color(seg.fg))
		}
		b.WriteString(style.Render(seg.text))
	}
	return ansi.Truncate(b.String(), width, "…")
}

// segment is a run of text with one foreground color and one emphasis
// state.
type segment struct {
	text string
	fg   string
	emph bool
}

// segments tokenizes the line and splits tokens at the emphasis span
// boundaries, so styling stays a flat sequence of runs.
func segments(l line, lex chroma.Lexer, th theme.Theme) []segment {
	var segs []segment
	pos := 0
	for _, tok := range tokenize(l.text, lex) {
		fg := tokenColor(tok.Type, th)
		start, end := pos, pos+len(tok.Value)
		for _, cut := range cuts(start, end, l.emphFrom, l.emphTo) {
			segs = append(segs, segment{
				text: l.text[cut[0]:cut[1]],
				fg:   fg,
				emph: cut[0] >= l.emphFrom && cut[1] <= l.emphTo && l.emphFrom < l.emphTo,
			})
		}
		pos = end
	}
	return segs
}

// cuts splits [start,end) at the emphasis boundaries that fall inside it.
func cuts(start, end, ef, et int) [][2]int {
	points := []int{start}
	for _, p := range []int{ef, et} {
		if p > start && p < end {
			points = append(points, p)
		}
	}
	points = append(points, end)
	var out [][2]int
	for i := 0; i+1 < len(points); i++ {
		if points[i] < points[i+1] {
			out = append(out, [2]int{points[i], points[i+1]})
		}
	}
	return out
}

func lexerFor(path string) chroma.Lexer {
	lex := lexers.Match(path)
	if lex == nil {
		lex = lexers.Fallback
	}
	return chroma.Coalesce(lex)
}

// tokenize runs chroma over one line. Diff previews tokenize per line,
// so a construct spanning lines (a raw string, say) may color oddly at
// its edges; that is the standard trade every diff pager makes.
func tokenize(text string, lex chroma.Lexer) []chroma.Token {
	it, err := lex.Tokenise(nil, text)
	if err != nil {
		return []chroma.Token{{Type: chroma.Text, Value: text}}
	}
	var toks []chroma.Token
	for tok := it(); tok != chroma.EOF; tok = it() {
		// chroma appends the newline it assumes; the line has none.
		tok.Value = strings.TrimRight(tok.Value, "\n")
		if tok.Value != "" {
			toks = append(toks, tok)
		}
	}
	return toks
}

// tokenColor resolves a token type to a hex color through the theme's
// chroma style, walking up the token hierarchy the way chroma does.
func tokenColor(t chroma.TokenType, th theme.Theme) string {
	if th.S.Chroma == nil {
		return ""
	}
	entry := th.S.Chroma.Get(t)
	if !entry.Colour.IsSet() {
		return ""
	}
	return entry.Colour.String()
}
