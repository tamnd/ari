package skill

import (
	"strings"
	"testing"
)

// TestSubstituteArguments: $ARGUMENTS expands to the whole string.
func TestSubstituteArguments(t *testing.T) {
	out := Substitute("Fix $ARGUMENTS now.", "the login bug", nil)
	if out != "Fix the login bug now." {
		t.Errorf("got %q", out)
	}
}

// TestSubstitutePositional: $1 and $2 pull whitespace-split slots, and a
// missing slot expands to empty.
func TestSubstitutePositional(t *testing.T) {
	out := Substitute("from $1 to $2 (and $3)", "alpha beta", nil)
	if out != "from alpha to beta (and )" {
		t.Errorf("got %q", out)
	}
}

// TestSubstituteNamed: a $name placeholder resolves from the named map.
func TestSubstituteNamed(t *testing.T) {
	out := Substitute("deploy $target for $reason", "", map[string]string{
		"target": "staging",
		"reason": "smoke test",
	})
	if out != "deploy staging for smoke test" {
		t.Errorf("got %q", out)
	}
}

// TestSubstituteAppendsWhenNoPlaceholder: a template that references nothing
// still receives the arguments as a trailing line.
func TestSubstituteAppendsWhenNoPlaceholder(t *testing.T) {
	out := Substitute("Summarize the diff.", "keep it short", nil)
	if !strings.Contains(out, "Summarize the diff.") {
		t.Errorf("body lost: %q", out)
	}
	if !strings.HasSuffix(strings.TrimRight(out, "\n"), "ARGUMENTS: keep it short") {
		t.Errorf("arguments not appended: %q", out)
	}
}

// TestSubstituteNoAppendWhenReferenced: a body that does reference arguments
// gets no trailing block, even if the value is empty.
func TestSubstituteNoAppendWhenReferenced(t *testing.T) {
	out := Substitute("Only $1 matters.", "solo", nil)
	if strings.Contains(out, "ARGUMENTS:") {
		t.Errorf("appended despite a placeholder: %q", out)
	}
	empty := Substitute("Only $1 matters.", "", nil)
	if strings.Contains(empty, "ARGUMENTS:") {
		t.Errorf("appended for a referenced but empty arg: %q", empty)
	}
}

// TestSubstituteEscapedDollar: "$$" is a literal dollar.
func TestSubstituteEscapedDollar(t *testing.T) {
	out := Substitute("cost is $$5 for $1", "widgets", nil)
	if out != "cost is $5 for widgets" {
		t.Errorf("got %q", out)
	}
}

// TestSubstituteNoArgsNoAppend: with no arguments there is nothing to append.
func TestSubstituteNoArgsNoAppend(t *testing.T) {
	out := Substitute("Just run it.", "", nil)
	if out != "Just run it." {
		t.Errorf("got %q", out)
	}
}
