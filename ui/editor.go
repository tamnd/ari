package ui

import (
	"strings"

	"charm.land/bubbles/v2/textarea"
	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/input"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/tea"
	"github.com/tamnd/ari/ui/theme"
)

// EditorController owns the prompt textarea, the external-editor hatch,
// and nothing else. Submitting goes through the submit callback the
// root injects, so the editor never sees the client.
type EditorController struct {
	ta     textarea.Model
	submit func(text string) btea.Cmd
	w, h   int
}

// NewEditor builds a focused textarea with the prompt styling.
func NewEditor(th theme.Theme, submit func(string) btea.Cmd) *EditorController {
	ta := textarea.New()
	ta.Placeholder = "ask ari anything"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = th.S.UserPrompt.Render("> ")
	return &EditorController{ta: ta, submit: submit}
}

// Focus and Blur hand the textarea the keyboard.
func (e *EditorController) Focus() btea.Cmd { return e.ta.Focus() }
func (e *EditorController) Blur()           { e.ta.Blur() }

// Lines is the content height the layout grows the editor by.
func (e *EditorController) Lines() int { return e.ta.LineCount() }

// SetValue replaces the buffer, used when the external editor returns.
func (e *EditorController) SetValue(s string) { e.ta.SetValue(s) }

// Value reads the buffer, for tests and the cancel path.
func (e *EditorController) Value() string { return e.ta.Value() }

// Handle runs an editor-scope action.
func (e *EditorController) Handle(a keys.Action, _ btea.KeyPressMsg) btea.Cmd {
	switch a {
	case keys.Submit:
		text := strings.TrimSpace(e.ta.Value())
		if text == "" {
			return nil
		}
		e.ta.Reset()
		return e.submit(text)
	case keys.Newline:
		e.ta.InsertString("\n")
	case keys.ExternalEditor:
		return input.OpenEditor(e.ta.Value(), e.ta.Line()+1, 1)
	}
	return nil
}

// Type forwards a raw message to the textarea: typing, paste, blink.
func (e *EditorController) Type(msg btea.Msg) btea.Cmd {
	ta, cmd := e.ta.Update(msg)
	e.ta = ta
	return cmd
}

// Draw sizes the textarea to the area and paints its lines.
func (e *EditorController) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	if area.Dx() != e.w || area.Dy() != e.h {
		e.w, e.h = area.Dx(), area.Dy()
		e.ta.SetWidth(e.w)
		e.ta.SetHeight(e.h)
	}
	for i, l := range strings.Split(e.ta.View(), "\n") {
		if i >= area.Dy() {
			break
		}
		tea.DrawStyled(scr, uv.Rect(area.Min.X, area.Min.Y+i, area.Dx(), 1), l)
	}
	return nil
}
