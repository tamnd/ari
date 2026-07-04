package fold

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// pin writes a pinned memory, the input to the pinned index.
func pin(t *testing.T, s *sqlite.Store, id, ns, body, file string, imp int) {
	t.Helper()
	m := sqlite.Memory{
		ID: id, Namespace: ns, Kind: sqlite.KindObservation,
		Label: body, Body: body, Importance: imp,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker",
		TTLClass: sqlite.TTLPinned, Pinned: true,
	}
	anchors := []sqlite.Anchor{{Kind: "file", Ref: file}}
	if err := s.InsertMemory(context.Background(), m, anchors, nil); err != nil {
		t.Fatalf("pin %s: %v", id, err)
	}
}

// TestPinnedIndexStableBetweenFolds is the cache-alignment guarantee: the index
// is byte-identical across turns with no fold between them, a new pin does not
// change it until a fold, and a fold rebuilds it exactly once. This is what
// keeps the prompt prefix stable so the cached-read discount holds (D14).
func TestPinnedIndexStableBetweenFolds(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	pin(t, s, "p1", ns, "run make gen after editing schema", "schema.go", 8)
	pin(t, s, "p2", ns, "the http transport is shared, reuse it", "transport.go", 7)
	c := New(s, &fakeSum{}, nil)

	idx1, err := c.PinnedIndex(ctx, ns)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	idx2, _ := c.PinnedIndex(ctx, ns)
	idx3, _ := c.PinnedIndex(ctx, ns)
	if idx1 != idx2 || idx2 != idx3 {
		t.Fatal("index is not byte-identical across three turns with no fold")
	}
	if idx1 == "" {
		t.Fatal("expected the two pins rendered")
	}

	// A new pin lands in the store, but the cache stays put until a fold.
	pin(t, s, "p3", ns, "prefer errors.Is over string matching", "errors.go", 6)
	if got, _ := c.PinnedIndex(ctx, ns); got != idx1 {
		t.Fatal("index changed on a turn; the prefix must rebuild only at a fold boundary")
	}

	// A fold rebuilds the index once; now the new pin shows.
	insert(t, s, "cand1", obs(ns, "a pending note the worker recorded", "other.go", 4))
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}
	after, _ := c.PinnedIndex(ctx, ns)
	if after == idx1 {
		t.Fatal("fold did not rebuild the index")
	}
	if !strings.Contains(after, "errors.go") {
		t.Fatalf("rebuilt index missing the new pin:\n%s", after)
	}

	// Stable again between folds.
	if a2, _ := c.PinnedIndex(ctx, ns); a2 != after {
		t.Fatal("index is not stable after the fold rebuilt it")
	}
}

// TestPinnedIndexEmptyNamespace: a namespace with no pins renders the empty
// string, and the assembler shows its own "no pins" wording.
func TestPinnedIndexEmptyNamespace(t *testing.T) {
	s := store(t)
	c := New(s, &fakeSum{}, nil)
	got, err := c.PinnedIndex(context.Background(), "ant_worker")
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	if got != "" {
		t.Fatalf("empty namespace index = %q, want \"\"", got)
	}
}

// TestPinnedIndexArchivedPinDropsOut: a pin that forget archived leaves the
// index at the next fold, so a retired pin never lingers in the prefix.
func TestPinnedIndexArchivedPinDropsOut(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	pin(t, s, "p1", ns, "keep this pin", "keep.go", 8)
	pin(t, s, "p2", ns, "archive this pin", "drop.go", 7)
	c := New(s, &fakeSum{}, nil)

	before, _ := c.PinnedIndex(ctx, ns)
	if !strings.Contains(before, "drop.go") {
		t.Fatalf("setup: index missing the pin to archive:\n%s", before)
	}

	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE memories SET archived_at = 1 WHERE id = 'p2'`)
		return err
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	insert(t, s, "cand1", obs(ns, "a pending note to drive the fold", "x.go", 4))
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}
	after, _ := c.PinnedIndex(ctx, ns)
	if strings.Contains(after, "drop.go") {
		t.Fatalf("archived pin still in index:\n%s", after)
	}
	if !strings.Contains(after, "keep.go") {
		t.Fatalf("kept pin missing from index:\n%s", after)
	}
}
