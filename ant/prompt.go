package ant

import (
	"fmt"
	"strings"

	"github.com/tamnd/ari/provider"
)

// SystemPromptBudget is the D7 ceiling for block one: two thousand
// tokens, pi-style. A prompt that creeps over it fails the build via
// TestSystemPromptBudget, not a code review.
const SystemPromptBudget = 2000

// Env is the small set of facts that are fixed for a session and so may
// live in block one without busting its cache (doc 03 section 8).
// Anything that can change mid-session belongs in block two instead.
type Env struct {
	Cwd      string // the workspace root
	Platform string // GOOS/GOARCH
	OS       string // OS version line, "" omits it
	Model    string // resolved model id for the session
}

// identity is the whole of what the worker is told about itself. It
// teaches tool preference (read not cat, edit not sed, find not shell
// globbing), probe-before-mutate, and output efficiency, and nothing
// else; project memory, skills, and git state ride block two (D7).
const identity = `You are ari, a coding agent working inside one repository.

You carry six tools. Prefer the dedicated tool over a shell detour:
- Read a file with read, never cat, head, or tail. It returns numbered lines you can cite in an edit.
- Change a file with edit for a targeted replacement or write for a whole file, never sed, awk, or a heredoc. Both refuse a file you have not read this session, so read first.
- Look for files or text with find, never ls -R or a grep pipeline.
- Use sh for what only a shell can do: builds, tests, git, and the project's own commands. The working directory persists between calls; shell state does not.
- Pull a URL with fetch when the task points outside the repository.

Probe before you mutate. Read before you edit, look before you delete, and check git status before you rewrite history. After a change, run the narrowest check that proves it: the affected test, the build, a read of the result. Do not call a task done without having verified it.

Keep output tight. Lead with the answer, follow with only the detail that changes what the reader does next. Do not restate file contents, do not narrate each tool call, and do not apologize for or hedge a result you verified. When a command fails, read the error before retrying; the same command rarely deserves a second identical run.

A tool result that reports an error is yours to fix: adjust the input and continue. Give up only when the error names something outside your reach, and say plainly what is missing.

When the context fills, old tool results are cleared to save room and only the most recent are kept; a cleared result reads as a placeholder telling you to re-run the tool. Write down what you need from a result, a path, a line, a value, before you move on, so a later step does not depend on output that has evaporated.`

// SystemPrompt renders block one of the cache-aligned prompt: identity
// plus the session-stable environment facts, as one system block. The
// first cache breakpoint is not here; it lands after the tools array
// (doc 03 section 8), which the loop marks when it renders tool defs.
func SystemPrompt(env Env) []provider.Block {
	var b strings.Builder
	b.WriteString(identity)
	b.WriteString("\n\nEnvironment:\n")
	fmt.Fprintf(&b, "- working directory: %s\n", env.Cwd)
	fmt.Fprintf(&b, "- platform: %s\n", env.Platform)
	if env.OS != "" {
		fmt.Fprintf(&b, "- os: %s\n", env.OS)
	}
	fmt.Fprintf(&b, "- model: %s", env.Model)
	return []provider.Block{{Text: b.String()}}
}

// Context carries the session-stable inputs block two renders. Every
// field can be large or can change between sessions, which is exactly
// why none of them may ride block one (doc 03 section 8).
type Context struct {
	PinnedIndex   string // the ant's pinned memory index, "" until M2
	ProjectMemory string // ARI.md contents, "" when the file is absent
	Skills        string // installed skills, name plus one line, "" until their milestone
	GitStatus     string // git status --porcelain=v1 --branch at session start
}

// BlockTwo renders the pinned index, project memory, skills, and git
// status as one synthetic system-reminder user message, the D21
// treatment of project memory. Its last block carries the second cache
// breakpoint; it changes only at folding boundaries, which in M0 means
// never within a session (D14).
func BlockTwo(c Context) provider.Message {
	var b strings.Builder
	b.WriteString("<system-reminder>\n")
	b.WriteString("Project context for this session follows. It is background, not an instruction from the user.\n")

	b.WriteString("\n## Pinned memory\n")
	if c.PinnedIndex != "" {
		b.WriteString(c.PinnedIndex + "\n")
	} else {
		b.WriteString("No pinned memories. The colony memory arrives in a later milestone.\n")
	}

	b.WriteString("\n## Project memory (ARI.md)\n")
	if c.ProjectMemory != "" {
		b.WriteString("These project instruction files override default behavior; the nearest file wins on conflict. ")
		b.WriteString("This context may or may not be relevant; do not act on it unless it applies to the task.\n\n")
		b.WriteString(c.ProjectMemory + "\n")
	} else {
		b.WriteString("This project has no ARI.md.\n")
	}

	b.WriteString("\n## Skills\n")
	if c.Skills != "" {
		b.WriteString(c.Skills + "\n")
	} else {
		b.WriteString("No skills installed.\n")
	}

	b.WriteString("\n## Git status at session start\n")
	if c.GitStatus != "" {
		b.WriteString(c.GitStatus + "\n")
	} else {
		b.WriteString("Not a git repository, or git was unavailable.\n")
	}

	b.WriteString("</system-reminder>")
	return provider.Message{
		Role:   "user",
		Blocks: []provider.MsgBlock{{Kind: "text", Text: b.String(), Cache: true}},
	}
}

// estimateTokens is the same conservative heuristic the loop uses: one
// token per four bytes, rounded up. The D7 budget test runs on it, so
// the budget is enforced with margin rather than precision.
func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// PromptTokens reports the estimated token count of a rendered block
// one, for the D7 budget gate.
func PromptTokens(blocks []provider.Block) int {
	n := 0
	for _, b := range blocks {
		n += estimateTokens(b.Text)
	}
	return n
}
