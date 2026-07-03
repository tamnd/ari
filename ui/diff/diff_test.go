package diff

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

// sample is the diff every golden renders: a Go file with a one-word
// change (exercises intra-line emphasis), a pure insertion, and a pure
// deletion, plus context.
const sample = `--- a/pkg/greet/greet.go
+++ b/pkg/greet/greet.go
@@ -1,7 +1,8 @@
 package greet

 import "fmt"

-func Hello(name string) {
-	fmt.Println("hello", name)
+func Hello(name string, loud bool) {
+	fmt.Println("HELLO", name)
+	fmt.Println("welcome")
 }
@@ -12,4 +13,3 @@
 func Bye() {
 	fmt.Println("bye")
-	fmt.Println("so long")
 }
`

// TestRenderGolden pins the full styled output in both layouts and both
// themes: line numbers, gutters, diff backgrounds, chroma foregrounds,
// and the intra-line emphasis spans all live in these files.
func TestRenderGolden(t *testing.T) {
	for _, th := range []theme.Theme{theme.Dark(), theme.Light()} {
		for _, tc := range []struct {
			name  string
			width int
			mode  Mode
		}{
			{"unified_80", 80, Unified},
			{"split_160", 160, Split},
			{"auto_160", 160, Auto}, // must match split_160
			{"auto_100", 100, Auto}, // must match unified at 100
		} {
			lines := Render(sample, tc.width, th, tc.mode)
			got := strings.ReplaceAll(strings.Join(lines, "\n"), "\x1b", "^[")
			eval.Golden(t, tc.name+"_"+th.Name, got+"\n")
		}
	}
}

// TestAutoThreshold: Auto picks split at and above the threshold,
// unified below it.
func TestAutoThreshold(t *testing.T) {
	th := theme.Dark()
	wide := Render(sample, splitThreshold, th, Auto)
	if split := Render(sample, splitThreshold, th, Split); !equal(wide, split) {
		t.Errorf("Auto at width %d should render split", splitThreshold)
	}
	narrow := Render(sample, splitThreshold-1, th, Auto)
	if uni := Render(sample, splitThreshold-1, th, Unified); !equal(narrow, uni) {
		t.Errorf("Auto at width %d should render unified", splitThreshold-1)
	}
}

// TestWidths: no rendered line overflows the requested width, down to
// widths far too small to be useful.
func TestWidths(t *testing.T) {
	th := theme.Dark()
	for _, mode := range []Mode{Unified, Split} {
		for w := 8; w <= 200; w += 7 {
			for i, line := range Render(sample, w, th, mode) {
				if got := ansi.StringWidth(line); got > w {
					t.Fatalf("mode %d width %d: line %d is %d cells: %q", mode, w, i, got, line)
				}
			}
		}
	}
}

// TestEmphasisSpans: the changed-span pairing marks only the words that
// differ, not the shared prefix and suffix.
func TestEmphasisSpans(t *testing.T) {
	of, ot, nf, nt := changedSpans(
		`func Hello(name string) {`,
		`func Hello(name string, loud bool) {`,
	)
	if old := `func Hello(name string) {`[of:ot]; old != "" && !strings.Contains(") {", old) {
		t.Errorf("old emphasis span = %q, want inside the changed tail", old)
	}
	if new := `func Hello(name string, loud bool) {`[nf:nt]; !strings.Contains(new, "loud bool") {
		t.Errorf("new emphasis span = %q, want it to cover the inserted words", new)
	}
	// Identical lines carry no span.
	of, ot, _, _ = changedSpans("same", "same")
	if of != ot {
		t.Errorf("identical lines got emphasis span [%d,%d)", of, ot)
	}
}

// TestLineNumbers: numbers restart per hunk header and count only the
// side a line exists on.
func TestLineNumbers(t *testing.T) {
	doc := parse(sample)
	var dels, adds []int
	for _, l := range doc.lines {
		switch l.kind {
		case kindDel:
			dels = append(dels, l.oldN)
		case kindAdd:
			adds = append(adds, l.newN)
		}
	}
	wantDels := []int{5, 6, 14}
	wantAdds := []int{5, 6, 7}
	if !equalInts(dels, wantDels) {
		t.Errorf("del line numbers = %v, want %v", dels, wantDels)
	}
	if !equalInts(adds, wantAdds) {
		t.Errorf("add line numbers = %v, want %v", adds, wantAdds)
	}
}

// TestCache: the second render of the same input returns the identical
// slice, and a different width misses.
func TestCache(t *testing.T) {
	th := theme.Dark()
	a := Render(sample, 91, th, Unified)
	b := Render(sample, 91, th, Unified)
	if len(a) == 0 || &a[0] != &b[0] {
		t.Error("second identical render did not hit the cache")
	}
	c := Render(sample, 92, th, Unified)
	if len(a) == len(c) && &a[0] == &c[0] {
		t.Error("different width returned the cached slice")
	}
}

// TestDegenerateInput: garbage in, no panic and no overflow out.
func TestDegenerateInput(t *testing.T) {
	th := theme.Dark()
	for _, in := range []string{
		"",
		"not a diff at all",
		"@@ garbage @@",
		"@@ -0,0 +0,0 @@\n+only add",
		"--- a/x\n+++ b/x",
		strings.Repeat("+", 500),
	} {
		for _, mode := range []Mode{Unified, Split} {
			for _, line := range Render(in, 40, th, mode) {
				if ansi.StringWidth(line) > 40 {
					t.Errorf("input %q overflowed: %q", in, line)
				}
			}
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
