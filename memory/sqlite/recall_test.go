package sqlite

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

// mem builds an observation with the given text and importance, at a fixed
// base time so recency is deterministic in the tests.
func mem(id, label, body string, importance int, vec []float32) Memory {
	return Memory{
		ID: id, Namespace: "ant_worker", Kind: KindObservation,
		Label: label, Body: body, Importance: importance,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: TTLNormal,
		Embedding: vec, EmbedModel: modelFor(vec),
	}
}

func modelFor(vec []float32) string {
	if len(vec) == 0 {
		return ""
	}
	return "test-embed"
}

func recallIDs(rows []Memory) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.ID
	}
	return ids
}

// TestRecallRanksKeywordMatchAboveParaphrase is the identifier case from the
// DoD: a query for a token returns the row that mentions it ahead of a row
// that only paraphrases the idea, because BM25 is weighted high in the fusion.
func TestRecallRanksKeywordMatchAboveParaphrase(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	// K names the identifier; P paraphrases without the token.
	if err := s.InsertMemory(ctx, mem("K", "regen rule", "run make gen after editing schema.go", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert K: %v", err)
	}
	if err := s.InsertMemory(ctx, mem("P", "codegen note", "regenerate the sources when a definition file changes", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert P: %v", err)
	}
	got, err := s.Recall(ctx, "ant_worker", "schema.go", nil, 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 || got[0].ID != "K" {
		t.Fatalf("recall schema.go = %v, want K first", recallIDs(got))
	}
}

// TestRecallSemanticMatchWithVectors is the concept case: with vectors on, a
// query vector near a row that shares no keyword still recalls it, because the
// cosine half of the fusion carries it.
func TestRecallSemanticMatchWithVectors(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	// Two orthogonal directions. The query points along the first row's axis.
	near := []float32{1, 0, 0}
	far := []float32{0, 1, 0}
	if err := s.InsertMemory(ctx, mem("NEAR", "retry policy", "back off and retry on a 429", 5, near), nil, nil); err != nil {
		t.Fatalf("insert NEAR: %v", err)
	}
	if err := s.InsertMemory(ctx, mem("FAR", "log format", "structured logs go to stderr", 5, far), nil, nil); err != nil {
		t.Fatalf("insert FAR: %v", err)
	}
	// A query with no shared keyword, so only the vector can rank it.
	got, err := s.Recall(ctx, "ant_worker", "unrelatedquerytoken", []float32{1, 0, 0}, 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 || got[0].ID != "NEAR" {
		t.Fatalf("semantic recall = %v, want NEAR first", recallIDs(got))
	}
}

// TestRecallDegradesToFTSOnly: with a nil query vector the cosine stage is
// skipped and recall still returns a real BM25 ranking, so the FTS-only world
// works through the same call.
func TestRecallDegradesToFTSOnly(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	if err := s.InsertMemory(ctx, mem("A", "transport", "reuse the shared http transport", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.InsertMemory(ctx, mem("B", "timeout", "set a five second busy timeout", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := s.Recall(ctx, "ant_worker", "transport", nil, 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 || got[0].ID != "A" {
		t.Fatalf("fts-only recall = %v, want A first", recallIDs(got))
	}
}

// TestRecallBumpsAccessStats: a recalled row has its access_count incremented
// and accessed_at refreshed through the writer, the pheromone deposit.
func TestRecallBumpsAccessStats(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	if err := s.InsertMemory(ctx, mem("H", "hot row", "the frequently recalled note", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	for i := range 3 {
		if _, err := s.Recall(ctx, "ant_worker", "recalled note", nil, 5); err != nil {
			t.Fatalf("recall %d: %v", i, err)
		}
	}
	var count int
	var accessed int64
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT access_count, accessed_at FROM memories WHERE id = 'H'`).Scan(&count, &accessed)
	}); err != nil {
		t.Fatalf("read stats: %v", err)
	}
	if count != 3 {
		t.Fatalf("access_count = %d, want 3", count)
	}
	if accessed <= 1000 {
		t.Fatalf("accessed_at = %d, want it refreshed above the write time 1000", accessed)
	}
}

// TestRecallSkipsArchivedAndOtherNamespaces: an archived row and a row in a
// different namespace never surface, so a forgotten memory stays gone and one
// ant's recall does not leak another's.
func TestRecallSkipsArchivedAndOtherNamespaces(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	if err := s.InsertMemory(ctx, mem("LIVE", "live note", "the visible transport rule", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert live: %v", err)
	}
	if err := s.InsertMemory(ctx, mem("GONE", "gone note", "the archived transport rule", 5, nil), nil, nil); err != nil {
		t.Fatalf("insert gone: %v", err)
	}
	other := mem("OTHER", "other note", "another ant transport rule", 5, nil)
	other.Namespace = "ant_other"
	if err := s.InsertMemory(ctx, other, nil, nil); err != nil {
		t.Fatalf("insert other: %v", err)
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`UPDATE memories SET archived_at = ? WHERE id = 'GONE'`, time.Now().Unix())
		return err
	}); err != nil {
		t.Fatalf("archive: %v", err)
	}
	got, err := s.Recall(ctx, "ant_worker", "transport rule", nil, 10)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	for _, r := range got {
		if r.ID != "LIVE" {
			t.Fatalf("recall returned %q, want only LIVE", r.ID)
		}
	}
	if len(got) != 1 {
		t.Fatalf("recall = %v, want just [LIVE]", recallIDs(got))
	}
}

// TestParkPrefersRecentAmongEqualRelevance is the recency term in isolation:
// two candidates with identical relevance and importance rank by recency, the
// more recently accessed one first. It drives rankByPark directly so the FTS
// tie-break order cannot decide the outcome.
func TestParkPrefersRecentAmongEqualRelevance(t *testing.T) {
	cands := []scored{
		{m: Memory{ID: "OLD", Importance: 5}, relevance: 0.5, recencyRaw: 0.1},
		{m: Memory{ID: "NEW", Importance: 5}, relevance: 0.5, recencyRaw: 1.0},
	}
	rankByPark(cands)
	if cands[0].m.ID != "NEW" {
		t.Fatalf("rank = [%s %s], want NEW first on recency", cands[0].m.ID, cands[1].m.ID)
	}
}

// TestParkPrefersImportantAmongEqualRelevance is the importance term in
// isolation: equal relevance and recency, the higher write-time importance
// ranks first.
func TestParkPrefersImportantAmongEqualRelevance(t *testing.T) {
	cands := []scored{
		{m: Memory{ID: "LOW", Importance: 2}, relevance: 0.5, recencyRaw: 0.5},
		{m: Memory{ID: "HIGH", Importance: 9}, relevance: 0.5, recencyRaw: 0.5},
	}
	rankByPark(cands)
	if cands[0].m.ID != "HIGH" {
		t.Fatalf("rank = [%s %s], want HIGH first on importance", cands[0].m.ID, cands[1].m.ID)
	}
}
