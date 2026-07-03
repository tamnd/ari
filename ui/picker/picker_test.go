package picker

import (
	"strings"
	"testing"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

var sessions = []Item{
	{Key: "s1", Label: "fix the flaky bus test", Detail: "2h ago"},
	{Key: "s2", Label: "wire the sidebar", Detail: "1d ago"},
	{Key: "s3", Label: "release checklist", Detail: "3d ago"},
	{Key: "s4", Label: "explain the diff cache", Detail: "5d ago"},
}

func key(s string) btea.KeyPressMsg {
	switch s {
	case "enter":
		return btea.KeyPressMsg{Code: btea.KeyEnter}
	case "down":
		return btea.KeyPressMsg{Code: btea.KeyDown}
	case "up":
		return btea.KeyPressMsg{Code: btea.KeyUp}
	case "backspace":
		return btea.KeyPressMsg{Code: btea.KeyBackspace}
	}
	return btea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func typeWord(d *Dialog, word string) {
	for _, r := range word {
		d.HandleMsg(key(string(r)))
	}
}

// TestSelectFirst: enter on a fresh picker selects the first item and
// the action carries the dialog id, not any session knowledge.
func TestSelectFirst(t *testing.T) {
	d := New("session", "switch session", sessions, theme.Dark())
	if act := d.HandleMsg(key("enter")); act != (Chosen{ID: "session", Key: "s1"}) {
		t.Fatalf("selected %v, want s1", act)
	}
}

// TestFilterNarrows: typing narrows to matching rows and enter picks
// from the filtered view.
func TestFilterNarrows(t *testing.T) {
	d := New("session", "switch session", sessions, theme.Dark())
	typeWord(d, "diff")
	if got := len(d.filtered()); got != 1 {
		t.Fatalf("filter %q left %d rows, want 1", d.filter, got)
	}
	if act := d.HandleMsg(key("enter")); act != (Chosen{ID: "session", Key: "s4"}) {
		t.Fatalf("selected %v, want s4", act)
	}
}

// TestFilterMatchesKeyToo: the key is searchable, so a model id matches
// even when the label is friendly.
func TestFilterMatchesKeyToo(t *testing.T) {
	d := New("model", "pick a model", []Item{
		{Key: "claude-sonnet-5", Label: "balanced"},
		{Key: "claude-haiku-4-5", Label: "cheap"},
	}, theme.Dark())
	typeWord(d, "haiku")
	if act := d.HandleMsg(key("enter")); act != (Chosen{ID: "model", Key: "claude-haiku-4-5"}) {
		t.Fatalf("selected %v, want the haiku row", act)
	}
}

// TestBackspaceWidens: deleting filter characters restores rows and
// resets focus to the top.
func TestBackspaceWidens(t *testing.T) {
	d := New("session", "s", sessions, theme.Dark())
	typeWord(d, "zzz")
	if len(d.filtered()) != 0 {
		t.Fatal("bogus filter still matches")
	}
	for range 3 {
		d.HandleMsg(key("backspace"))
	}
	if got := len(d.filtered()); got != len(sessions) {
		t.Fatalf("after clearing filter %d rows, want %d", got, len(sessions))
	}
}

// TestEnterOnEmptyIsNoop: no matches means enter does nothing rather
// than selecting a phantom.
func TestEnterOnEmptyIsNoop(t *testing.T) {
	d := New("session", "s", sessions, theme.Dark())
	typeWord(d, "zzz")
	if act := d.HandleMsg(key("enter")); act != nil {
		t.Fatalf("enter on empty list produced %v", act)
	}
}

// TestArrowsWrap: focus wraps both directions over the filtered view.
func TestArrowsWrap(t *testing.T) {
	d := New("session", "s", sessions, theme.Dark())
	d.HandleMsg(key("up"))
	if act := d.HandleMsg(key("enter")); act != (Chosen{ID: "session", Key: "s4"}) {
		t.Fatalf("up from first = %v, want wrap to s4", act)
	}
}

// TestDialogContract: the picker satisfies dialog.Dialog and renders
// its rows.
func TestDialogContract(t *testing.T) {
	var d dialog.Dialog = New("session", "switch session", sessions, theme.Dark())
	buf := uv.NewScreenBuffer(70, 16)
	d.Draw(buf, buf.Bounds())
	out := buf.Buffer.String()
	for _, want := range []string{"switch session", "▸ fix the flaky bus test", "2h ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("picker frame missing %q:\n%s", want, out)
		}
	}
}

// TestDrawGolden pins the frame, fresh and filtered.
func TestDrawGolden(t *testing.T) {
	var b strings.Builder
	shot := func(name string, d *Dialog) {
		buf := uv.NewScreenBuffer(64, 14)
		d.Draw(buf, buf.Bounds())
		b.WriteString("== " + name + " ==\n" + buf.Buffer.String())
	}
	fresh := New("session", "switch session", sessions, theme.Dark())
	shot("fresh", fresh)
	typeWord(fresh, "the")
	shot("filtered", fresh)
	eval.Golden(t, "frames", b.String())
}
