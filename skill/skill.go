// Package skill discovers ari's skills and slash commands and renders the
// lazy, budgeted listing block two carries. A skill is a directory with a
// SKILL.md; a slash command is a markdown template under commands/. Both
// share one frontmatter parser, one discovery walk, and one precedence
// rule, because the moment they drift into two config formats they drift
// into two sets of bugs (doc 13 section 2.6, D20).
//
// This package reads frontmatter at discovery and bodies only on demand:
// the whole point of lazy loading is that a repo with fifty skills costs
// fifty small header reads at session start and nothing in the prompt
// beyond a name and one line each. Invocation, the step that injects a
// body into the context, is the next slice; this package stops at Body().
package skill

import (
	"fmt"
	"os"
	"strings"
)

// descriptionCap is the hard character ceiling on a description, the
// foundation of the token budget: one line is a skill's entire presence in
// the prompt until it is invoked (doc 13 section 2.2). An over-cap
// description is truncated with a marker, never rejected, so a foreign
// skill still loads.
const descriptionCap = 200

// Kind separates a directory skill from a command template. They share
// everything else, so the distinction is one field, not two types.
type Kind string

const (
	KindSkill   Kind = "skill"   // a directory with a SKILL.md
	KindCommand Kind = "command" // a markdown template under commands/
)

// Scope is where a skill was discovered, which sets its precedence and, in
// a later slice, whether inline shell execution is allowed.
type Scope string

const (
	ScopeProject Scope = "project" // .ari/skills or .ari/commands in the repo
	ScopeUser    Scope = "user"    // the global nest
	ScopeBuiltin Scope = "builtin" // bundled with the binary
)

// ArgSpec is one declared named argument for $name substitution.
type ArgSpec struct {
	Name     string
	Required bool
}

// Skill is one discovered skill or command. Everything here is read from
// the frontmatter at discovery; the body is read only when Body is called,
// which is what keeps discovery cheap (doc 13 section 2.2).
type Skill struct {
	Name         string
	Description  string
	Kind         Kind
	Scope        Scope
	Path         string   // the SKILL.md or command .md file
	AllowedTools []string // doc 05 rule strings, active only during invocation
	ArgumentHint string
	Arguments    []ArgSpec
	ModelHidden  bool   // disable-model-invocation: user-invocable only
	Model        string // tier hint, empty for the session default
	Context      string // inline or fresh, empty for the default

	// read is injected by tests so Body runs without touching disk. Nil
	// means os.ReadFile.
	read func(string) ([]byte, error)
}

// Warning is a skill that failed to load, surfaced to the user and to ari
// doctor rather than taking the session down: one malformed skill must
// never break discovery (doc 13 section 2.3).
type Warning struct {
	Path   string
	Reason string
}

func (w Warning) String() string { return w.Path + ": " + w.Reason }

// Body reads and returns the instruction body on demand, the lazy half of
// lazy loading. It is not read at discovery, so a listed-but-never-invoked
// skill costs only its frontmatter.
func (s *Skill) Body() (string, error) {
	read := s.read
	if read == nil {
		read = os.ReadFile
	}
	data, err := read(s.Path)
	if err != nil {
		return "", fmt.Errorf("reading skill %s: %w", s.Name, err)
	}
	_, body, err := splitFrontmatter(string(data))
	if err != nil {
		return "", fmt.Errorf("parsing skill %s: %w", s.Name, err)
	}
	return strings.TrimSpace(body), nil
}

// fromFrontmatter fills a Skill's declared fields from parsed frontmatter,
// applying the description cap and the name fallback. The name defaults to
// fallbackName (the directory or file stem) when the frontmatter omits it.
func fromFrontmatter(fm frontmatter, body, fallbackName string) (Skill, error) {
	name := fm.scalar("name")
	if name == "" {
		name = fallbackName
	}
	if !validName(name) {
		return Skill{}, fmt.Errorf("name %q must be lowercase letters, digits, and dashes", name)
	}
	desc := fm.scalar("description")
	if desc == "" {
		desc = firstMarkdownLine(body)
	}
	desc = capDescription(desc)
	return Skill{
		Name:         name,
		Description:  desc,
		AllowedTools: fm.list("allowed-tools"),
		ArgumentHint: fm.scalar("argument-hint"),
		Arguments:    parseArguments(fm.list("arguments")),
		ModelHidden:  fm.bool("disable-model-invocation"),
		Model:        fm.scalar("model"),
		Context:      fm.scalar("context"),
	}, nil
}

// parseArguments reads the arguments frontmatter list. Each item is a name,
// optional when it ends in "?", the pragmatic shape the substitution of the
// next slice needs without a nested-map YAML parser.
func parseArguments(items []string) []ArgSpec {
	var out []ArgSpec
	for _, it := range items {
		name := strings.TrimSpace(it)
		required := true
		if strings.HasSuffix(name, "?") {
			required = false
			name = strings.TrimSuffix(name, "?")
		}
		if name != "" {
			out = append(out, ArgSpec{Name: name, Required: required})
		}
	}
	return out
}

// validName holds the doc 13 rule: lowercase, digits, and dashes, so a name
// is a safe slash-command handle and a safe tool argument.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
		default:
			return false
		}
	}
	return true
}

// firstMarkdownLine is the description fallback: the first non-blank,
// non-heading line of the body, so a skill that omits the frontmatter
// description still lists with something useful (doc 13 section 2.2).
func firstMarkdownLine(body string) string {
	for line := range strings.SplitSeq(body, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "#") {
			continue
		}
		return t
	}
	return ""
}

// capDescription enforces descriptionCap on a rune boundary, appending an
// ellipsis so a truncated line reads as truncated.
func capDescription(desc string) string {
	desc = strings.TrimSpace(strings.ReplaceAll(desc, "\n", " "))
	if len(desc) <= descriptionCap {
		return desc
	}
	cut := descriptionCap - 1
	for cut > 0 && !utf8Start(desc[cut]) {
		cut--
	}
	return strings.TrimSpace(desc[:cut]) + "…"
}

// utf8Start reports whether b can start a UTF-8 sequence.
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }
