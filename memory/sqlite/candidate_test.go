package sqlite

import (
	"context"
	"database/sql"
	"testing"
)

func cand(ns, kind, body string, anchors []Anchor, evidence []string) Candidate {
	return Candidate{
		Namespace: ns, Kind: kind, Body: body, Importance: 5,
		Anchors: anchors, Evidence: evidence,
		Source: Source{Ant: "worker", Task: "t1", Commit: "9c2e1a4"},
	}
}

// TestInsertCandidateLandsPending: a candidate with an anchor writes a pending
// row with its anchor edge, the row the consolidator will later fold.
func TestInsertCandidateLandsPending(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	c := cand("ant_worker", KindObservation, "make gen must run after schema.go changes",
		[]Anchor{{Kind: "file", Ref: "schema.go", FileHash: "9c2e1a4"}}, nil)
	if err := s.InsertCandidate(ctx, "C1", c); err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	cands, ids, err := s.PendingCandidates(ctx, "ant_worker", 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(ids) != 1 || ids[0] != "C1" {
		t.Fatalf("pending ids = %v, want [C1]", ids)
	}
	if cands[0].Body != c.Body {
		t.Fatalf("pending body = %q, want %q", cands[0].Body, c.Body)
	}
	var anchors int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM candidate_anchor WHERE candidate_id = 'C1'`).Scan(&anchors)
	}); err != nil {
		t.Fatalf("count anchors: %v", err)
	}
	if anchors != 1 {
		t.Fatalf("candidate anchors = %d, want 1", anchors)
	}
}

// TestReflectionCandidateWithoutEvidenceIsRefused: the D11 guard at the
// candidate write, refusing a reflection with no evidence and leaving no row.
func TestReflectionCandidateWithoutEvidenceIsRefused(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	c := cand("ant_worker", KindReflection, "never hand-edit generated code",
		[]Anchor{{Kind: "file", Ref: "gen/model.go"}}, nil)
	if err := s.InsertCandidate(ctx, "R1", c); err == nil {
		t.Fatal("reflection candidate with no evidence was accepted, want refusal")
	}
	_, ids, err := s.PendingCandidates(ctx, "ant_worker", 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("refused candidate left rows: %v", ids)
	}
}

// TestRecallNeverReturnsCandidates: a pending candidate is not a live memory,
// so recall cannot surface it. This is the poisoning defense: nothing an ant
// proposes reaches recall without passing through a fold.
func TestRecallNeverReturnsCandidates(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	c := cand("ant_worker", KindObservation, "the pending transport rule",
		[]Anchor{{Kind: "file", Ref: "http.go"}}, nil)
	if err := s.InsertCandidate(ctx, "C2", c); err != nil {
		t.Fatalf("insert candidate: %v", err)
	}
	got, err := s.Recall(ctx, "ant_worker", "transport rule", nil, 10)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("recall surfaced a pending candidate: %d rows", len(got))
	}
}
