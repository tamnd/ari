package skill

import (
	"sort"
	"strings"
)

// budgetCap is the hard ceiling on the skills listing regardless of window
// size: a big context window is not a licence to spend it all on a skill
// directory. One percent of the window, capped here, is the whole footprint
// (doc 13 section 2.5).
const budgetCap = 2000

// Estimator turns a string into a token estimate. The caller passes the same
// (len+3)/4 estimator the prompt builder uses, so the listing is measured in
// the same unit as the budget it must fit (doc 07).
type Estimator func(string) int

// Budget is the token allowance for the skills listing: one percent of the
// context window, never above budgetCap. A tiny window still gets a floor of
// one line's worth so a single skill can always announce itself.
func Budget(contextWindow int) int {
	b := contextWindow / 100
	if b > budgetCap {
		return budgetCap
	}
	if b < 64 {
		return 64
	}
	return b
}

// RenderList builds the block two skills listing: one line per skill, name
// then description, ranked so the closest scope wins a tie and the whole
// thing fits the budget. The caller owns the "## Skills" heading, so this
// returns only the entry lines. Skills that do not fit are dropped and
// returned in cut, so the caller can note the elision rather than pretend
// everything listed (doc 13 section 2.5). ModelHidden skills never appear
// here: they are user-invocable only and must not tempt the model.
//
// est measures the running block so the cut lands on the same token scale as
// the budget. A nil est falls back to the standard (len+3)/4 estimate.
func RenderList(skills []Skill, budget int, est Estimator) (block string, cut []string) {
	if est == nil {
		est = func(s string) int { return (len(s) + 3) / 4 }
	}
	ranked := rankForListing(skills)

	var b strings.Builder
	for _, s := range ranked {
		line := "- " + s.Name
		if s.Description != "" {
			line += ": " + s.Description
		}
		line += "\n"
		if est(b.String()+line) > budget {
			cut = append(cut, s.Name)
			continue
		}
		b.WriteString(line)
	}
	return strings.TrimRight(b.String(), "\n"), cut
}

// rankForListing orders skills for the prompt: model-visible only, closest
// scope first (project over user over builtin), then by name for stability.
// The scope rank is what makes a repo's own skill outrank a namesake from the
// user nest when the budget forces a choice.
func rankForListing(skills []Skill) []Skill {
	ranked := make([]Skill, 0, len(skills))
	for _, s := range skills {
		if s.ModelHidden {
			continue // disable-model-invocation: absent from the model listing
		}
		ranked = append(ranked, s)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		ri, rj := scopeRank(ranked[i].Scope), scopeRank(ranked[j].Scope)
		if ri != rj {
			return ri < rj
		}
		return ranked[i].Name < ranked[j].Name
	})
	return ranked
}

// scopeRank orders the scopes for listing priority, lowest first.
func scopeRank(s Scope) int {
	switch s {
	case ScopeProject:
		return 0
	case ScopeUser:
		return 1
	default:
		return 2
	}
}
