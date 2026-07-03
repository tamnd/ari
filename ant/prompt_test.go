package ant

import (
	"strings"
	"testing"
)

// TestSystemPromptBudget is the D7 gate: block one stays under two
// thousand tokens or the build fails. The estimate is the same
// conservative bytes-over-four the loop uses, so headroom is real.
func TestSystemPromptBudget(t *testing.T) {
	blocks := SystemPrompt(Env{
		Cwd:      "/Users/someone/src/a-rather-long-project-directory-name",
		Platform: "darwin/arm64",
		OS:       "Darwin 24.6.0",
		Model:    "claude-opus-4-8",
	})
	got := PromptTokens(blocks)
	if got > SystemPromptBudget {
		t.Fatalf("block one is %d estimated tokens, over the %d budget (D7)", got, SystemPromptBudget)
	}
	t.Logf("block one: %d of %d estimated tokens", got, SystemPromptBudget)
}

func TestSystemPromptTeachesTheTools(t *testing.T) {
	text := SystemPrompt(Env{Cwd: "/w", Platform: "linux/amd64", Model: "m"})[0].Text
	for _, want := range []string{"read", "find", "write", "edit", "sh", "fetch", "Probe before you mutate"} {
		if !strings.Contains(text, want) {
			t.Errorf("block one does not mention %q", want)
		}
	}
	if !strings.Contains(text, "working directory: /w") {
		t.Error("block one is missing the environment facts")
	}
}

func TestBlockTwoRendersEverySection(t *testing.T) {
	msg := BlockTwo(Context{
		PinnedIndex:   "- [worker/main] prefer table-driven tests",
		ProjectMemory: "Run make check before pushing.",
		GitStatus:     "## main\n M ant/prompt.go",
	})
	if msg.Role != "user" {
		t.Fatalf("block two must ride as a user message, got role %q", msg.Role)
	}
	if n := len(msg.Blocks); n != 1 {
		t.Fatalf("block two is one block, got %d", n)
	}
	b := msg.Blocks[0]
	if !b.Cache {
		t.Error("block two's last block must carry the second cache breakpoint (D14)")
	}
	text := b.Text
	if !strings.HasPrefix(text, "<system-reminder>") || !strings.HasSuffix(text, "</system-reminder>") {
		t.Error("block two must be wrapped in a system-reminder envelope")
	}
	for _, want := range []string{
		"prefer table-driven tests",
		"Run make check before pushing.",
		"No skills installed.",
		" M ant/prompt.go",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("block two is missing %q", want)
		}
	}
}

func TestBlockTwoPlaceholders(t *testing.T) {
	text := BlockTwo(Context{}).Blocks[0].Text
	for _, want := range []string{
		"No pinned memories",
		"no ARI.md",
		"No skills installed",
		"Not a git repository",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("empty context should render the %q placeholder", want)
		}
	}
}
