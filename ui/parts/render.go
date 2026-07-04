package parts

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/tool"
	"github.com/tamnd/ari/ui/diff"
	"github.com/tamnd/ari/ui/markdown"
	"github.com/tamnd/ari/ui/theme"
)

// Renderer turns a part into styled lines within a width. Renderers are
// pure: same part, width, and theme yield the same block, which is what
// the memo (doc 02 section 5.3) and the goldens (section 17) rely on.
type Renderer interface {
	Render(p Part, width int, th theme.Theme) Block
}

// Render dispatches to the renderer for the part's kind and tool. It is
// the one entry point the chat adapter calls.
func Render(p Part, width int, th theme.Theme) Block {
	if width < 4 {
		width = 4
	}
	return rendererFor(p).Render(p, width, th)
}

func rendererFor(p Part) Renderer {
	switch p.Kind {
	case KindText:
		return textRenderer{}
	case KindReasoning:
		return reasoningRenderer{}
	case KindToolCall:
		return toolCallRenderer{}
	case KindToolResult:
		switch p.Tool {
		case "read":
			return readRenderer{}
		case "write":
			return writeRenderer{}
		case "edit":
			return editRenderer{}
		case "sh":
			return shRenderer{}
		case "find":
			return findRenderer{}
		case "fetch":
			return fetchRenderer{}
		default:
			return fallbackRenderer{}
		}
	case KindFinish:
		return finishRenderer{}
	default:
		return fallbackRenderer{}
	}
}

// textRenderer: markdown for the assistant, plain wrapped prose for the
// user. The assistant path is the same one-shot render the stream cache
// is oracle-tested against, so freezing a streamed message never
// changes its bytes.
type textRenderer struct{}

func (textRenderer) Render(p Part, width int, th theme.Theme) Block {
	if p.Role == RoleUser {
		return styleWrapped(p.Text, width, th.S.UserPrompt)
	}
	out := markdown.Render(p.Text, width, th.S.Markdown)
	if out == "" {
		return nil
	}
	return strings.Split(out, "\n")
}

// reasoningRenderer: the thinking stream, dim, with the elapsed time
// once it closes.
type reasoningRenderer struct{}

func (reasoningRenderer) Render(p Part, width int, th theme.Theme) Block {
	b := styleWrapped(p.Text, width, th.S.Reasoning)
	if !p.Finished.IsZero() && !p.Started.IsZero() {
		d := p.Finished.Sub(p.Started).Round(100 * time.Millisecond)
		b = append(b, fit(th.S.Faint.Render(fmt.Sprintf("thought for %s", d)), width))
	}
	return b
}

// toolCallRenderer: the invocation line, name plus a one-line argument
// summary, marked pending until the result lands.
type toolCallRenderer struct{}

func (toolCallRenderer) Render(p Part, width int, th theme.Theme) Block {
	a := th.Accent(p.Ant)
	head := th.S.ToolName.Foreground(a.Color).Render(string(a.Glyph)+" "+p.Tool) + " " +
		th.S.ToolInput.Render(argSummary(p.Args))
	if p.Finished.IsZero() {
		head += th.S.Faint.Render(" …")
	}
	return Block{ansi.Truncate(head, width, "…")}
}

// argSummary flattens a JSON argument object into "k=v k=v", the most
// scannable one-line form.
func argSummary(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return string(raw)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%v", k, m[k]))
	}
	return strings.Join(parts, " ")
}

// readRenderer: a file header and a bounded preview; the full content
// already lives in the model context, the human wants the shape.
type readRenderer struct{}

const previewLines = 8

func (readRenderer) Render(p Part, width int, th theme.Theme) Block {
	text := resultText(p)
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	b := Block{}
	for i, l := range lines {
		if i == previewLines {
			b = append(b, fit(th.S.Faint.Render(fmt.Sprintf("… +%d more lines", len(lines)-i)), width))
			break
		}
		b = append(b, fit(th.S.ToolOutput.Render(l), width))
	}
	return b
}

// writeRenderer: the path and what happened, then the written content
// previewed as added lines.
type writeRenderer struct{}

func (writeRenderer) Render(p Part, width int, th theme.Theme) Block {
	d, ok := p.Result.(tool.WriteDisplay)
	if !ok {
		return fallbackRenderer{}.Render(p, width, th)
	}
	verb := "rewrote"
	if d.Created {
		verb = "created"
	}
	b := Block{fit(th.S.Info.Render(verb+" "+d.Path), width)}
	for i, l := range strings.Split(strings.TrimRight(d.Content, "\n"), "\n") {
		if i == previewLines {
			b = append(b, th.S.Faint.Render("…"))
			break
		}
		b = append(b, fit(th.S.Diff.GutterAdd.Render("+ ")+th.S.ToolOutput.Render(l), width))
	}
	return append(b, diagnosticLines(d.Diagnostics, width, th)...)
}

// diagnosticLines renders the language server's findings for a write or
// edit under the content, one red line each with its 1-based position, so
// the same errors the model reads in its result are visible to the human
// watching. Nothing when the file came back clean.
func diagnosticLines(ds []tool.Diagnostic, width int, th theme.Theme) Block {
	var b Block
	for _, d := range ds {
		line := fmt.Sprintf("%s [%d:%d] %s", strings.ToUpper(d.Severity), d.Line, d.Col, d.Message)
		b = append(b, ansi.Truncate(th.S.Error.Render(line), width, "…"))
	}
	return b
}

// editRenderer: the real diff, through the shared ui/diff renderer, so
// the chat result shows exactly what the permission prompt approved with
// no second code path (plan 02 slice 2). Line numbers, gutters, chroma
// coloring, and intra-line word emphasis all come from that one package.
type editRenderer struct{}

func (editRenderer) Render(p Part, width int, th theme.Theme) Block {
	d, ok := p.Result.(tool.EditDisplay)
	if !ok {
		return fallbackRenderer{}.Render(p, width, th)
	}
	b := Block{fit(th.S.Info.Render("edited "+d.Path), width)}
	// diff.Render keeps its own minimum width so a diff never collapses to
	// nonsense; the chat block still has to fit whatever width it is given,
	// so clip each line back to that width here.
	for _, l := range diff.Render(d.Diff, width, th, diff.Auto) {
		b = append(b, ansi.Truncate(l, width, "…"))
	}
	return append(b, diagnosticLines(d.Diagnostics, width, th)...)
}

// shRenderer: the command, the captured output, and how it exited.
type shRenderer struct{}

func (shRenderer) Render(p Part, width int, th theme.Theme) Block {
	d, ok := p.Result.(tool.ShDisplay)
	if !ok {
		return fallbackRenderer{}.Render(p, width, th)
	}
	b := Block{ansi.Truncate(th.S.ToolName.Render("$ ")+th.S.Base.Render(d.Command), width, "…")}
	if out := strings.TrimRight(d.Output, "\n"); out != "" {
		for l := range strings.SplitSeq(out, "\n") {
			b = append(b, ansi.Truncate(th.S.ToolOutput.Render(l), width, "…"))
		}
	}
	switch {
	case d.Background != "":
		b = append(b, fit(th.S.Info.Render("running in background: "+d.Background), width))
	case d.Killed:
		b = append(b, fit(th.S.Error.Render("killed (timeout)"), width))
	case d.ExitCode != 0:
		b = append(b, fit(th.S.Error.Render(fmt.Sprintf("exit %d", d.ExitCode)), width))
	}
	return b
}

// findRenderer: matches grouped by file, the way a human scans them.
type findRenderer struct{}

func (findRenderer) Render(p Part, width int, th theme.Theme) Block {
	d, ok := p.Result.(tool.FindDisplay)
	if !ok {
		return fallbackRenderer{}.Render(p, width, th)
	}
	var b Block
	for _, f := range d.Files {
		b = append(b, ansi.Truncate(th.S.Info.Render(f.Path), width, "…"))
		for _, m := range f.Matches {
			line := th.S.Faint.Render(fmt.Sprintf("%5d ", m.Line)) + th.S.ToolOutput.Render(m.Text)
			b = append(b, ansi.Truncate(line, width, "…"))
		}
	}
	note := fmt.Sprintf("%d matches", d.Total)
	if d.Capped {
		note += " (capped)"
	}
	return append(b, fit(th.S.Faint.Render(note), width))
}

// fetchRenderer: where it went and what came back, never the body.
type fetchRenderer struct{}

func (fetchRenderer) Render(p Part, width int, th theme.Theme) Block {
	d, ok := p.Result.(tool.FetchDisplay)
	if !ok {
		return fallbackRenderer{}.Render(p, width, th)
	}
	line := th.S.Info.Render(d.URL) + th.S.Faint.Render(fmt.Sprintf("  %s, %d bytes", d.ContentType, d.Bytes))
	return Block{ansi.Truncate(line, width, "…")}
}

// fallbackRenderer is the safety net for tool results the UI has no
// typed renderer for, which is exactly the MCP case (D20): pretty JSON,
// wrapped, never a crash.
type fallbackRenderer struct{}

func (fallbackRenderer) Render(p Part, width int, th theme.Theme) Block {
	text := resultText(p)
	style := th.S.ToolOutput
	if p.Kind == KindToolResult && !p.OK {
		style = th.S.ToolErr
	}
	var b Block
	for l := range strings.SplitSeq(strings.TrimRight(text, "\n"), "\n") {
		b = append(b, ansi.Truncate(style.Render(l), width, "…"))
	}
	return b
}

// finishRenderer: the end-of-turn ledger line.
type finishRenderer struct{}

func (finishRenderer) Render(p Part, width int, th theme.Theme) Block {
	line := fmt.Sprintf("%s · %d in / %d out", p.Stop, p.Usage.Input, p.Usage.Output)
	if p.Usage.CacheRead > 0 {
		line += fmt.Sprintf(" (%d cached)", p.Usage.CacheRead)
	}
	return Block{ansi.Truncate(th.S.Faint.Render(line), width, "…")}
}

// resultText flattens whatever Result holds into text: a string as-is,
// anything else as indented JSON, falling back to the part text.
func resultText(p Part) string {
	switch v := p.Result.(type) {
	case string:
		if v != "" {
			return v
		}
	case nil:
	default:
		if raw, err := json.MarshalIndent(v, "", "  "); err == nil {
			return string(raw)
		}
	}
	return p.Text
}

// styleWrapped word-wraps plain text to width and styles each line.
func styleWrapped(text string, width int, style interface{ Render(...string) string }) Block {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	// Wrap, not Wordwrap: a token longer than the width has to break
	// rather than overflow.
	wrapped := ansi.Wrap(strings.TrimRight(text, "\n"), width, "")
	var b Block
	for l := range strings.SplitSeq(wrapped, "\n") {
		b = append(b, style.Render(l))
	}
	return b
}

// fit truncates a styled line to the width. Every line a renderer emits
// goes through this or an equivalent, which is what the narrow-width
// test enforces.
func fit(line string, width int) string {
	return ansi.Truncate(line, width, "…")
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
