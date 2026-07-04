package sqlite

import (
	"context"
	"testing"
)

func seedRow(t *testing.T, s *Store, m Memory, anchors []Anchor) {
	t.Helper()
	if err := s.InsertMemory(context.Background(), m, anchors, nil); err != nil {
		t.Fatalf("insert %s: %v", m.ID, err)
	}
}

func baseRow(id, body string) Memory {
	return Memory{
		ID: id, Namespace: "worker/main", Kind: KindObservation,
		Label: body, Body: body, Importance: 5,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: TTLNormal,
	}
}

// TestExportRowsOrderedWithAnchors: export returns a namespace's live rows in a
// stable order with their anchors attached, and leaves archived rows out.
func TestExportRowsOrderedWithAnchors(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	a := baseRow("01a", "first")
	a.CreatedAt = 1000
	b := baseRow("01b", "second")
	b.CreatedAt = 2000
	seedRow(t, s, a, []Anchor{{Kind: "file", Ref: "a.go", FileHash: "h1"}})
	seedRow(t, s, b, nil)

	gone := baseRow("01c", "gone")
	seedRow(t, s, gone, nil)
	if _, _, err := s.ArchiveMemory(ctx, "worker/main", "01c"); err != nil {
		t.Fatalf("archive: %v", err)
	}

	rows, err := s.ExportRows(ctx, "worker/main")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("exported %d rows, want 2 (archived excluded)", len(rows))
	}
	if rows[0].ID != "01a" || rows[1].ID != "01b" {
		t.Fatalf("order = %s,%s, want 01a,01b by created_at", rows[0].ID, rows[1].ID)
	}
	if len(rows[0].Anchors) != 1 || rows[0].Anchors[0].Ref != "a.go" || rows[0].Anchors[0].FileHash != "h1" {
		t.Fatalf("row 0 anchors = %+v, want a.go@h1", rows[0].Anchors)
	}
	if len(rows[1].Anchors) != 0 {
		t.Fatalf("row 1 anchors = %+v, want none", rows[1].Anchors)
	}
}

// TestUpdateMemoryTextMarksReadOnly: an edited body updates the row and flips
// read_only, and reports whether it matched a live row.
func TestUpdateMemoryTextMarksReadOnly(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	seedRow(t, s, baseRow("01a", "before"), nil)

	ok, err := s.UpdateMemoryText(ctx, "worker/main", "01a", "after", "after")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !ok {
		t.Fatal("update reported no match for a live row")
	}
	rows, err := s.ExportRows(ctx, "worker/main")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if rows[0].Body != "after" || !rows[0].ReadOnly {
		t.Fatalf("row = %+v, want body 'after' and read_only", rows[0])
	}

	miss, err := s.UpdateMemoryText(ctx, "worker/main", "nope", "x", "x")
	if err != nil {
		t.Fatalf("update miss: %v", err)
	}
	if miss {
		t.Fatal("update reported a match for an id that names nothing")
	}
}

// TestExportMarksHumanAndVerified: the export flags carry through, so a
// developer reading the file sees which rows a human pinned and which the
// machine confirmed.
func TestExportMarksHumanAndVerified(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	seedRow(t, s, baseRow("01a", "human row"), nil)
	if _, err := s.UpdateMemoryText(ctx, "worker/main", "01a", "human row", "human row"); err != nil {
		t.Fatalf("update: %v", err)
	}
	seedRow(t, s, baseRow("01b", "verified row"), nil)
	if err := s.SetStale(ctx, "01b", false, "deadbeef"); err != nil {
		t.Fatalf("verify: %v", err)
	}

	rows, err := s.ExportRows(ctx, "worker/main")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	byID := map[string]ExportRow{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	if !byID["01a"].ReadOnly {
		t.Error("01a should be read_only after an edit")
	}
	if !byID["01b"].Verified {
		t.Error("01b should be verified after a verified_at stamp")
	}
}
