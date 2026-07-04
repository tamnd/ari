package fold

import (
	"context"
	"testing"
)

// This is one of the four M2 ship-gate fixtures (spec 2085 slice 12): the
// cache-alignment gate. The pinned index is the head of every prompt the ant
// sends, and the prompt cache only pays off when that prefix is byte-identical
// turn after turn. The consolidator rebuilds the index only at a fold boundary,
// never mid-turn, so the prefix holds steady between folds and moves exactly once
// when a fold lands. This gate models a run of turns and asserts the prefix is
// stable between folds, changes exactly once across the fold, and the fraction of
// turns served from cache stays above the floor.

// cacheReadFloor is the minimum fraction of turns whose prompt prefix matched the
// prior turn's, so the cached-read discount covered them. Below this the cache is
// not paying off and the run is re-billing the prefix too often.
const cacheReadFloor = 0.9

// TestCacheAlignmentAcrossARun: over a run of turns with a single fold in the
// middle, the prefix is byte-identical between folds, changes exactly once at the
// fold, and the cache-read fraction clears the floor.
func TestCacheAlignmentAcrossARun(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	pin(t, s, "p1", ns, "run make gen after editing the schema", "schema.go", 8)
	pin(t, s, "p2", ns, "the http transport is shared, reuse it", "transport.go", 7)
	c := New(s, &fakeSum{merge: "m", lesson: ""}, nil)

	const turns = 30
	const addPinAt = 10 // a new pin lands here, invisible until the fold
	const foldAt = 15   // the only fold in the run

	prefixes := make([]string, turns)
	for turn := range turns {
		switch turn {
		case addPinAt:
			// A worker pins a new fact mid-run. It must not move the prefix yet.
			pin(t, s, "p3", ns, "prefer errors.Is over string matching", "errors.go", 6)
		case foldAt:
			// The one fold of the run. It rebuilds the prefix to fold in the pin.
			insert(t, s, "cand1", obs(ns, "a pending note the worker recorded", "other.go", 4))
			if _, err := c.FoldNamespace(ctx, ns); err != nil {
				t.Fatalf("fold: %v", err)
			}
		}
		p, err := c.PinnedIndex(ctx, ns)
		if err != nil {
			t.Fatalf("index turn %d: %v", turn, err)
		}
		prefixes[turn] = p
	}

	// Count the turns whose prefix changed from the prior turn. Exactly one, at
	// the fold; the mid-run pin at turn 10 must not count.
	var changes, changeAt int
	for i := 1; i < turns; i++ {
		if prefixes[i] != prefixes[i-1] {
			changes++
			changeAt = i
		}
	}
	if changes != 1 {
		t.Fatalf("prefix changed %d times, want exactly 1 (only at the fold)", changes)
	}
	if changeAt != foldAt {
		t.Fatalf("prefix changed at turn %d, want the fold at %d", changeAt, foldAt)
	}

	// The prefix must have held across the mid-run pin: turn 10 equals turn 9.
	if prefixes[addPinAt] != prefixes[addPinAt-1] {
		t.Fatal("the mid-run pin moved the prefix; it must wait for the fold")
	}

	// Cache-read fraction: turns whose prefix matched the prior turn, over the
	// turns that had a prior turn. One fold in thirty turns must clear the floor.
	hits := (turns - 1) - changes
	frac := float64(hits) / float64(turns-1)
	if frac <= cacheReadFloor {
		t.Fatalf("cache-read fraction = %.3f over %d turns, want > %.2f", frac, turns, cacheReadFloor)
	}
}
