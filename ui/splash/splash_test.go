package splash

import (
	"strings"
	"testing"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

func key(s string) btea.KeyPressMsg {
	switch s {
	case "enter":
		return btea.KeyPressMsg{Code: btea.KeyEnter}
	case "down":
		return btea.KeyPressMsg{Code: btea.KeyDown}
	case "up":
		return btea.KeyPressMsg{Code: btea.KeyUp}
	}
	return btea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

// TestWordmarkGolden pins the logo bytes per theme: letterforms,
// gradient, and the deterministic background field.
func TestWordmarkGolden(t *testing.T) {
	for _, th := range []theme.Theme{theme.Dark(), theme.Light()} {
		got := strings.Join(Wordmark(th), "\n")
		eval.Golden(t, "wordmark_"+th.Name, strings.ReplaceAll(got, "\x1b", "^[")+"\n")
	}
}

// TestWordmarkCached: the same theme returns the identical slice, so a
// resize storm never re-renders the logo.
func TestWordmarkCached(t *testing.T) {
	th := theme.Dark()
	a, b := Wordmark(th), Wordmark(th)
	if &a[0] != &b[0] {
		t.Error("wordmark re-rendered on second call")
	}
	if c := Wordmark(theme.Light()); &a[0] == &c[0] {
		t.Error("themes share one cache entry")
	}
}

// TestWordmarkShape: every line is the same width and the mark is the
// letterform height.
func TestWordmarkShape(t *testing.T) {
	lines := Wordmark(theme.Dark())
	if len(lines) != 7 { // 5 letterform rows plus a field row each side
		t.Fatalf("wordmark is %d lines, want 7", len(lines))
	}
	w := ansi.StringWidth(lines[0])
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Errorf("line %d width %d, others %d", i, got, w)
		}
	}
}

// TestCompactGolden pins the sidebar variant.
func TestCompactGolden(t *testing.T) {
	var b strings.Builder
	for _, th := range []theme.Theme{theme.Dark(), theme.Light()} {
		b.WriteString(th.Name + ": " + Compact(th) + "\n")
	}
	eval.Golden(t, "compact", strings.ReplaceAll(b.String(), "\x1b", "^["))
}

// walk drives a dialog to a chosen option by label and returns the
// action enter produces.
func choose(t *testing.T, p *Pick, option string) any {
	t.Helper()
	for range len(p.options) {
		if p.options[p.focus] == option {
			return p.HandleMsg(key("enter"))
		}
		p.HandleMsg(key("down"))
	}
	t.Fatalf("option %q not in %v", option, p.options)
	return nil
}

// TestFlowSequence: provider, credential notice, tier, init, done, and
// the outcome carries every answer.
func TestFlowSequence(t *testing.T) {
	th := theme.Dark()
	f := NewFlow(th)

	p1 := f.Start()
	act := choose(t, p1, "anthropic")
	d2, done := f.Next(act)
	if done != nil || d2 == nil || d2.ID() != "onboard-credential" {
		t.Fatalf("after provider: dialog=%v done=%v, want the credential notice", d2, done)
	}

	n := d2.(*Notice)
	d3, done := f.Next(n.HandleMsg(key("enter")))
	if done != nil || d3.ID() != "onboard-tier" {
		t.Fatalf("after credential: dialog=%v done=%v, want tier pick", d3, done)
	}

	d4, done := f.Next(choose(t, d3.(*Pick), "balanced"))
	if done != nil || d4.ID() != "onboard-init" {
		t.Fatalf("after tier: dialog=%v done=%v, want init pick", d4, done)
	}

	d5, done := f.Next(choose(t, d4.(*Pick), "initialize now"))
	if d5 != nil {
		t.Fatalf("flow continued past the end: %v", d5)
	}
	want := Done{Outcome: Outcome{Provider: "anthropic", Tier: "balanced", InitProject: true}}
	if done != want {
		t.Fatalf("outcome = %+v, want %+v", done, want)
	}
}

// TestFlowCredentialNamesVarOnly: the credential step names the
// variable for the picked provider and never renders anything that
// looks like a captured value (D16).
func TestFlowCredentialNamesVarOnly(t *testing.T) {
	f := NewFlow(theme.Dark())
	d, _ := f.Next(Picked{ID: "onboard-provider", Option: "openai"})
	n := d.(*Notice)
	text := strings.Join(n.body, " ")
	if !strings.Contains(text, "OPENAI_API_KEY") {
		t.Errorf("credential notice does not name the variable: %q", text)
	}
	if strings.Contains(strings.ToLower(text), "enter") || strings.Contains(strings.ToLower(text), "paste") {
		t.Errorf("credential notice invites typing a secret: %q", text)
	}
}

// TestFlowIgnoresForeignActions: an action from some other dialog does
// not advance the flow.
func TestFlowIgnoresForeignActions(t *testing.T) {
	f := NewFlow(theme.Dark())
	if d, done := f.Next(Picked{ID: "theme-picker", Option: "dark"}); d != nil || done != nil {
		t.Fatalf("foreign action advanced the flow: %v %v", d, done)
	}
	if d, done := f.Next("garbage"); d != nil || done != nil {
		t.Fatalf("garbage advanced the flow: %v %v", d, done)
	}
}

// TestPickWraps: focus wraps both directions.
func TestPickWraps(t *testing.T) {
	p := NewPick("x", "t", "", []string{"a", "b", "c"}, theme.Dark())
	p.HandleMsg(key("up"))
	if act := p.HandleMsg(key("enter")); act != (Picked{ID: "x", Option: "c"}) {
		t.Fatalf("up from first = %v, want wrap to c", act)
	}
}

// TestDialogsSatisfyContract: both dialog kinds are dialog.Dialog and
// stay inside their rectangle.
func TestDialogsSatisfyContract(t *testing.T) {
	th := theme.Dark()
	var _ dialog.Dialog = NewPick("p", "t", "d", []string{"a"}, th)
	var _ dialog.Dialog = NewNotice("n", "t", []string{"b"}, th)

	buf := uv.NewScreenBuffer(60, 12)
	NewPick("p", "title", "detail", []string{"a", "b"}, th).Draw(buf, buf.Bounds())
	out := buf.String()
	for _, want := range []string{"title", "detail", "▸ a"} {
		if !strings.Contains(out, want) {
			t.Errorf("pick frame missing %q:\n%s", want, out)
		}
	}
}

// TestOnboardingGolden pins the three onboarding screens.
func TestOnboardingGolden(t *testing.T) {
	th := theme.Dark()
	f := NewFlow(th)
	var b strings.Builder
	shot := func(name string, d dialog.Dialog) {
		buf := uv.NewScreenBuffer(64, 12)
		d.Draw(buf, buf.Bounds())
		b.WriteString("== " + name + " ==\n" + buf.String())
	}
	p := f.Start()
	shot("provider", p)
	cred, _ := f.Next(Picked{ID: "onboard-provider", Option: "anthropic"})
	shot("credential", cred)
	tier, _ := f.Next(Acknowledged{ID: "onboard-credential"})
	shot("tier", tier)
	init, _ := f.Next(Picked{ID: "onboard-tier", Option: "best"})
	shot("init", init)
	eval.Golden(t, "onboarding", b.String())
}
