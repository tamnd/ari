package perm

import (
	"strings"
	"testing"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

func key(s string) btea.KeyPressMsg {
	switch s {
	case "enter":
		return btea.KeyPressMsg{Code: btea.KeyEnter}
	case "tab":
		return btea.KeyPressMsg{Code: btea.KeyTab}
	case "left":
		return btea.KeyPressMsg{Code: btea.KeyLeft}
	case "right":
		return btea.KeyPressMsg{Code: btea.KeyRight}
	default:
		return btea.KeyPressMsg{Code: rune(s[0]), Text: s}
	}
}

var reqs = map[string]event.PermissionRequested{
	"edit_diff": {
		ID: "p1", Call: "c1", Tool: "edit",
		Consequence: event.Consequence{Kind: "diff", Content: "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n package main\n-var debug = false\n+var debug = true\n"},
		Reason:      "edit outside the workspace",
	},
	"sh_command": {
		ID: "p2", Call: "c2", Tool: "sh",
		Consequence: event.Consequence{Kind: "command", Content: "rm -rf build/\nmake all"},
		Suggestions: []string{"allow rm under build/ for this session"},
		Reason:      "rm is not on the allowlist",
	},
	"fetch_url": {
		ID: "p3", Call: "c3", Tool: "fetch",
		Consequence: event.Consequence{Kind: "url", Content: "https://example.com/api/data"},
	},
	"json_fullauto": {
		ID: "p4", Call: "c4", Tool: "deploy",
		Consequence: event.Consequence{Kind: "json", Content: "{\n  \"target\": \"prod\"\n}"},
		Mode:        "full-auto",
		Reason:      "deploy always asks",
	},
}

func frameOf(d *Dialog, w, h int) string {
	buf := uv.NewScreenBuffer(w, h)
	d.Draw(buf, uv.Rect(0, 0, w, h))
	return buf.Buffer.String()
}

// TestDrawGolden pins one frame per consequence kind, plus the
// full-auto warning restyle, in both themes. Plain-text frames: the
// styling bytes are pinned by the theme and diff goldens.
func TestDrawGolden(t *testing.T) {
	for _, thName := range []string{"dark", "light"} {
		th := theme.Dark()
		if thName == "light" {
			th = theme.Light()
		}
		var b strings.Builder
		for _, name := range []string{"edit_diff", "sh_command", "fetch_url", "json_fullauto"} {
			b.WriteString("== " + name + " ==\n")
			b.WriteString(frameOf(New(reqs[name], th), 80, 20))
		}
		eval.Golden(t, "draw_"+thName, b.String())
	}
}

// TestDenyIsDefault: opening the dialog and pressing enter denies.
func TestDenyIsDefault(t *testing.T) {
	d := New(reqs["sh_command"], theme.Dark())
	act := d.HandleMsg(key("enter"))
	if act != (Decision{ID: "p2", Choice: Deny}) {
		t.Fatalf("default answer = %v, want deny", act)
	}
}

// TestFocusCycle: tab walks deny, allow once, allow session and wraps;
// left goes backward.
func TestFocusCycle(t *testing.T) {
	d := New(reqs["sh_command"], theme.Dark())
	d.HandleMsg(key("tab"))
	if act := d.HandleMsg(key("enter")); act != (Decision{ID: "p2", Choice: AllowOnce}) {
		t.Fatalf("after one tab: %v, want allow once", act)
	}
	d.HandleMsg(key("tab"))
	if act := d.HandleMsg(key("enter")); act != (Decision{ID: "p2", Choice: AllowSession}) {
		t.Fatalf("after two tabs: %v, want allow session", act)
	}
	d.HandleMsg(key("tab")) // wraps to deny
	d.HandleMsg(key("left"))
	if act := d.HandleMsg(key("enter")); act != (Decision{ID: "p2", Choice: AllowSession}) {
		t.Fatalf("wrap then left: %v, want allow session", act)
	}
}

// TestShortcuts: d, a, s answer directly regardless of focus.
func TestShortcuts(t *testing.T) {
	for _, tc := range []struct {
		k    string
		want Choice
	}{{"d", Deny}, {"n", Deny}, {"a", AllowOnce}, {"y", AllowOnce}, {"s", AllowSession}} {
		d := New(reqs["edit_diff"], theme.Dark())
		if act := d.HandleMsg(key(tc.k)); act != (Decision{ID: "p1", Choice: tc.want}) {
			t.Errorf("key %q = %v, want choice %v", tc.k, act, tc.want)
		}
	}
}

// TestUnknownKeysReturnNil: unmapped input answers nothing.
func TestUnknownKeysReturnNil(t *testing.T) {
	d := New(reqs["edit_diff"], theme.Dark())
	if act := d.HandleMsg(key("x")); act != nil {
		t.Fatalf("unmapped key answered: %v", act)
	}
	if act := d.HandleMsg("not a key"); act != nil {
		t.Fatalf("non-key message answered: %v", act)
	}
}

// TestConsequenceCapped: a consequence taller than the viewport is
// clipped with a more-lines marker and the choices survive on screen.
func TestConsequenceCapped(t *testing.T) {
	req := reqs["sh_command"]
	req.Consequence.Content = strings.Repeat("echo line\n", 60)
	d := New(req, theme.Dark())
	frame := frameOf(d, 80, 16)
	if !strings.Contains(frame, "more lines") {
		t.Error("tall consequence not clipped with a marker")
	}
	if !strings.Contains(frame, "deny") {
		t.Error("choices fell off the bottom")
	}
}

// TestModeWarning: a non-default mode adds the warning line; the
// default mode does not.
func TestModeWarning(t *testing.T) {
	if f := frameOf(New(reqs["json_fullauto"], theme.Dark()), 80, 20); !strings.Contains(f, "full-auto") {
		t.Error("full-auto request missing the mode warning")
	}
	if f := frameOf(New(reqs["edit_diff"], theme.Dark()), 80, 20); strings.Contains(f, "mode") {
		t.Error("default-mode request grew a mode warning")
	}
}

// TestInBounds at a few viewport sizes, including tiny.
func TestInBounds(t *testing.T) {
	for _, name := range []string{"edit_diff", "sh_command", "json_fullauto"} {
		d := New(reqs[name], theme.Dark())
		tea.AssertInBounds(t, d, 80, 20)
		tea.AssertInBounds(t, d, 150, 40)
		tea.AssertInBounds(t, d, 30, 8)
	}
}
