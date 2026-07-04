package skill

import (
	"strings"
	"testing"
)

// est is the standard prompt estimator, so the tests measure the listing on
// the same scale the budget uses.
func est(s string) int { return (len(s) + 3) / 4 }

func mk(name, desc string, scope Scope) Skill {
	return Skill{Name: name, Description: desc, Scope: scope}
}

// TestRenderListFitsBudget: a fixture with many skills is cut to the budget,
// and every rendered skill is one name-plus-line entry.
func TestRenderListFitsBudget(t *testing.T) {
	var skills []Skill
	for i := range 60 {
		skills = append(skills, mk(
			"skill-"+string(rune('a'+i%26))+string(rune('a'+i/26)),
			strings.Repeat("word ", 10),
			ScopeProject,
		))
	}
	budget := Budget(50000) // 500 tokens, too small for all sixty
	block, cut := RenderList(skills, budget, est)
	if est(block) > budget {
		t.Errorf("block over budget: %d > %d", est(block), budget)
	}
	if len(cut) == 0 {
		t.Error("expected some skills cut at this budget")
	}
	// Rendered plus cut accounts for every model-visible skill.
	rendered := 0
	for _, line := range strings.Split(block, "\n") {
		if strings.HasPrefix(line, "- ") {
			rendered++
		}
	}
	if rendered+len(cut) != len(skills) {
		t.Errorf("rendered %d + cut %d != %d skills", rendered, len(cut), len(skills))
	}
}

// TestRenderListRanksProjectFirst: when the budget forces a choice, the
// project scope lists ahead of user and builtin.
func TestRenderListRanksProjectFirst(t *testing.T) {
	skills := []Skill{
		mk("zebra", "builtin one", ScopeBuiltin),
		mk("alpha", "user one", ScopeUser),
		mk("mango", "project one", ScopeProject),
	}
	block, _ := RenderList(skills, 4000, est)
	pi := strings.Index(block, "mango")
	ui := strings.Index(block, "alpha")
	bi := strings.Index(block, "zebra")
	if pi >= ui || ui >= bi {
		t.Errorf("ranking wrong: project=%d user=%d builtin=%d\n%s", pi, ui, bi, block)
	}
}

// TestModelHiddenExcluded: a disable-model-invocation skill never appears in
// the model listing.
func TestModelHiddenExcluded(t *testing.T) {
	skills := []Skill{
		mk("visible", "shown", ScopeProject),
		{Name: "secret", Description: "hidden", Scope: ScopeProject, ModelHidden: true},
	}
	block, cut := RenderList(skills, 4000, est)
	if strings.Contains(block, "secret") {
		t.Error("model-hidden skill leaked into the listing")
	}
	for _, c := range cut {
		if c == "secret" {
			t.Error("model-hidden skill reported as cut, should be absent entirely")
		}
	}
	if !strings.Contains(block, "visible") {
		t.Error("visible skill missing")
	}
}

// TestBudgetCapAndFloor: the budget is one percent of the window, capped and
// floored.
func TestBudgetCapAndFloor(t *testing.T) {
	if got := Budget(1000000); got != budgetCap {
		t.Errorf("cap not applied: %d", got)
	}
	if got := Budget(120000); got != 1200 {
		t.Errorf("one percent wrong: %d", got)
	}
	if got := Budget(1000); got != 64 {
		t.Errorf("floor not applied: %d", got)
	}
}

// TestBodiesAbsentFromListing: the listing carries names and descriptions
// only, never a body.
func TestBodiesAbsentFromListing(t *testing.T) {
	skills := []Skill{mk("x", "one line", ScopeProject)}
	block, _ := RenderList(skills, 4000, est)
	if strings.Contains(block, "body") {
		t.Errorf("body leaked into listing:\n%s", block)
	}
}
