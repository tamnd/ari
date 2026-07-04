package parts

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/tool"
	"github.com/tamnd/ari/ui/theme"
)

func TestMain(m *testing.M) { eval.Main(m) }

var (
	started  = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	finished = started.Add(3140 * time.Millisecond)
)

// fixture is every renderer exercised once, plus the variants that take a
// different path through the same renderer (pending vs done, ok vs error,
// created vs rewrote). The goldens over this table are the pin for D23:
// a style tweak shows up as a reviewable diff, not a surprise on screen.
var fixture = []struct {
	name string
	p    Part
}{
	{"user_text", Part{
		Kind: KindText, Role: RoleUser,
		Text: "Please rename the config loader and make sure every call site follows, then run the tests.",
	}},
	{"assistant_markdown", Part{
		Kind: KindText, Role: RoleAssistant,
		Text: "## Plan\n\nRename goes in two steps:\n\n1. move the loader\n2. fix the call sites\n\nThen `go test ./...` settles it.",
	}},
	{"reasoning_open", Part{
		Kind: KindReasoning, Role: RoleAssistant,
		Text: "The loader is referenced from three packages, the rename has to land in one commit.",
	}},
	{"reasoning_done", Part{
		Kind: KindReasoning, Role: RoleAssistant,
		Text:    "The loader is referenced from three packages.",
		Started: started, Finished: finished,
	}},
	{"tool_call_pending", Part{
		Kind: KindToolCall, Role: RoleAssistant, Tool: "read",
		Args: json.RawMessage(`{"path":"config/loader.go","limit":40}`),
	}},
	{"tool_call_done", Part{
		Kind: KindToolCall, Role: RoleAssistant, Tool: "edit", Ant: "worker",
		Args:    json.RawMessage(`{"path":"config/loader.go"}`),
		Started: started, Finished: finished,
	}},
	{"read_result", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "read", OK: true,
		Result: "package config\n\nimport \"os\"\n\n// Load reads the config file.\nfunc Load(path string) (*Config, error) {\n\tdata, err := os.ReadFile(path)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n\treturn parse(data)\n}",
	}},
	{"write_created", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "write", OK: true,
		Result: tool.WriteDisplay{Path: "config/loader.go", Created: true,
			Content: "package config\n\nfunc Load() {}\n"},
	}},
	{"write_rewrote_long", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "write", OK: true,
		Result: tool.WriteDisplay{Path: "notes.txt",
			Content: "one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"},
	}},
	{"edit_diff", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "edit", OK: true,
		Result: tool.EditDisplay{Path: "config/loader.go",
			Diff: "--- a/config/loader.go\n+++ b/config/loader.go\n@@ -1,3 +1,3 @@\n package config\n-func LoadCfg() {}\n+func Load() {}\n"},
	}},
	{"edit_diagnostics", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "edit", OK: true,
		Result: tool.EditDisplay{Path: "config/loader.go",
			Diff:        "--- a/config/loader.go\n+++ b/config/loader.go\n@@ -1,3 +1,3 @@\n package config\n-var n int = 0\n+var n int = \"x\"\n",
			Diagnostics: []tool.Diagnostic{{Line: 2, Col: 5, Severity: "error", Message: "cannot use \"x\" as int value"}}},
	}},
	{"write_diagnostics", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "write", OK: true,
		Result: tool.WriteDisplay{Path: "config/loader.go",
			Content:     "package config\n\nvar n int = \"x\"\n",
			Diagnostics: []tool.Diagnostic{{Line: 3, Col: 5, Severity: "error", Message: "cannot use \"x\" as int value"}}},
	}},
	{"sh_ok", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "sh", OK: true,
		Result: tool.ShDisplay{Command: "go test ./config/", Output: "ok  \tari/config\t0.31s\n"},
	}},
	{"sh_exit", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "sh", OK: false,
		Result: tool.ShDisplay{Command: "go vet ./...", Output: "config/loader.go:9: unreachable code\n", ExitCode: 1},
	}},
	{"sh_killed", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "sh", OK: false,
		Result: tool.ShDisplay{Command: "sleep 600", Killed: true},
	}},
	{"sh_background", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "sh", OK: true,
		Result: tool.ShDisplay{Command: "go run ./cmd/serve", Background: "sh-3"},
	}},
	{"find_result", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "find", OK: true,
		Result: tool.FindDisplay{Total: 5, Capped: true, Files: []tool.FindFile{
			{Path: "config/loader.go", Matches: []tool.FindMatch{
				{Line: 9, Text: "func Load(path string) (*Config, error) {"},
				{Line: 21, Text: "\tcfg, err := Load(defaultPath)"},
			}},
			{Path: "main.go", Matches: []tool.FindMatch{
				{Line: 14, Text: "\tcfg, err := config.Load(*flagConfig)"},
			}},
		}},
	}},
	{"fetch_result", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "fetch", OK: true,
		Result: tool.FetchDisplay{URL: "https://go.dev/doc/comment", ContentType: "text/html", Bytes: 48213},
	}},
	{"fallback_json", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "mcp_weather", OK: true,
		Result: map[string]any{"city": "Hanoi", "temp": 31, "sky": "haze"},
	}},
	{"fallback_error", Part{
		Kind: KindToolResult, Role: RoleTool, Tool: "mcp_weather", OK: false,
		Result: "upstream timeout after 10s",
	}},
	{"finish", Part{
		Kind: KindFinish, Role: RoleAssistant, Stop: "end_turn",
		Usage: Usage{Input: 1200, Output: 340},
	}},
	{"finish_cached", Part{
		Kind: KindFinish, Role: RoleAssistant, Stop: "tool_use",
		Usage: Usage{Input: 5200, Output: 90, CacheRead: 4800},
	}},
}

// TestRenderGolden pins every renderer at width 60 under both themes.
// Escapes print as ^[ so the goldens diff readably.
func TestRenderGolden(t *testing.T) {
	for _, th := range []theme.Theme{theme.Dark(), theme.Light()} {
		var b strings.Builder
		for _, tc := range fixture {
			b.WriteString("== " + tc.name + " ==\n")
			for _, line := range Render(tc.p, 60, th) {
				b.WriteString(line + "\n")
			}
			b.WriteString("\n")
		}
		eval.Golden(t, "render_"+th.Name, strings.ReplaceAll(b.String(), "\x1b", "^["))
	}
}

// TestRenderPure asserts the memo contract: same part, width, and theme
// give byte-identical blocks on repeated calls.
func TestRenderPure(t *testing.T) {
	th := theme.Dark()
	for _, tc := range fixture {
		a := Render(tc.p, 60, th)
		b := Render(tc.p, 60, th)
		if len(a) != len(b) {
			t.Fatalf("%s: %d lines then %d", tc.name, len(a), len(b))
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("%s line %d differs between calls", tc.name, i)
			}
		}
	}
}

// TestRenderNarrow drives every part through a degenerate width and
// asserts nothing overflows it: the truncation floor holds everywhere.
func TestRenderNarrow(t *testing.T) {
	th := theme.Dark()
	for _, w := range []int{1, 4, 12} {
		want := max(w, 4)
		for _, tc := range fixture {
			for i, line := range Render(tc.p, w, th) {
				if got := ansi.StringWidth(line); got > want {
					t.Errorf("%s width %d line %d: %d cells wide: %q", tc.name, w, i, got, line)
				}
			}
		}
	}
}

// TestDiagnosticsRenderUnderChange: the language server's finding shows
// under an edit's diff and a write's content, so the human sees the same
// error the model was handed.
func TestDiagnosticsRenderUnderChange(t *testing.T) {
	th := theme.Dark()
	for _, name := range []string{"edit_diagnostics", "write_diagnostics"} {
		var p Part
		for _, f := range fixture {
			if f.name == name {
				p = f.p
			}
		}
		out := strings.Join(Render(p, 60, th), "\n")
		if !strings.Contains(out, "ERROR [") || !strings.Contains(out, "cannot use") {
			t.Errorf("%s did not render the diagnostic under the change:\n%s", name, out)
		}
	}
}
