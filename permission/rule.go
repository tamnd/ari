package permission

import (
	"fmt"
	"strings"

	"github.com/tamnd/ari/tool"
)

// Layer identifies where a rule came from, lowest precedence first.
// Denies union across layers, so a higher layer cannot un-deny a lower
// layer's deny; the layering only matters for the mode and for audit.
type Layer string

const (
	LayerUser         Layer = "user"
	LayerProject      Layer = "project"
	LayerProjectLocal Layer = "project_local"
	LayerFlag         Layer = "flag"
	LayerSession      Layer = "session" // "allow for session", in memory only
)

// Rule is one parsed permission rule. The Pattern keeps the content
// matcher as written; Source is the whole rule text verbatim, because
// the journal records what the user wrote, never a compiled form.
type Rule struct {
	Pattern tool.Pattern
	Layer   Layer
}

// Parse turns one rule source string into a Rule. The grammar is tiny:
// a tool name, optionally followed by a parenthesized content matcher.
// "tool", "tool()", and "tool(*)" all normalize to the tool-wide form,
// so there is exactly one way to mean the whole tool.
func Parse(source string, layer Layer) (Rule, error) {
	s := strings.TrimSpace(source)
	name, content := s, ""
	if open := strings.IndexByte(s, '('); open >= 0 {
		if !strings.HasSuffix(s, ")") {
			return Rule{}, fmt.Errorf("rule %q has an unclosed content matcher", source)
		}
		name, content = s[:open], s[open+1:len(s)-1]
	}
	if name == "" || strings.ContainsAny(name, " \t()") {
		return Rule{}, fmt.Errorf("rule %q has no tool name", source)
	}
	if content == "*" {
		content = "" // tool(*) is the tool-wide form
	}
	return Rule{
		Pattern: tool.Pattern{Tool: name, Content: content, Source: source},
		Layer:   layer,
	}, nil
}

// MustParse is Parse for rule literals in tests and defaults.
func MustParse(source string, layer Layer) Rule {
	r, err := Parse(source, layer)
	if err != nil {
		panic(err)
	}
	return r
}

// ParseAll parses a list of rule sources from one layer.
func ParseAll(sources []string, layer Layer) ([]Rule, error) {
	rules := make([]Rule, 0, len(sources))
	for _, s := range sources {
		r, err := Parse(s, layer)
		if err != nil {
			return nil, err
		}
		rules = append(rules, r)
	}
	return rules, nil
}

// Rules is the resolved rule set the pipeline evaluates. Each list is
// the union across settings layers; the stage order handles every
// cross-behavior interaction, so there is no precedence table here.
type Rules struct {
	Deny  []Rule
	Ask   []Rule
	Allow []Rule
}

// toolWide reports whether the rule applies to the whole tool.
func (r Rule) toolWide() bool { return r.Pattern.Content == "" }

// appliesTo reports whether the rule names this tool. "*" is any tool.
func (r Rule) appliesTo(toolName string) bool {
	return r.Pattern.Tool == toolName || r.Pattern.Tool == "*"
}
