package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

// ftsMatch returns the ids of memory rows whose FTS index matches query,
// which is how the recall path (slice 4) will read memories_fts.
func ftsMatch(t *testing.T, s *Store, query string) []string {
	t.Helper()
	var ids []string
	err := s.Read(context.Background(), func(db *sql.DB) error {
		// Quote the query as a phrase so a token with a dot or slash (like
		// schema.go) is matched literally rather than parsed as FTS5 column
		// syntax; slice 4's recall builds queries the same guarded way.
		rows, err := db.Query(`
			SELECT m.id FROM memories_fts f
			JOIN memories m ON m.rowid = f.rowid
			WHERE memories_fts MATCH ? ORDER BY m.id`, `"`+query+`"`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("fts match %q: %v", query, err)
	}
	return ids
}

func obs(id, label, body string) Memory {
	return Memory{
		ID: id, Namespace: "ant_worker", Kind: KindObservation,
		Label: label, Body: body, Importance: 5,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: TTLFast,
	}
}

// TestFTSStaysInSyncOnInsertUpdateDelete is the trigger round trip: an
// inserted row is findable, an update moves what matches, and a delete drops
// it from the index, so external-content FTS5 tracks the base table.
func TestFTSStaysInSyncOnInsertUpdateDelete(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()

	if err := s.InsertMemory(ctx, obs("01A", "generator rule", "run make gen after editing schema.go"), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.InsertMemory(ctx, obs("01B", "http client", "reuse the shared transport for outbound calls"), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}

	if got := ftsMatch(t, s, "schema.go"); len(got) != 1 || got[0] != "01A" {
		t.Fatalf("after insert, match schema.go = %v, want [01A]", got)
	}

	// Update 01A so it no longer mentions schema.go but does mention gopls.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE memories SET body = ? WHERE id = ?`, "run gopls after a rename", "01A")
		return err
	}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got := ftsMatch(t, s, "schema.go"); len(got) != 0 {
		t.Fatalf("after update, match schema.go = %v, want none", got)
	}
	if got := ftsMatch(t, s, "gopls"); len(got) != 1 || got[0] != "01A" {
		t.Fatalf("after update, match gopls = %v, want [01A]", got)
	}

	// Delete 01B and confirm it leaves the index.
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`DELETE FROM memories WHERE id = ?`, "01B")
		return err
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if got := ftsMatch(t, s, "transport"); len(got) != 0 {
		t.Fatalf("after delete, match transport = %v, want none", got)
	}
}

// TestReflectionWithoutEvidenceIsRefused is the D11 guard at the store: a
// reflection with no evidence edge fails before any write, with a
// model-facing reason.
func TestReflectionWithoutEvidenceIsRefused(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	ref := Memory{
		ID: "R1", Namespace: "ant_worker", Kind: KindReflection,
		Label: "never hand-edit gen", Body: "gen/*.go is overwritten by make gen",
		Importance: 8, CreatedAt: 2000, AccessedAt: 2000, SourceAnt: "worker", TTLClass: TTLNormal,
	}
	err := s.InsertMemory(ctx, ref, nil, nil)
	if err == nil {
		t.Fatal("reflection with no evidence was accepted, want refusal")
	}
	// Nothing partial should have landed.
	var count int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM memories WHERE id = 'R1'`).Scan(&count)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("refused reflection left a row: count = %d", count)
	}
}

// TestReflectionWithEvidenceLands is the accepting path: a reflection that
// names the observation it rests on is written with its evidence edge.
func TestReflectionWithEvidenceLands(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	if err := s.InsertMemory(ctx, obs("O1", "gen fail", "hand edit to gen/model.go was lost"), nil, nil); err != nil {
		t.Fatalf("insert observation: %v", err)
	}
	ref := Memory{
		ID: "R2", Namespace: "ant_worker", Kind: KindReflection,
		Label: "never hand-edit gen", Body: "run make gen after editing schema.go",
		Importance: 8, CreatedAt: 2000, AccessedAt: 2000, SourceAnt: "worker", TTLClass: TTLNormal,
	}
	anchors := []Anchor{{Kind: "file", Ref: "schema.go", FileHash: "9c2e1a4"}}
	if err := s.InsertMemory(ctx, ref, anchors, []string{"O1"}); err != nil {
		t.Fatalf("insert reflection: %v", err)
	}
	var edges int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM memory_evidence WHERE memory_id = 'R2' AND evidence_id = 'O1'`).Scan(&edges)
	}); err != nil {
		t.Fatalf("count edges: %v", err)
	}
	if edges != 1 {
		t.Fatalf("evidence edges = %d, want 1", edges)
	}
	var anchorRefs int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM memory_anchor WHERE memory_id = 'R2'`).Scan(&anchorRefs)
	}); err != nil {
		t.Fatalf("count anchors: %v", err)
	}
	if anchorRefs != 1 {
		t.Fatalf("anchors = %d, want 1", anchorRefs)
	}
}

// TestEmbeddingRoundTrips confirms a float32 vector survives the BLOB
// encode on write and decodes back to the same numbers, the on-disk form
// slice 4's cosine search reads.
func TestEmbeddingRoundTrips(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	m := obs("E1", "vec", "has an embedding")
	m.Embedding = []float32{0.5, -0.25, 1.0, 0}
	m.EmbedModel = "test-embed-v1"
	if err := s.InsertMemory(ctx, m, nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	var blob []byte
	var model string
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT embedding, embed_model FROM memories WHERE id = 'E1'`).Scan(&blob, &model)
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if model != "test-embed-v1" {
		t.Fatalf("embed_model = %q, want test-embed-v1", model)
	}
	want := encodeVector(m.Embedding)
	if len(blob) != len(want) {
		t.Fatalf("blob len = %d, want %d", len(blob), len(want))
	}
	for i := range want {
		if blob[i] != want[i] {
			t.Fatalf("blob byte %d = %d, want %d", i, blob[i], want[i])
		}
	}
}
