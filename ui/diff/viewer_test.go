package diff

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/theme"
)

// TestViewerHunkNav walks the focus across the sample's two hunks and
// back, asserting the counter tracks and the focus clamps at both ends
// instead of running off the list.
func TestViewerHunkNav(t *testing.T) {
	v := NewViewer(sample, theme.Dark())
	v.SetSize(80, 12)
	if got := v.Hunks(); got != 2 {
		t.Fatalf("Hunks() = %d, want 2", got)
	}
	if got := v.Focus(); got != 0 {
		t.Errorf("initial focus = %d, want 0", got)
	}
	v.PrevHunk() // clamps at the top
	if got := v.Focus(); got != 0 {
		t.Errorf("focus after PrevHunk at top = %d, want 0", got)
	}
	v.NextHunk()
	if got := v.Focus(); got != 1 {
		t.Errorf("focus after NextHunk = %d, want 1", got)
	}
	v.NextHunk() // clamps at the bottom
	if got := v.Focus(); got != 1 {
		t.Errorf("focus after NextHunk at bottom = %d, want 1", got)
	}
}

// TestViewerFocusScrolls: focusing the second hunk brings its header into
// the window; the first hunk's header scrolls off the top.
func TestViewerFocusScrolls(t *testing.T) {
	v := NewViewer(sample, theme.Dark())
	v.SetSize(80, 6)

	first := strings.Join(v.View(), "\n")
	if !strings.Contains(first, "@@ -1,7 +1,8 @@") {
		t.Errorf("focused-hunk-0 view missing the first hunk header:\n%s", first)
	}

	v.NextHunk()
	second := strings.Join(v.View(), "\n")
	if !strings.Contains(second, "@@ -12,4 +13,3 @@") {
		t.Errorf("focused-hunk-1 view missing the second hunk header:\n%s", second)
	}
	if strings.Contains(second, "@@ -1,7 +1,8 @@") {
		t.Errorf("focused-hunk-1 view still shows the first hunk header:\n%s", second)
	}
}

// TestViewerToggle: from Auto at a narrow width the first toggle commits
// to split, and toggling again returns to unified, so the layout flips
// live on every press.
func TestViewerToggle(t *testing.T) {
	v := NewViewer(sample, theme.Dark())
	v.SetSize(100, 20) // below splitThreshold: Auto would pick unified
	if got := v.Mode(); got != Auto {
		t.Fatalf("initial mode = %v, want Auto", got)
	}
	v.Toggle()
	if got := v.Mode(); got != Split {
		t.Errorf("first toggle mode = %v, want Split", got)
	}
	v.Toggle()
	if got := v.Mode(); got != Unified {
		t.Errorf("second toggle mode = %v, want Unified", got)
	}
}

// TestViewerScrollNoOverflow: horizontal scrolling never produces a line
// wider than the viewport, and scrolling left clamps at zero.
func TestViewerScrollNoOverflow(t *testing.T) {
	v := NewViewer(sample, theme.Dark())
	v.SetSize(24, 20)
	v.ScrollLeft() // clamps at 0, no panic
	for range 6 {
		v.ScrollRight()
		for i, line := range v.View() {
			if w := ansi.StringWidth(line); w > 24 {
				t.Fatalf("scrolled line %d is %d cells wide, want <= 24: %q", i, w, line)
			}
		}
	}
}

// TestViewerGolden pins the whole frame in both themes: the counter line,
// the windowed body, and the focused-hunk scroll, for unified and split.
func TestViewerGolden(t *testing.T) {
	for _, th := range []theme.Theme{theme.Dark(), theme.Light()} {
		for _, tc := range []struct {
			name  string
			build func() Viewer
		}{
			{"unified_hunk0", func() Viewer {
				v := NewViewer(sample, th)
				v.SetSize(80, 10)
				v.SetMode(Unified)
				return v
			}},
			{"unified_hunk1", func() Viewer {
				v := NewViewer(sample, th)
				v.SetSize(80, 10)
				v.SetMode(Unified)
				v.NextHunk()
				return v
			}},
			{"split_hunk0", func() Viewer {
				v := NewViewer(sample, th)
				v.SetSize(160, 10)
				v.SetMode(Split)
				return v
			}},
		} {
			v := tc.build()
			got := strings.ReplaceAll(strings.Join(v.View(), "\n"), "\x1b", "^[")
			eval.Golden(t, "viewer_"+tc.name+"_"+th.Name, got+"\n")
		}
	}
}

// TestViewerSingleCharEdit: a one-character change renders through the
// viewer with the intra-line emphasis marking only that character, which
// is the whole reason word diff exists. Golden-pinned so the span cannot
// silently widen to the whole line.
func TestViewerSingleCharEdit(t *testing.T) {
	const oneChar = `--- a/x.go
+++ b/x.go
@@ -1,1 +1,1 @@
-var x = 1
+var x = 2
`
	v := NewViewer(oneChar, theme.Dark())
	v.SetSize(60, 8)
	got := strings.ReplaceAll(strings.Join(v.View(), "\n"), "\x1b", "^[")
	eval.Golden(t, "viewer_single_char_edit", got+"\n")
}
