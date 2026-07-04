package ant

import (
	"testing"

	"github.com/tamnd/ari/skill"
	"github.com/tamnd/ari/tool"
)

// TestModelVisibleSkill: the skill tool is offered only when at least one
// discovered skill is reachable by the model.
func TestModelVisibleSkill(t *testing.T) {
	if modelVisibleSkill(nil) {
		t.Error("empty set should not be model-visible")
	}
	hidden := []skill.Skill{{Name: "a", ModelHidden: true}}
	if modelVisibleSkill(hidden) {
		t.Error("a set of only hidden skills should not be model-visible")
	}
	mixed := []skill.Skill{{Name: "a", ModelHidden: true}, {Name: "b"}}
	if !modelVisibleSkill(mixed) {
		t.Error("a set with one visible skill should be model-visible")
	}
}

// TestAllowedToolsMatcherNormalizes proves gate one uses the same doc 05 sh
// normalization the pipeline does: a narrow allowance covers its own commands
// but cannot be widened by a compound command smuggling a second one past it.
func TestAllowedToolsMatcherNormalizes(t *testing.T) {
	w := &worker{}
	match := w.allowedToolsMatcher(tool.NewSh(), []string{"sh(git *)"})

	if !match("git status") {
		t.Error("git status should match sh(git *)")
	}
	if match("rm -rf /") {
		t.Error("an unrelated command must not match")
	}
	if match("git status && rm -rf /") {
		t.Error("a compound command must not slip a second command past the allowance")
	}
}

// TestAllowedToolsMatcherNoRulesDeniesAll: a skill with no allowed-tools rules
// matches nothing, the safe default.
func TestAllowedToolsMatcherNoRulesDeniesAll(t *testing.T) {
	w := &worker{}
	match := w.allowedToolsMatcher(tool.NewSh(), nil)
	if match("git status") {
		t.Error("no rules should deny every command")
	}
}
