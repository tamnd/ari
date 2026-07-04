package memory

import (
	"fmt"
	"strings"
	"testing"
)

// TestRenderIndexDeterministic is the golden case: a fixed set of pins renders
// to exact, stable bytes, the property the cache_control breakpoint rests on.
func TestRenderIndexDeterministic(t *testing.T) {
	rows := []Row{
		{Label: "run make gen after editing schema", Anchors: []string{"file:schema.go"}},
		{Label: "the http transport is shared, do not build a new one", Anchors: []string{"file:transport.go", "symbol:Client"}},
		{Label: "a note with no anchor"},
	}
	want := "- run make gen after editing schema (file:schema.go)\n" +
		"- the http transport is shared, do not build a new one (file:transport.go, symbol:Client)\n" +
		"- a note with no anchor"
	got := RenderIndex(rows, DefaultIndexCap)
	if got != want {
		t.Fatalf("index =\n%q\nwant\n%q", got, want)
	}
	// Same rows in, same bytes out, twice.
	if again := RenderIndex(rows, DefaultIndexCap); again != got {
		t.Fatal("RenderIndex is not deterministic")
	}
}

// TestRenderIndexEmpty: no pins renders the empty string, the assembler owns the
// "no pins yet" wording.
func TestRenderIndexEmpty(t *testing.T) {
	if got := RenderIndex(nil, DefaultIndexCap); got != "" {
		t.Fatalf("empty index = %q, want \"\"", got)
	}
}

// TestRenderIndexTruncatesLongLine: a line past the per-line cap is cut on a
// rune boundary and marked with an ellipsis rather than overflowing.
func TestRenderIndexTruncatesLongLine(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := RenderIndex([]Row{{Label: long}}, IndexCap{Lines: 100, PerLine: 40})
	if len(got) > 40 {
		t.Fatalf("line length = %d, want <= 40", len(got))
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated line = %q, want an ellipsis suffix", got)
	}
}

// TestRenderIndexTruncateRuneSafe: cutting a multibyte line never splits a rune.
func TestRenderIndexTruncateRuneSafe(t *testing.T) {
	got := RenderIndex([]Row{{Label: strings.Repeat("é", 100)}}, IndexCap{Lines: 100, PerLine: 20})
	if !utf8ValidString(got) {
		t.Fatalf("truncation split a rune: %q", got)
	}
}

// TestRenderIndexLineCap: more pins than the line cap collapse the tail into one
// visible overflow marker, never a silent drop.
func TestRenderIndexLineCap(t *testing.T) {
	var rows []Row
	for i := range 10 {
		rows = append(rows, Row{Label: fmt.Sprintf("pin %d", i)})
	}
	got := RenderIndex(rows, IndexCap{Lines: 4, PerLine: 160})
	lines := strings.Split(got, "\n")
	if len(lines) != 4 {
		t.Fatalf("lines = %d, want 4 (cap)", len(lines))
	}
	if !strings.Contains(lines[3], "more pinned") {
		t.Fatalf("last line = %q, want an overflow marker", lines[3])
	}
}

func utf8ValidString(s string) bool {
	for _, r := range s {
		if r == 0xFFFD {
			return false
		}
	}
	return true
}
