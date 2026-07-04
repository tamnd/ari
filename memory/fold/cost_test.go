package fold

import (
	"context"
	"testing"
)

// This is one of the four M2 ship-gate fixtures (spec 2085 slice 12): the
// folding-cost gate. It meters what a fold spends on the cheap tier and holds it
// under a per-candidate token budget, so the background synthesis stays cheap as
// the candidate backlog grows. The consolidator calls the model once per
// multi-member cluster, not once per candidate, so the spend scales with the
// number of distinct ideas, not the volume of notes. If a change ever made the
// fold call the model per candidate, the token spend would blow past this budget
// and fail the build.

// tokenBudgetPerCandidate is the ceiling this gate holds. A fold of N candidates
// may spend at most this many cheap-tier tokens per candidate. The real fold
// spends far less because it summarizes a whole cluster in one call; the budget
// leaves headroom for the prompt overhead a live summarizer adds without letting
// a per-candidate call slip in.
const tokenBudgetPerCandidate = 3

// TestFoldingCostStaysUnderBudget: a fold of many candidates in a handful of
// clusters spends cheap-tier tokens under the per-candidate budget.
func TestFoldingCostStaysUnderBudget(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	// Twenty-four candidates in three tight clusters of eight. A cluster costs
	// one merge call regardless of how many notes it holds.
	seeds := [][]string{
		{
			"run make gen after editing the schema definition files",
			"make gen must run after editing schema definition files",
			"after editing schema definition files run make gen",
			"editing schema definition files means running make gen",
			"run make gen once the schema definition files change",
			"schema definition files edited then run make gen",
			"make gen after the schema definition files were edited",
			"remember to run make gen after schema definition files",
		},
		{
			"the http client timeout should be thirty seconds",
			"set the http client timeout to thirty seconds",
			"http client timeout is thirty seconds not less",
			"thirty seconds is the correct http client timeout",
			"http client timeout must be thirty seconds",
			"keep the http client timeout at thirty seconds",
			"the http client timeout is set to thirty seconds",
			"thirty seconds for the http client timeout",
		},
		{
			"the writer goroutine serializes every database write",
			"every database write goes through the writer goroutine",
			"database writes are serialized by the writer goroutine",
			"the writer goroutine is the single database write path",
			"all database writes funnel through the writer goroutine",
			"serialize database writes on the writer goroutine",
			"the single writer goroutine handles every database write",
			"database write serialization lives in the writer goroutine",
		},
	}
	var n int
	files := []string{"schema.go", "http.go", "store.go"}
	for ci, cluster := range seeds {
		for i, body := range cluster {
			insert(t, s, string(rune('a'+ci))+string(rune('0'+i)), obs(ns, body, files[ci], 4))
			n++
		}
	}

	sum := &fakeSum{merge: "canonical merged note", lesson: ""}
	c := New(s, sum, nil)
	r, err := c.FoldNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if r.Candidates != n {
		t.Fatalf("candidates = %d, want %d", r.Candidates, n)
	}

	// The report's cheap-tier token count is what the ledger stamps against the
	// cheap tier. Hold it under the per-candidate budget.
	budget := tokenBudgetPerCandidate * r.Candidates
	if r.TokensCheap > budget {
		t.Fatalf("fold spent %d cheap tokens over %d candidates, budget %d (%.2f/candidate, ceiling %d)",
			r.TokensCheap, r.Candidates, budget, float64(r.TokensCheap)/float64(r.Candidates), tokenBudgetPerCandidate)
	}

	// And the spend tracks clusters, not candidates: the fold summarizes a whole
	// cluster in one call, so the call count stays well under the candidate count
	// rather than scaling one-to-one with the notes.
	if sum.count() >= r.Candidates {
		t.Fatalf("summarizer calls = %d for %d candidates, want far fewer (per cluster, not per candidate)",
			sum.count(), r.Candidates)
	}
}
