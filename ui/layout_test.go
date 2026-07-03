package ui

import (
	"fmt"
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

func sketch(l Layout) string {
	r := func(name string, r uv.Rectangle) string {
		return fmt.Sprintf("%-8s x=%d y=%d w=%d h=%d\n", name, r.Min.X, r.Min.Y, r.Dx(), r.Dy())
	}
	var b strings.Builder
	fmt.Fprintf(&b, "compact=%v\n", l.Compact)
	b.WriteString(r("header", l.Header))
	b.WriteString(r("sidebar", l.Sidebar))
	b.WriteString(r("main", l.Main))
	b.WriteString(r("pills", l.Pills))
	b.WriteString(r("editor", l.Editor))
	b.WriteString(r("status", l.Status))
	return b.String()
}

// TestLayoutGolden pins the regions at sizes on both sides of the
// compact breakpoint, plus the forced-compact override.
func TestLayoutGolden(t *testing.T) {
	var b strings.Builder
	for _, c := range []struct {
		name    string
		w, h    int
		lines   int
		compact bool
	}{
		{"wide", 160, 48, 1, false},
		{"just_above", 120, 30, 1, false},
		{"narrow", 119, 48, 1, false},
		{"short", 160, 29, 1, false},
		{"forced", 160, 48, 1, true},
		{"grown_editor", 160, 48, 8, false},
		{"tiny", 40, 10, 1, false},
	} {
		b.WriteString("== " + c.name + " ==\n" + sketch(ComputeLayout(c.w, c.h, c.lines, c.compact)))
	}
	eval.Golden(t, "regions", b.String())
}

// TestLayoutComparable: the draw path gates resize work on ==, so the
// same inputs must produce an equal struct.
func TestLayoutComparable(t *testing.T) {
	a := ComputeLayout(160, 48, 4, false)
	b := ComputeLayout(160, 48, 4, false)
	if a != b {
		t.Fatalf("same inputs produced unequal layouts:\n%v\n%v", a, b)
	}
	if c := ComputeLayout(161, 48, 4, false); a == c {
		t.Fatal("different widths produced equal layouts")
	}
}

// TestEditorGrowth: the editor tracks content between the floor and cap.
func TestEditorGrowth(t *testing.T) {
	for lines, want := range map[int]int{0: 3, 1: 3, 3: 3, 7: 7, 15: 15, 40: 15} {
		if got := ComputeLayout(160, 48, lines, false).Editor.Dy(); got != want {
			t.Errorf("editor height for %d lines = %d, want %d", lines, got, want)
		}
	}
}

// TestRegionsTile: regions cover the area without overlap on both sides
// of the breakpoint, so no widget fights another for cells.
func TestRegionsTile(t *testing.T) {
	for _, c := range []struct{ w, h int }{{160, 48}, {80, 20}, {40, 8}, {121, 31}} {
		l := ComputeLayout(c.w, c.h, 5, false)
		owned := make([]int, c.w*c.h)
		claim := func(r uv.Rectangle) {
			for y := r.Min.Y; y < r.Max.Y; y++ {
				for x := r.Min.X; x < r.Max.X; x++ {
					owned[y*c.w+x]++
				}
			}
		}
		for _, r := range []uv.Rectangle{l.Header, l.Sidebar, l.Main, l.Pills, l.Editor, l.Status} {
			claim(r)
		}
		for i, n := range owned {
			if n != 1 {
				t.Fatalf("%dx%d: cell (%d,%d) owned by %d regions", c.w, c.h, i%c.w, i/c.w, n)
			}
		}
	}
}
