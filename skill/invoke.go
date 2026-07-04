package skill

import (
	"context"
	"fmt"
	"strings"
)

// InlineRunner runs one command through the host's permission-gated sh path
// and returns its output. It is the second inline-shell gate: every command
// it receives has already matched the skill's allowed-tools, and it submits
// the command as a normal sh call so the full doc 05 pipeline, safety floor
// included, still decides (doc 13 section 2.7). A nil runner disables inline
// execution.
type InlineRunner func(ctx context.Context, command string) (string, error)

// Matcher reports whether a command is permitted by the skill's declared
// allowed-tools rules, the first inline-shell gate. It carries the doc 05
// normalization so a skill cannot smuggle a second command past a narrow
// allowance. The skill package holds no permission logic of its own, so the
// host builds this from the parsed rules and passes it in; a nil matcher
// denies every command, which is the safe default.
type Matcher func(command string) bool

// Request carries everything an invocation needs from the host: the raw and
// resolved arguments, the two inline-shell gates, and the session trust flag.
type Request struct {
	Args    string            // the whole argument string, for $ARGUMENTS
	Named   map[string]string // resolved named arguments, for $name
	Match   Matcher           // gate one: allowed-tools membership
	Inline  InlineRunner      // gate two: the permission-gated sh path
	Trusted bool              // gates three and four: a human-authored skill in a trusted session
}

// Invocation is the result of invoking a skill: the processed body ready to
// inject as a synthetic user message, the allowed-tools rules to add to the
// session for the duration of the invocation, and any notes worth logging.
type Invocation struct {
	Message string
	Grants  []string
	Notes   []string
}

// Invoke loads the skill's body, substitutes its arguments, runs the gated
// inline-shell pass, and wraps the result in the invocation marker. The
// allowed-tools rules ride back as Grants so the host can add them to the
// session; they narrow what the skill's own commands may ask for, never widen
// what the session allows (doc 13 sections 2.5 and 2.7).
func (s *Skill) Invoke(ctx context.Context, req Request) (Invocation, error) {
	body, err := s.Body()
	if err != nil {
		return Invocation{}, err
	}
	body = Substitute(body, req.Args, req.Named)

	var notes []string
	// context:fork is an M3 feature: a forked skill runs in a sub-agent with
	// its own token budget, and M1 has no sub-agents (non-goal N1). It loads
	// inline for now with a note that forking arrives with the colony, which
	// keeps the frontmatter forward-compatible without pretending M1 has
	// machinery it does not (doc 13 section 2.7).
	if strings.EqualFold(s.Context, "fork") {
		notes = append(notes, fmt.Sprintf("skill %s asked for context:fork; sub-agents arrive with the colony, so it runs inline for now", s.Name))
	}

	// Inline shell runs only when all four gates open: the skill declares
	// allowed-tools (gate one has something to match against), the session is
	// trusted and the skill is human-authored (gates three and four), and the
	// host wired both the matcher and the runner. Otherwise the "!" syntax
	// passes through as literal text (doc 13 section 2.7).
	inlineOK := req.Trusted && s.inlineTrusted() && len(s.AllowedTools) > 0 && req.Match != nil && req.Inline != nil
	filled, shellNotes := fillInlineShell(ctx, body, req, inlineOK)
	notes = append(notes, shellNotes...)

	return Invocation{
		Message: wrapInvocation(s.Name, filled),
		Grants:  append([]string(nil), s.AllowedTools...),
		Notes:   notes,
	}, nil
}

// inlineTrusted reports whether the skill's provenance allows inline shell:
// only the project and user scopes, which a human put on disk deliberately.
// A builtin or a skill that arrived over a machine channel never runs its own
// shell, matching the rule that MCP-sourced skills never execute shell
// injection (doc 13 section 2.7).
func (s *Skill) inlineTrusted() bool {
	return s.Scope == ScopeProject || s.Scope == ScopeUser
}

// wrapInvocation wraps a processed body in the marker that prevents
// re-invocation loops: once a tag for a name is in the context, the model
// follows the loaded instructions instead of invoking again (doc 13 section
// 2.5).
func wrapInvocation(name, body string) string {
	return fmt.Sprintf("<skill-invocation name=%q>\n%s\n</skill-invocation>", name, strings.TrimRight(body, "\n"))
}

// fillInlineShell splices the output of the body's inline commands in place.
// The command syntax is a backtick command prefixed with an exclamation mark,
// !`cmd`. When inline execution is not open, or a specific command fails its
// gate, the whole token is left as literal text rather than dropped, so the
// model still sees what the skill author wrote.
func fillInlineShell(ctx context.Context, body string, req Request, inlineOK bool) (string, []string) {
	var b strings.Builder
	var notes []string
	for i := 0; i < len(body); {
		cmd, tokenEnd, ok := inlineCommandAt(body, i)
		if !ok {
			b.WriteByte(body[i])
			i++
			continue
		}
		token := body[i:tokenEnd]
		switch {
		case !inlineOK:
			b.WriteString(token) // gates closed: literal text
		case !req.Match(cmd):
			// Gate one failed: the command is outside allowed-tools, so it is
			// not the skill's to run. Literal text, with a note.
			b.WriteString(token)
			notes = append(notes, fmt.Sprintf("inline command %q is not covered by allowed-tools; left as text", cmd))
		default:
			out, err := req.Inline(ctx, cmd)
			if err != nil {
				b.WriteString(token)
				notes = append(notes, fmt.Sprintf("inline command %q did not run: %v", cmd, err))
			} else {
				b.WriteString(spliceOutput(cmd, out))
			}
		}
		i = tokenEnd
	}
	return b.String(), notes
}

// inlineCommandAt reports whether an inline command token starts at i and, if
// so, returns the command text and the index just past the closing backtick.
func inlineCommandAt(body string, i int) (cmd string, end int, ok bool) {
	if body[i] != '!' || i+1 >= len(body) || body[i+1] != '`' {
		return "", 0, false
	}
	rel := strings.IndexByte(body[i+2:], '`')
	if rel < 0 {
		return "", 0, false // an unclosed backtick is not a command
	}
	closeAt := i + 2 + rel
	return body[i+2 : closeAt], closeAt + 1, true
}

// spliceOutput renders an inline command's output fenced and labeled so the
// transcript shows exactly what ran and what it printed.
func spliceOutput(cmd, out string) string {
	out = strings.TrimRight(out, "\n")
	return fmt.Sprintf("```\n$ %s\n%s\n```", cmd, out)
}

// NamedArgs maps a skill's declared arguments onto the whitespace-split
// argument string, so a slash command like /deploy staging fills $target when
// the frontmatter declares target as the first argument. Positions past the
// declared list are left to $ARGUMENTS and the positional $1/$2 forms.
func NamedArgs(s *Skill, args string) map[string]string {
	if len(s.Arguments) == 0 {
		return nil
	}
	fields := strings.Fields(args)
	named := make(map[string]string, len(s.Arguments))
	for i, spec := range s.Arguments {
		if i < len(fields) {
			named[spec.Name] = fields[i]
		}
	}
	return named
}

// NearMatches returns the discovered names closest to an unknown one, so an
// invalid invocation returns a helpful validation error rather than a bare
// miss. It is a cheap prefix-and-substring match, enough to catch a typo.
func NearMatches(names []string, target string) []string {
	var out []string
	for _, n := range names {
		if n == target || strings.HasPrefix(n, target) || strings.HasPrefix(target, n) || strings.Contains(n, target) {
			out = append(out, n)
		}
	}
	return out
}
