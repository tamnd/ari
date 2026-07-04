package fold

import (
	"context"
	"database/sql"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// readOnlyRow returns the body and read_only flag of one memory, so a test can
// prove a fold left a human-edited row exactly as written.
func readOnlyRow(t *testing.T, s *sqlite.Store, id string) (string, bool) {
	t.Helper()
	var body string
	var ro int
	if err := s.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(`SELECT body, read_only FROM memories WHERE id = ?`, id).Scan(&body, &ro)
	}); err != nil {
		t.Fatalf("read row %s: %v", id, err)
	}
	return body, ro == 1
}

// TestFoldLeavesReadOnlyRow: a human-edited (read_only) row anchored to a file
// stands untouched through a fold that ingests a candidate contradicting it. The
// consolidator only appends candidate-derived rows and demotes non-read_only
// rows, so the human's word outranks the machine's (D11).
func TestFoldLeavesReadOnlyRow(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "worker/main"

	// A live, human-edited row: read_only, anchored to a file.
	human := "the timeout is 30 seconds, do not change it"
	m := sqlite.Memory{
		ID: "HR", Namespace: ns, Kind: sqlite.KindObservation,
		Label: human, Body: human, Importance: 8,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "human", TTLClass: sqlite.TTLNormal,
	}
	if err := s.InsertMemory(ctx, m, []sqlite.Anchor{{Kind: "file", Ref: "config.go"}}, nil); err != nil {
		t.Fatalf("seed human row: %v", err)
	}
	if _, err := s.UpdateMemoryText(ctx, ns, "HR", human, human); err != nil {
		t.Fatalf("mark read_only: %v", err)
	}

	// A candidate that contradicts it, anchored to the same file.
	insert(t, s, "cand", obs(ns, "the timeout should be 5 seconds", "config.go", 6))

	c := New(s, &fakeSum{}, nil)
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}

	body, ro := readOnlyRow(t, s, "HR")
	if body != human {
		t.Fatalf("read_only row body = %q, want it unchanged", body)
	}
	if !ro {
		t.Fatal("read_only flag was cleared by the fold")
	}
	// The contradicting candidate landed as its own new row, not a rewrite.
	if got := countMemories(t, s, ns, "observation"); got != 2 {
		t.Fatalf("observation rows = %d, want 2 (the human row and the new candidate)", got)
	}
}
