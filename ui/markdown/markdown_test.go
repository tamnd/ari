package markdown

import (
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

func cfg() (c struct {
	dark theme.Theme
}) {
	c.dark = theme.Themes()["dark"]
	return
}

// corpus is the adversarial document set: every construct the boundary
// detector must refuse to seal early, plus the plain prose it must seal.
var corpus = []struct{ name, doc string }{
	{"plain paragraphs", "First paragraph of ordinary prose that goes on for a while.\n\nSecond paragraph.\n\nThird paragraph closes it out."},
	{"heading and prose", "# Title\n\nSome prose under the title.\n\n## Section\n\nMore prose, and then a final line."},
	{"code fence", "Intro paragraph.\n\n```go\nx := 1\ny := 2\n```\n\nAfter the fence."},
	{"fence with blank lines inside", "Before.\n\n```\nline one\n\nline two after a blank\n```\n\nAfter."},
	{"unterminated fence", "Before.\n\n```\nstill open\n\nnever closed"},
	{"tilde fence", "Para.\n\n~~~python\nprint(1)\n~~~\n\nDone."},
	{"loose list", "- alpha\n\n- beta\n\n- gamma\n\ntail paragraph"},
	{"tight list then prose", "- one\n- two\n- three\n\nA paragraph after the list.\n\nAnother one."},
	{"ordered list", "1. first\n2. second\n\n10. tenth restarts\n\ntail"},
	{"table", "Header text.\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\nAfter the table."},
	{"blockquote", "Intro.\n\n> quoted line one\n> quoted line two\n\nAfter the quote."},
	{"setext candidate", "This could be a heading\n\n===\n\nBut it never was."},
	{"real setext", "Heading text\n===\n\nBody paragraph.\n\nMore body."},
	{"reference link", "See [the docs][ref] for more.\n\nA middle paragraph.\n\n[ref]: https://example.com\n\ntail"},
	{"shortcut reference", "As [others] have said.\n\n[others]: https://example.com/o\n\ntail"},
	{"inline links are safe", "See [the docs](https://example.com) for more.\n\nSecond paragraph with [another](https://x.dev).\n\nThird."},
	{"footnote", "A claim[^1] made boldly.\n\n[^1]: the fine print\n\ntail"},
	{"html comment", "Before.\n\n<!-- a comment\nspanning lines -->\n\nAfter."},
	{"indented code", "Before.\n\n    indented code line\n\n    more indented code\n\nAfter."},
	{"thematic break", "Above the line.\n\n---\n\nBelow the line."},
	{"emphasis and code spans", "Some *emphasis* and `inline code` here.\n\nAnd **bold** with `more [not a link] code`.\n\nEnd."},
	{"deep mix", "# Report\n\nFindings below.\n\n```sh\ngrep -r TODO .\n```\n\n- item one\n- item two\n\n> a quote\n\nFinal paragraph."},
}

// TestStreamedEqualsFull is the stream cache oracle (doc 02 section
// 17.2): for every document, fed in small chunks, the incremental
// render at every prefix must byte-match a from-scratch full render of
// that prefix. A boundary detector bug that seals an open construct
// fails here, not on screen.
func TestStreamedEqualsFull(t *testing.T) {
	th := cfg().dark
	const width = 60
	for _, tc := range corpus {
		t.Run(tc.name, func(t *testing.T) {
			c := NewStreamCache(width, th.S.Markdown)
			// Chunk sizes cycle through awkward lengths so boundaries
			// land mid-construct, mid-line, and mid-rune.
			sizes := []int{1, 3, 7, 2, 11, 5}
			for i, pos := 0, 0; pos < len(tc.doc); i++ {
				pos = min(pos+sizes[i%len(sizes)], len(tc.doc))
				prefix := tc.doc[:pos]
				got := c.Render(prefix)
				want := Render(prefix, width, th.S.Markdown)
				if got != want {
					t.Fatalf("prefix %d bytes: streamed render diverged\nstreamed: %q\nfull:     %q",
						pos, got, want)
				}
			}
		})
	}
}

// TestBoundaryAdvances proves the cache is an optimization, not a
// no-op: on plain prose the boundary moves past settled paragraphs.
func TestBoundaryAdvances(t *testing.T) {
	th := cfg().dark
	c := NewStreamCache(60, th.S.Markdown)
	doc := "First paragraph.\n\nSecond paragraph.\n\nThird still streaming"
	c.Render(doc)
	if c.boundary == 0 {
		t.Fatal("boundary never advanced on plain paragraphs; the cache is doing nothing")
	}
	if got, want := doc[:c.boundary], "First paragraph.\n\nSecond paragraph.\n\n"; got != want {
		t.Errorf("boundary sealed %q, want %q", got, want)
	}
}

// TestBoundaryRefusesOpenConstructs: every ambiguous case resolves to
// not-safe (doc 02 section 6.2).
func TestBoundaryRefusesOpenConstructs(t *testing.T) {
	cases := []struct {
		name, doc string
		want      int // the furthest legal boundary; 0 for "never"
	}{
		{"open fence", "para\n\n```\ncode\n\nstill code", 6},
		{"loose list", "- a\n\n- b\n\n", 0},
		{"reference use", "see [docs][ref]\n\nmore\n\n", 0},
		{"shortcut use", "see [docs]\n\nmore\n\n", 0},
		{"definition", "[ref]: https://x\n\nmore\n\n", 0},
		{"open html comment", "<!-- open\n\nstill inside\n\n", 0},
		{"indented code", "    code\n\n", 0},
		{"blockquote last", "> quote\n\n", 0},
		{"table last", "| a |\n|---|\n\n", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if b := findSafeBoundary(tc.doc, 0); b > tc.want {
				t.Errorf("boundary advanced to %d past an open construct in %q (max safe %d)",
					b, tc.doc, tc.want)
			}
		})
	}
}

// TestNonAppendResets: an edit before the boundary (resume, not
// streaming) throws the cache away instead of gluing stale bytes.
func TestNonAppendResets(t *testing.T) {
	th := cfg().dark
	c := NewStreamCache(60, th.S.Markdown)
	c.Render("First paragraph.\n\nSecond paragraph.\n\ntail")
	if c.boundary == 0 {
		t.Fatal("setup: boundary should have advanced")
	}
	edited := "Rewritten opening.\n\ntail"
	if got, want := c.Render(edited), Render(edited, 60, th.S.Markdown); got != want {
		t.Errorf("render after non-append edit diverged\ngot:  %q\nwant: %q", got, want)
	}
}

// TestSetWidthResets: a width change moves every wrap boundary, so the
// cache falls back to a full render at the new width.
func TestSetWidthResets(t *testing.T) {
	th := cfg().dark
	doc := "A paragraph long enough to wrap differently at different widths, with several words.\n\nSecond paragraph here.\n\ntail"
	c := NewStreamCache(60, th.S.Markdown)
	c.Render(doc)
	c.SetWidth(40)
	if got, want := c.Render(doc), Render(doc, 40, th.S.Markdown); got != want {
		t.Errorf("render after width change diverged\ngot:  %q\nwant: %q", got, want)
	}
}

// FuzzStreamedEqualsFull extends the oracle to adversarial partial
// inputs: any byte soup, chunked any way, must keep the equality.
func FuzzStreamedEqualsFull(f *testing.F) {
	for _, tc := range corpus {
		f.Add(tc.doc, uint8(3))
	}
	f.Add("[a]\n\n[a]: x\n\n", uint8(1))
	f.Add("```\n\n```\n\n- x\n\n1) y\n\n", uint8(2))
	th := theme.Themes()["dark"]
	const width = 48
	f.Fuzz(func(t *testing.T, doc string, step uint8) {
		if len(doc) > 4096 {
			t.Skip()
		}
		n := max(int(step%16), 1)
		c := NewStreamCache(width, th.S.Markdown)
		for pos := 0; pos < len(doc); {
			pos = min(pos+n, len(doc))
			prefix := doc[:pos]
			if got, want := c.Render(prefix), Render(prefix, width, th.S.Markdown); got != want {
				t.Fatalf("prefix %d: streamed %q != full %q", pos, got, want)
			}
		}
	})
}

// TestRenderGolden pins the one-shot render of the mixed document so a
// glamour or normalizer change is a reviewed diff, not a surprise.
func TestRenderGolden(t *testing.T) {
	th := cfg().dark
	doc := corpus[len(corpus)-1].doc
	out := Render(doc, 60, th.S.Markdown)
	eval.Golden(t, "render_deep_mix", strings.ReplaceAll(out, "\x1b", "^["))
}
