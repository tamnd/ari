package fold

import (
	"context"
	"database/sql"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// This is one of the four M2 ship-gate fixtures (spec 2085 slice 12): the
// poisoning gate. It pins down two defenses against a bad memory gaining weight
// through the back door.
//
// First, a wrong belief a worker proposes lands as a pending candidate, not a
// live row, so it cannot surface in recall until the consolidator has weighed
// and folded it. A single stray remember call never poisons the next turn.
//
// Second, repeating the same wrong claim does not inflate its weight. The fold
// clusters the repeats into one row whose importance is the strongest single
// note, never the sum, so an attacker cannot lift a claim's rank by saying it
// ten times.

// TestPoisoningCandidateNotRecallableUntilFolded: a wrong-belief candidate is
// invisible to recall while it is pending and only becomes recallable once the
// fold has folded it into a live row.
func TestPoisoningCandidateNotRecallableUntilFolded(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	insert(t, s, "bad", obs(ns, "the deploy script force pushes to the main branch", "deploy.sh", 5))

	// Pending: recall must not see it.
	got, err := s.Recall(ctx, ns, "deploy script force pushes main branch", nil, 10)
	if err != nil {
		t.Fatalf("recall before fold: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a pending candidate surfaced in recall before the fold: %v", ids(got))
	}

	// Fold it in, then it is a live memory and recallable.
	c := New(s, &fakeSum{merge: "m", lesson: ""}, nil)
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}
	got, err = s.Recall(ctx, ns, "deploy script force pushes main branch", nil, 10)
	if err != nil {
		t.Fatalf("recall after fold: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("the folded belief is not recallable after the fold")
	}
}

// TestPoisoningRepetitionDoesNotInflateWeight: the same wrong claim recorded ten
// times folds to a single row at the strongest single importance, not ten rows
// and not a summed importance, so repetition buys no rank.
func TestPoisoningRepetitionDoesNotInflateWeight(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	// Ten near-duplicate rewordings of one wrong claim, each importance 5.
	claims := []string{
		"skip the code review on a friday afternoon deploy",
		"friday afternoon deploys can skip the code review",
		"code review is skippable for a friday afternoon deploy",
		"on a friday afternoon deploy just skip the code review",
		"skip code review when deploying on a friday afternoon",
		"a friday afternoon deploy does not need the code review",
		"the code review is skipped on friday afternoon deploys",
		"friday afternoon means skip the deploy code review",
		"you can skip a code review for the friday afternoon deploy",
		"deploying friday afternoon lets you skip the code review",
	}
	for i, body := range claims {
		insert(t, s, "w"+string(rune('0'+i)), obs(ns, body, "deploy.sh", 5))
	}

	c := New(s, &fakeSum{merge: "canonical merged claim", lesson: ""}, nil)
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}

	// The ten repeats collapsed to one row.
	if got := countMemories(t, s, ns, sqlite.KindObservation); got != 1 {
		t.Fatalf("observation rows = %d, want 1 (repetition must dedupe to one)", got)
	}
	// Its importance is the strongest single note, not the sum of the repeats.
	var imp int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT importance FROM memories WHERE namespace = ?`, ns).Scan(&imp)
	}); err != nil {
		t.Fatalf("importance: %v", err)
	}
	if imp != 5 {
		t.Fatalf("merged importance = %d, want 5 (repetition must not inflate past the strongest single note)", imp)
	}
}

// ids is a small recall-result helper for the failure messages.
func ids(rows []sqlite.Memory) []string {
	out := make([]string, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
