package memory

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
	"github.com/tamnd/ari/tool"
)

func tctx() *tool.ToolContext {
	return &tool.ToolContext{Ant: "worker", Namespace: "ant_worker"}
}

func countRows(t *testing.T, s *sqlite.Store, table string) int {
	t.Helper()
	var n int
	if err := s.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&n)
	}); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return n
}

// TestRememberQueuesNeverWritesLive: remember appends a candidate and does not
// touch live memory (D12). The memories table stays empty; the candidate table
// gains one row.
func TestRememberQueuesNeverWritesLive(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	rt := NewRemember(s)
	args := json.RawMessage(`{"body":"run make gen after editing schema","importance":6,"anchors":[{"kind":"file","ref":"schema.go"}]}`)

	if err := rt.ValidateInput(ctx, args, tctx()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	res, err := rt.Call(ctx, args, tctx(), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Model, "queued") {
		t.Fatalf("result = %q, want a queued confirmation, not stored", res.Model)
	}
	if got := countRows(t, s, "memories"); got != 0 {
		t.Fatalf("live memories = %d, want 0: remember must not write live memory", got)
	}
	if got := countRows(t, s, "memory_candidates"); got != 1 {
		t.Fatalf("candidates = %d, want 1", got)
	}
	if rt.IsReadOnly(args) {
		t.Fatal("remember must not be read-only")
	}
}

// TestRememberRefusesReflectionWithoutEvidence: the D11 rule at the tool
// boundary, the first of three enforcement points.
func TestRememberRefusesReflectionWithoutEvidence(t *testing.T) {
	s := store(t)
	rt := NewRemember(s)
	args := json.RawMessage(`{"body":"a lesson","importance":7,"kind":"reflection","anchors":[{"kind":"file","ref":"a.go"}]}`)
	err := rt.ValidateInput(context.Background(), args, tctx())
	if err == nil || !strings.Contains(err.Error(), "evidence") {
		t.Fatalf("validate err = %v, want a reflection-needs-evidence refusal", err)
	}
}

// TestRememberRefusesMissingAnchor: a memory with no anchor cannot be found or
// invalidated, so remember refuses it.
func TestRememberRefusesMissingAnchor(t *testing.T) {
	s := store(t)
	rt := NewRemember(s)
	args := json.RawMessage(`{"body":"floating note","importance":5,"anchors":[]}`)
	if err := rt.ValidateInput(context.Background(), args, tctx()); err == nil {
		t.Fatal("validate should refuse a memory with no anchor")
	}
}

// TestRecallReturnsRankedAndBumpsAccess: recall returns the matching row shaped
// for the model and bumps its access count, the pheromone deposit.
func TestRecallReturnsRankedAndBumpsAccess(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	m := sqlite.Memory{
		ID: "M1", Namespace: "ant_worker", Kind: sqlite.KindObservation,
		Label: "regen", Body: "run make gen after editing schema.go", Importance: 6,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: sqlite.TTLNormal,
	}
	if err := s.InsertMemory(ctx, m, []sqlite.Anchor{{Kind: "file", Ref: "schema.go"}}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	rt := NewRecall(s, NullEmbedder{})
	args := json.RawMessage(`{"query":"make gen schema"}`)
	if err := rt.ValidateInput(ctx, args, tctx()); err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !rt.IsReadOnly(args) || !rt.IsConcurrencySafe(args) {
		t.Fatal("recall must be read-only and concurrency-safe")
	}
	res, err := rt.Call(ctx, args, tctx(), nil)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	if !strings.Contains(res.Model, "make gen") || !strings.Contains(res.Model, "[fresh]") {
		t.Fatalf("recall result = %q, want the row with a fresh marker", res.Model)
	}

	var count int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT access_count FROM memories WHERE id='M1'`).Scan(&count)
	}); err != nil {
		t.Fatalf("read access_count: %v", err)
	}
	if count < 1 {
		t.Fatalf("access_count = %d, want >= 1: recall must bump access stats", count)
	}
}

// TestForgetArchivesAndLeavesRecall: forget archives a row so it drops from
// recall and the pinned index but stays in the file (D11, D13).
func TestForgetArchivesAndLeavesRecall(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	m := sqlite.Memory{
		ID: "M1", Namespace: "ant_worker", Kind: sqlite.KindObservation,
		Label: "the transport rule", Body: "reuse the shared http transport", Importance: 8,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: sqlite.TTLPinned, Pinned: true,
	}
	if err := s.InsertMemory(ctx, m, []sqlite.Anchor{{Kind: "file", Ref: "transport.go"}}, nil); err != nil {
		t.Fatalf("seed: %v", err)
	}

	ft := NewForget(s)
	if ft.IsDestructive(nil) {
		t.Fatal("forget is archival, not destructive")
	}
	args := json.RawMessage(`{"id":"M1"}`)

	// It renders the row it would archive to the permission pipeline.
	perm := ft.CheckPermissions(ctx, args, tctx())
	if !perm.IsAsk() || !strings.Contains(perm.Message(), "transport rule") {
		t.Fatalf("permission = %+v, want an ask naming the row", perm)
	}

	if _, err := ft.Call(ctx, args, tctx(), nil); err != nil {
		t.Fatalf("call: %v", err)
	}

	// Gone from recall.
	rows, err := s.Recall(ctx, "ant_worker", "transport", nil, 5)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("recall after forget returned %d rows, want 0", len(rows))
	}
	// Gone from the pinned index.
	pins, err := s.PinnedRows(ctx, "ant_worker")
	if err != nil {
		t.Fatalf("pins: %v", err)
	}
	if len(pins) != 0 {
		t.Fatalf("pinned rows after forget = %d, want 0", len(pins))
	}
	// Still in the file: the row exists, archived.
	if got := countRows(t, s, "memories"); got != 1 {
		t.Fatalf("memories table = %d, want 1: forget archives, never deletes", got)
	}
}

// TestForgetUnknownIDReports: forgetting an id that names nothing live is
// reported, not a silent success.
func TestForgetUnknownIDReports(t *testing.T) {
	s := store(t)
	ft := NewForget(s)
	_, err := ft.Call(context.Background(), json.RawMessage(`{"id":"nope"}`), tctx(), nil)
	if err == nil || !strings.Contains(err.Error(), "nothing to archive") {
		t.Fatalf("call err = %v, want a nothing-to-archive report", err)
	}
}

// TestMemoryToolsRefuseWithoutNamespace: every memory tool fails closed when no
// namespace is bound to the session.
func TestMemoryToolsRefuseWithoutNamespace(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	tc := &tool.ToolContext{Ant: "worker"} // no Namespace
	for _, tt := range []struct {
		name string
		tl   tool.Tool
		args string
	}{
		{"remember", NewRemember(s), `{"body":"x","importance":5,"anchors":[{"kind":"file","ref":"a.go"}]}`},
		{"recall", NewRecall(s, NullEmbedder{}), `{"query":"x"}`},
		{"forget", NewForget(s), `{"id":"x"}`},
	} {
		if err := tt.tl.ValidateInput(ctx, json.RawMessage(tt.args), tc); err == nil {
			t.Errorf("%s: want a refusal with no namespace bound", tt.name)
		}
	}
}
