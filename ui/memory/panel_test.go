package memory

import (
	"strings"
	"testing"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

func full() State {
	return State{
		Namespace: "worker/main",
		Index:     "- run make gen after schema.go (file:schema.go)\n- reuse the shared transport (file:transport.go)",
		Query:     "transport",
		Searched:  true,
		Selected:  1,
		Results: []Hit{
			{ID: "01aa", Label: "reuse the shared transport", Body: "One http.Transport for the whole process.", Stale: false},
			{ID: "01bb", Label: "gen is generated", Body: "Never hand-edit gen/*.go; run make gen.", Stale: true},
		},
		Folds: []Fold{
			{Namespace: "worker/main", Merged: 3, Reflections: 1, Archived: 2, Candidates: 5},
		},
	}
}

func frame(st State, th theme.Theme, w, h int) string {
	p := New(th)
	p.SetState(st)
	buf := uv.NewScreenBuffer(w, h)
	p.Draw(buf, uv.Rect(0, 0, w, h))
	return buf.String()
}

// TestDrawGolden pins the full panel in both themes, so a palette regression in
// either shows up in review.
func TestDrawGolden(t *testing.T) {
	var b strings.Builder
	b.WriteString("== dark ==\n" + frame(full(), theme.Dark(), 80, 30))
	b.WriteString("== light ==\n" + frame(full(), theme.Light(), 80, 30))
	eval.Golden(t, "panel", b.String())
}

// TestEmptyStateReads: no pins, no search, no folds each render their own
// placeholder rather than a blank region.
func TestEmptyStateReads(t *testing.T) {
	out := frame(State{Namespace: "worker/main"}, theme.Dark(), 80, 24)
	for _, want := range []string{"no pins yet", "type a query", "no folds yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("empty panel missing %q:\n%s", want, out)
		}
	}
}

// TestTypingBuildsQuery: printable keys extend the query and backspace trims it,
// so the search line tracks what the user typed.
func TestTypingBuildsQuery(t *testing.T) {
	p := New(theme.Dark())
	for _, r := range "ab" {
		p.HandleMsg(btea.KeyPressMsg{Text: string(r)})
	}
	if p.st.Query != "ab" {
		t.Fatalf("query = %q, want ab", p.st.Query)
	}
	p.HandleMsg(btea.KeyPressMsg{Code: btea.KeyBackspace})
	if p.st.Query != "a" {
		t.Fatalf("query after backspace = %q, want a", p.st.Query)
	}
}

// TestEnterSearches: enter answers with a Search carrying the trimmed query.
func TestEnterSearches(t *testing.T) {
	p := New(theme.Dark())
	p.SetState(State{Query: "  cache  "})
	act := p.HandleMsg(btea.KeyPressMsg{Code: btea.KeyEnter})
	s, ok := act.(Search)
	if !ok || s.Query != "cache" {
		t.Fatalf("enter = %#v, want Search{cache}", act)
	}
}

// TestForgetOnSelection: ctrl+d answers with a Forget for the highlighted row,
// and does nothing when there are no results to forget.
func TestForgetOnSelection(t *testing.T) {
	p := New(theme.Dark())
	if act := p.HandleMsg(ctrlD()); act != nil {
		t.Fatalf("ctrl+d with no results = %#v, want nil", act)
	}
	p.SetResults([]Hit{{ID: "01aa", Label: "a"}, {ID: "01bb", Label: "b"}})
	p.HandleMsg(btea.KeyPressMsg{Code: btea.KeyDown})
	act := p.HandleMsg(ctrlD())
	f, ok := act.(Forget)
	if !ok || f.ID != "01bb" {
		t.Fatalf("ctrl+d = %#v, want Forget{01bb}", act)
	}
}

// TestSelectionClampsToResults: down never moves the highlight past the last
// row, so a forget always names a real id.
func TestSelectionClampsToResults(t *testing.T) {
	p := New(theme.Dark())
	p.SetResults([]Hit{{ID: "only"}})
	for range 3 {
		p.HandleMsg(btea.KeyPressMsg{Code: btea.KeyDown})
	}
	if p.st.Selected != 0 {
		t.Fatalf("selected = %d, want 0 with one result", p.st.Selected)
	}
}

// TestFoldLogNewestFirst: folds prepend and cap, so the tail shows the most
// recent consolidations.
func TestFoldLogNewestFirst(t *testing.T) {
	p := New(theme.Dark())
	for i := range foldCap + 3 {
		p.AddFold(Fold{Namespace: "ns", Merged: i})
	}
	if len(p.st.Folds) != foldCap {
		t.Fatalf("folds = %d, want capped at %d", len(p.st.Folds), foldCap)
	}
	if p.st.Folds[0].Merged != foldCap+2 {
		t.Fatalf("newest merged = %d, want %d", p.st.Folds[0].Merged, foldCap+2)
	}
}

// TestInBounds at the panel's own size and squeezed narrower.
func TestInBounds(t *testing.T) {
	p := New(theme.Dark())
	p.SetState(full())
	tea.AssertInBounds(t, p, 80, 30)
	tea.AssertInBounds(t, p, 60, 20)
}

func ctrlD() btea.KeyPressMsg {
	return btea.KeyPressMsg{Code: 'd', Mod: btea.ModCtrl}
}
