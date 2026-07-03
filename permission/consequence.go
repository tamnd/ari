package permission

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/tool"
)

// RenderConsequence builds the one preview every client shows for a
// permission ask: a real unified diff for edit and write, the command
// for sh, the URL for fetch, pretty JSON for everything else (D2, D15,
// doc 01 section 5.3). It is rendered here in the core so no client
// re-derives it from raw tool input.
func RenderConsequence(toolName string, input json.RawMessage) event.Consequence {
	switch toolName {
	case "edit":
		if diff, ok := renderEditDiff(input); ok {
			return event.Consequence{Kind: "diff", Content: diff}
		}
	case "write":
		if diff, ok := renderWriteDiff(input); ok {
			return event.Consequence{Kind: "diff", Content: diff}
		}
	case "sh":
		var a struct {
			Command string `json:"command"`
		}
		if json.Unmarshal(input, &a) == nil && a.Command != "" {
			return event.Consequence{Kind: "command", Content: a.Command}
		}
	case "fetch":
		var a struct {
			URL string `json:"url"`
		}
		if json.Unmarshal(input, &a) == nil && a.URL != "" {
			return event.Consequence{Kind: "url", Content: a.URL}
		}
	}
	return event.Consequence{Kind: "json", Content: prettyJSON(input)}
}

// renderEditDiff previews an edit as the unified diff it would apply.
// The preview is best-effort: a file that cannot be read or an
// old_string that does not match falls back to the JSON render, and
// the edit tool's own validation carries the real refusal.
func renderEditDiff(input json.RawMessage) (string, bool) {
	var a struct {
		FilePath   string `json:"file_path"`
		OldString  string `json:"old_string"`
		NewString  string `json:"new_string"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if json.Unmarshal(input, &a) != nil || a.FilePath == "" {
		return "", false
	}
	old, err := os.ReadFile(a.FilePath)
	if err != nil || !strings.Contains(string(old), a.OldString) || a.OldString == "" {
		return "", false
	}
	count := 1
	if a.ReplaceAll {
		count = -1
	}
	after := strings.Replace(string(old), a.OldString, a.NewString, count)
	return tool.UnifiedDiff(a.FilePath, string(old), after), true
}

// renderWriteDiff previews a write as a diff from the current content,
// empty when the file does not exist yet, so a create shows as all
// additions.
func renderWriteDiff(input json.RawMessage) (string, bool) {
	var a struct {
		FilePath string `json:"file_path"`
		Content  string `json:"content"`
	}
	if json.Unmarshal(input, &a) != nil || a.FilePath == "" {
		return "", false
	}
	old, err := os.ReadFile(a.FilePath)
	if err != nil {
		old = nil
	}
	return tool.UnifiedDiff(a.FilePath, string(old), a.Content), true
}

// prettyJSON indents the raw input for the fallback render.
func prettyJSON(input json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, input, "", "  "); err != nil {
		return string(input)
	}
	return buf.String()
}
