package theme

import (
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// TestExpandGolden pins the expansion of both stock palettes: one sample
// line per component style, escapes readable. A palette tweak shows up
// here as a reviewable diff over every surface it touches.
func TestExpandGolden(t *testing.T) {
	for _, th := range []Theme{Dark(), Light()} {
		s := th.S
		samples := []struct {
			name string
			out  string
		}{
			{"base", s.Base.Render("base text")},
			{"muted", s.Muted.Render("muted text")},
			{"subtle", s.Subtle.Render("subtle text")},
			{"faint", s.Faint.Render("faint text")},
			{"user_prompt", s.UserPrompt.Render("user prompt")},
			{"reasoning", s.Reasoning.Render("reasoning")},
			{"tool_name", s.ToolName.Render("tool name")},
			{"tool_input", s.ToolInput.Render("tool input")},
			{"tool_output", s.ToolOutput.Render("tool output")},
			{"tool_err", s.ToolErr.Render("tool error")},
			{"success", s.Success.Render("success")},
			{"warning", s.Warning.Render("warning")},
			{"error", s.Error.Render("error")},
			{"info", s.Info.Render("info")},
			{"title", s.Title.Render("title")},
			{"border", s.Border.Render("border")},
			{"selected", s.Selected.Render("selected")},
			{"diff_add", s.Diff.Add.Render("+ added line")},
			{"diff_del", s.Diff.Del.Render("- deleted line")},
			{"diff_header", s.Diff.Header.Render("@@ -1 +1 @@")},
			{"diff_context", s.Diff.Context.Render("  context")},
		}
		var b strings.Builder
		for _, sm := range samples {
			b.WriteString(sm.name + ": " + sm.out + "\n")
		}
		eval.Golden(t, "expand_"+th.Name, strings.ReplaceAll(b.String(), "\x1b", "^["))
	}
}

// TestAccentStable: the same ant gets the same accent on every call, and
// the worker (and empty ID) always get the primary with pi.
func TestAccentStable(t *testing.T) {
	th := Dark()
	first := th.Accent("scout-2")
	for range 3 {
		if got := th.Accent("scout-2"); got != first {
			t.Fatalf("accent for one ant drifted: %v then %v", first, got)
		}
	}
	w := th.Accent("worker")
	if w.Glyph != 'π' || w.Color != th.P.Primary {
		t.Errorf("worker accent = %v, want primary with pi", w)
	}
	if e := th.Accent(""); e != w {
		t.Errorf("empty ID accent = %v, want the worker accent", e)
	}
}

// TestAccentDeterministic: the assignment derives from the ID alone, so
// two theme instances agree without shared state.
func TestAccentDeterministic(t *testing.T) {
	a, b := Dark(), Dark()
	for _, id := range []string{"scout-1", "scout-2", "archivist", "queen"} {
		if x, y := a.Accent(id), b.Accent(id); x != y {
			t.Errorf("accent for %s differs across instances: %v vs %v", id, x, y)
		}
	}
}

// TestMarkdownMarginsZero: the glamour config must have no document
// margin, or the stream cache's fragment glue would drift from the
// full render (doc 02 section 6).
func TestMarkdownMarginsZero(t *testing.T) {
	for _, th := range []Theme{Dark(), Light()} {
		m := th.S.Markdown.Document.Margin
		if m == nil || *m != 0 {
			t.Errorf("%s: document margin = %v, want zero", th.Name, m)
		}
		if th.S.Markdown.Document.BlockPrefix != "" || th.S.Markdown.Document.BlockSuffix != "" {
			t.Errorf("%s: document block prefix/suffix must be empty", th.Name)
		}
	}
}
