package sidebar

import (
	"strings"
	"testing"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

var full = State{
	Cwd:        "/home/tam/github/tamnd/ari",
	Model:      "claude-sonnet-5",
	Provider:   "anthropic",
	Effort:     "high",
	ContextPct: 0.42,
	CostUSD:    1.2345,
	Files: []FileChange{
		{Path: "core/session.go", Added: 120, Removed: 8},
		{Path: "ui/sidebar/sidebar.go", Added: 44, Removed: 0},
		{Path: "event/payloads.go", Added: 3, Removed: 3},
	},
	Colony: "one ant, awake",
}

func frame(st State, w, h int) string {
	s := New(theme.Dark())
	s.SetState(st)
	buf := uv.NewScreenBuffer(w, h)
	s.Draw(buf, uv.Rect(0, 0, w, h))
	return buf.String()
}

// TestDrawGolden pins the full panel tall, the squeezed panel short,
// and the debug variant with the drop counter.
func TestDrawGolden(t *testing.T) {
	var b strings.Builder
	b.WriteString("== tall ==\n" + frame(full, Width, 24))
	b.WriteString("== short ==\n" + frame(full, Width, 12))
	debug := full
	debug.Debug = true
	debug.Drops = 17
	b.WriteString("== debug ==\n" + frame(debug, Width, 24))
	eval.Golden(t, "frames", b.String())
}

// TestLedgerNumbersShown: the context fill and cost render from state,
// which the root fills from the ledger.
func TestLedgerNumbersShown(t *testing.T) {
	out := frame(full, Width, 24)
	for _, want := range []string{"42%", "$1.23", "claude-sonnet-5 (high)", "anthropic"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q:\n%s", want, out)
		}
	}
}

// TestDropCounterBehindToggle: drops render only in debug.
func TestDropCounterBehindToggle(t *testing.T) {
	st := full
	st.Drops = 9
	if strings.Contains(frame(st, Width, 24), "drops") {
		t.Error("drop counter visible without the debug toggle")
	}
	st.Debug = true
	if !strings.Contains(frame(st, Width, 24), "bus drops 9") {
		t.Error("drop counter missing with debug on")
	}
}

// TestShortHeightKeepsEssentials: squeezed vertically, the model line
// survives and the file list gives way with a more marker.
func TestShortHeightKeepsEssentials(t *testing.T) {
	st := full
	for i := range 20 {
		st.Files = append(st.Files, FileChange{Path: strings.Repeat("x", i) + ".go", Added: i})
	}
	out := frame(st, Width, 14)
	if !strings.Contains(out, "claude-sonnet-5") {
		t.Error("model line lost on a short terminal")
	}
	if !strings.Contains(out, "more") {
		t.Error("file overflow has no more marker")
	}
	if got := len(strings.Split(strings.TrimRight(out, "\n"), "\n")); got > 14 {
		t.Errorf("rendered %d lines into height 14", got)
	}
}

// TestLongPathsShortened: a deep cwd keeps its tail.
func TestLongPathsShortened(t *testing.T) {
	st := full
	st.Cwd = "/very/deep/path/that/never/ends/github/tamnd/ari"
	out := frame(st, Width, 24)
	if !strings.Contains(out, "tamnd/ari") {
		t.Error("shortened path lost its tail")
	}
	if strings.Contains(out, "/very/deep") {
		t.Error("long path was not shortened")
	}
}

// TestDiagnosticCountsShown: a language server with errors renders its
// name and tally; a clean one reads clean.
func TestDiagnosticCountsShown(t *testing.T) {
	st := full
	st.Diagnostics = []ServerDiag{
		{Name: "gopls", Errors: 2, Warnings: 1},
		{Name: "pyright", Errors: 0, Warnings: 0},
	}
	out := frame(st, Width, 24)
	for _, want := range []string{"language servers", "gopls", "2 err", "1 warn", "pyright", "clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("panel missing %q:\n%s", want, out)
		}
	}
}

// TestNoDiagnosticSectionWhenEmpty: no servers, no section.
func TestNoDiagnosticSectionWhenEmpty(t *testing.T) {
	if strings.Contains(frame(full, Width, 24), "language servers") {
		t.Error("language servers section shown with no servers")
	}
}

// TestInBounds at the layout width and when squeezed narrower.
func TestInBounds(t *testing.T) {
	s := New(theme.Dark())
	s.SetState(full)
	tea.AssertInBounds(t, s, Width, 24)
	tea.AssertInBounds(t, s, Width, 8)
	tea.AssertInBounds(t, s, 18, 24)
}
