package fold

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// fakeRepo is a scripted working tree. It answers the three questions the
// invalidation pass asks from maps a test sets up, and counts ChangedSince
// calls so a test can prove the diff runs once per commit, not once per row.
type fakeRepo struct {
	head    string
	changed map[string]map[string]bool // anchor_commit -> files changed since it
	hashes  map[string]string          // path -> current hash, absent means gone

	mu           sync.Mutex
	changedCalls int
}

func (r *fakeRepo) Head() (string, bool) { return r.head, r.head != "" }

func (r *fakeRepo) ChangedSince(commit string) (map[string]bool, bool) {
	r.mu.Lock()
	r.changedCalls++
	r.mu.Unlock()
	f, ok := r.changed[commit]
	return f, ok
}

func (r *fakeRepo) HashFile(path string) (string, bool) {
	h, ok := r.hashes[path]
	return h, ok
}

func (r *fakeRepo) calls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.changedCalls
}

// live writes one live memory row anchored to a file, the state the
// invalidation pass reads. hash is the content hash the anchor stored at write
// time, commit the point it was true at.
func live(t *testing.T, s *sqlite.Store, id, ns, body, file, hash, commit string, imp int) {
	t.Helper()
	m := sqlite.Memory{
		ID: id, Namespace: ns, Kind: sqlite.KindObservation,
		Label: body, Body: body, Importance: imp,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker",
		TTLClass: sqlite.TTLNormal, AnchorCommit: commit,
	}
	anchors := []sqlite.Anchor{{Kind: "file", Ref: file, FileHash: hash}}
	if err := s.InsertMemory(context.Background(), m, anchors, nil); err != nil {
		t.Fatalf("insert live %s: %v", id, err)
	}
}

// staleOf reads back the demotion flag of one row, so a test can assert a
// demote or a restore landed.
func staleOf(t *testing.T, s *sqlite.Store, ns, id string) bool {
	t.Helper()
	rows, err := s.StaleRows(context.Background(), ns)
	if err != nil {
		t.Fatalf("stale rows: %v", err)
	}
	for _, r := range rows {
		if r.ID == id {
			return r.Stale
		}
	}
	t.Fatalf("row %s not found", id)
	return false
}

// TestDemoteFlagsFileChangedInCommit is the core of the slice: a memory
// anchored to a file is demoted once that file appears in the diff between the
// memory's anchor_commit and Head.
func TestDemoteFlagsFileChangedInCommit(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "run make gen after editing schema.go", "schema.go", "h1", "c0", 5)

	repo := &fakeRepo{
		head:    "c1",
		changed: map[string]map[string]bool{"c0": {"schema.go": true}},
		hashes:  map[string]string{"schema.go": "h1"}, // hash still matches, the commit is what moved
	}
	c := New(s, &fakeSum{}, nil, WithRepo(repo))

	moved, err := c.demoteStale(ctx, ns)
	if err != nil {
		t.Fatalf("demoteStale: %v", err)
	}
	if len(moved) != 1 || !moved[0].Stale || moved[0].ID != "m1" {
		t.Fatalf("moved = %+v, want one demote of m1", moved)
	}
	if !staleOf(t, s, ns, "m1") {
		t.Fatal("m1 should be marked stale after its file changed")
	}
}

// TestDemoteFlagsHashDrift covers the other trigger: no anchor_commit to diff
// against, but the file's content no longer hashes to what the anchor stored,
// so the memory is stale.
func TestDemoteFlagsHashDrift(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "the transport rule", "transport.go", "h1", "", 5)

	repo := &fakeRepo{
		head:    "c1",
		changed: map[string]map[string]bool{},
		hashes:  map[string]string{"transport.go": "h2"}, // drifted
	}
	c := New(s, &fakeSum{}, nil, WithRepo(repo))

	if _, err := c.demoteStale(ctx, ns); err != nil {
		t.Fatalf("demoteStale: %v", err)
	}
	if !staleOf(t, s, ns, "m1") {
		t.Fatal("m1 should be stale after its file hash drifted")
	}
}

// TestRestoreOnMatchingHash is the reverse: a row already demoted whose files
// match again has its flag cleared and its verified_at advanced to Head.
func TestRestoreOnMatchingHash(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "the transport rule", "transport.go", "h1", "c0", 5)
	if err := s.SetStale(ctx, "m1", true, ""); err != nil {
		t.Fatalf("pre-mark stale: %v", err)
	}

	repo := &fakeRepo{
		head:    "c1",
		changed: map[string]map[string]bool{"c0": {}}, // nothing touched since c0
		hashes:  map[string]string{"transport.go": "h1"},
	}
	c := New(s, &fakeSum{}, nil, WithRepo(repo))

	moved, err := c.demoteStale(ctx, ns)
	if err != nil {
		t.Fatalf("demoteStale: %v", err)
	}
	if len(moved) != 1 || moved[0].Stale {
		t.Fatalf("moved = %+v, want one restore", moved)
	}
	if staleOf(t, s, ns, "m1") {
		t.Fatal("m1 should be restored once its file matches again")
	}
	var verified string
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COALESCE(verified_at,'') FROM memories WHERE id=?`, "m1").Scan(&verified)
	}); err != nil {
		t.Fatalf("read verified_at: %v", err)
	}
	if verified != "c1" {
		t.Fatalf("verified_at = %q, want c1 (Head)", verified)
	}
}

// TestDiffRunsOncePerCommit is the efficiency guarantee: three rows sharing one
// anchor_commit trigger the diff for that commit exactly once, not once per row.
func TestDiffRunsOncePerCommit(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "note one", "a.go", "h1", "c0", 5)
	live(t, s, "m2", ns, "note two", "b.go", "h2", "c0", 5)
	live(t, s, "m3", ns, "note three", "c.go", "h3", "c0", 5)

	repo := &fakeRepo{
		head:    "c1",
		changed: map[string]map[string]bool{"c0": {"a.go": true}},
		hashes:  map[string]string{"a.go": "h1", "b.go": "h2", "c.go": "h3"},
	}
	c := New(s, &fakeSum{}, nil, WithRepo(repo))

	if _, err := c.demoteStale(ctx, ns); err != nil {
		t.Fatalf("demoteStale: %v", err)
	}
	if got := repo.calls(); got != 1 {
		t.Fatalf("ChangedSince called %d times, want 1 for one shared commit", got)
	}
}

// TestFoldReportsDemotions wires the pass into a real fold: a pending candidate
// drives the fold, and a stale row anchored to a changed file shows up in the
// report's Demoted count.
func TestFoldReportsDemotions(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "run make gen after editing schema.go", "schema.go", "h1", "c0", 5)
	insert(t, s, "cand1", obs(ns, "a fresh note the worker just recorded", "other.go", 4))

	repo := &fakeRepo{
		head:    "c1",
		changed: map[string]map[string]bool{"c0": {"schema.go": true}},
		hashes:  map[string]string{"schema.go": "h1", "other.go": ""},
	}
	c := New(s, &fakeSum{merge: "merged"}, nil, WithRepo(repo))

	report, err := c.FoldNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("FoldNamespace: %v", err)
	}
	if report.Demoted != 1 {
		t.Fatalf("report.Demoted = %d, want 1", report.Demoted)
	}
}

// TestNoRepoLeavesRowsAlone: without a working tree the pass is inert, so a
// consolidator built for an import job never demotes.
func TestNoRepoLeavesRowsAlone(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	live(t, s, "m1", ns, "the transport rule", "transport.go", "h1", "c0", 5)

	c := New(s, &fakeSum{}, nil) // no WithRepo
	moved, err := c.demoteStale(ctx, ns)
	if err != nil {
		t.Fatalf("demoteStale: %v", err)
	}
	if moved != nil {
		t.Fatalf("moved = %+v, want nil with no repo", moved)
	}
	if staleOf(t, s, ns, "m1") {
		t.Fatal("m1 must stay live with no repo to check against")
	}
}
