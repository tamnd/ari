package colony

import (
	"fmt"
	"strings"
)

// renderSkill renders a card's Discovery section as an agentskills.io-style
// SKILL.md: YAML frontmatter carrying the name and the summary, then a body
// a human can read to know what the ant is for. It is a projection of the D
// section, so card.json stays the source of truth and SKILL.md is generated
// beside it for the human and for skill-directory tooling.
func renderSkill(c Card) string {
	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "name: %s\n", c.Name)
	fmt.Fprintf(&b, "description: %s\n", oneLine(c.Discovery.Summary))
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", c.Name)
	if c.Discovery.Summary != "" {
		b.WriteString(c.Discovery.Summary)
		b.WriteString("\n\n")
	}

	if len(c.Discovery.Classes) > 0 {
		b.WriteString("## Task classes\n\n")
		for _, cl := range c.Discovery.Classes {
			fmt.Fprintf(&b, "- %s\n", cl)
		}
		b.WriteString("\n")
	}
	if len(c.Discovery.Prefers) > 0 {
		b.WriteString("## Prefers\n\n")
		for _, p := range c.Discovery.Prefers {
			fmt.Fprintf(&b, "- %s\n", p)
		}
		b.WriteString("\n")
	}
	if len(c.Discovery.Signals) > 0 {
		b.WriteString("## Signals\n\n")
		for _, s := range c.Discovery.Signals {
			fmt.Fprintf(&b, "- `%s`\n", s)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Tools\n\n")
	fmt.Fprintf(&b, "%s\n\n", strings.Join(c.Tools, ", "))

	b.WriteString("## Verification\n\n")
	if c.Verify.Check != "" {
		fmt.Fprintf(&b, "%s\n", c.Verify.Check)
	}
	return b.String()
}

// oneLine flattens a summary to a single frontmatter-safe line, collapsing
// newlines so the YAML description never spills across lines.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
