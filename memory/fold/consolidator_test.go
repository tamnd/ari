package fold

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/memory/sqlite"
	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// store brings a migrated store up in a temp dir, the state every fold test
// starts from.
func store(t *testing.T) *sqlite.Store {
	t.Helper()
	s, err := sqlite.Open(filepath.Join(t.TempDir(), "colony.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return s
}

// fakeSum is a stand-in for the cheap tier. It answers a merge prompt and a
// reflect prompt differently, counts its calls, spends a token per call, and
// can block its first call so a test can hold a fold open and prove a second
// one is refused.
type fakeSum struct {
	mu     sync.Mutex
	calls  int
	merge  string
	lesson string

	block   chan struct{}
	entered chan struct{}
	once    sync.Once
}

func (f *fakeSum) Summarize(ctx context.Context, prompt string) (string, int, error) {
	if f.block != nil {
		f.once.Do(func() {
			if f.entered != nil {
				close(f.entered)
			}
		})
		<-f.block
	}
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	if strings.Contains(prompt, "general lesson") {
		return f.lesson, 5, nil
	}
	return f.merge, 3, nil
}

func (f *fakeSum) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func obs(ns, body, anchor string, importance int) sqlite.Candidate {
	return sqlite.Candidate{
		Namespace: ns, Kind: sqlite.KindObservation, Body: body, Importance: importance,
		Anchors: []sqlite.Anchor{{Kind: "file", Ref: anchor}},
		Source:  sqlite.Source{Ant: "worker", Task: "t1", Commit: "9c2e1a4"},
	}
}

func insert(t *testing.T, s *sqlite.Store, id string, c sqlite.Candidate) {
	t.Helper()
	if err := s.InsertCandidate(context.Background(), id, c); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
}

func countMemories(t *testing.T, s *sqlite.Store, ns, kind string) int {
	t.Helper()
	var n int
	if err := s.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(
			`SELECT COUNT(*) FROM memories WHERE namespace = ? AND kind = ?`, ns, kind).Scan(&n)
	}); err != nil {
		t.Fatalf("count memories: %v", err)
	}
	return n
}

// TestFoldMergesRepeats: twenty candidates, most of them near-duplicates in a
// few clusters, fold to a handful of live rows, and the repeats do not inflate
// importance past the strongest single note.
func TestFoldMergesRepeats(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	// Cluster A: eight rewordings of the same note, all importance 4.
	for i, body := range []string{
		"run make gen after editing schema definition files",
		"make gen must run after editing schema definition files",
		"after editing schema definition files run make gen",
		"editing schema definition files means running make gen",
		"run make gen once schema definition files change",
		"schema definition files edited then run make gen please",
		"make gen after schema definition files were edited",
		"remember to run make gen after schema definition files",
	} {
		insert(t, s, string(rune('a'+i)), obs(ns, body, "schema.go", 4))
	}
	// Cluster B: five rewordings of a timeout note, importance 5.
	for i, body := range []string{
		"the http client timeout should be thirty seconds",
		"set the http client timeout to thirty seconds",
		"http client timeout is thirty seconds not less",
		"thirty seconds is the correct http client timeout",
		"http client timeout must be thirty seconds",
	} {
		insert(t, s, string(rune('A'+i)), obs(ns, body, "http.go", 5))
	}
	// Seven distinct singletons.
	for i, body := range []string{
		"the parser owns the unsuffixed packages now",
		"golden files are regenerated from typescript sources",
		"cosign signs the release artifacts in the pipeline",
		"the writer goroutine serializes every database write",
		"wal readers never block the single writer",
		"embeddings are stored as little endian float blobs",
		"recall fuses full text and vector rank",
	} {
		insert(t, s, "s"+string(rune('0'+i)), obs(ns, body, "note"+string(rune('0'+i))+".go", 3))
	}

	sum := &fakeSum{merge: "canonical merged note about the cluster", lesson: ""}
	c := New(s, sum, nil)
	reports, err := c.Fold(ctx)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %d, want 1", len(reports))
	}
	r := reports[0]
	if r.Candidates != 20 {
		t.Fatalf("candidates = %d, want 20", r.Candidates)
	}
	// Two merged clusters plus seven singletons.
	if got := countMemories(t, s, ns, sqlite.KindObservation); got != 9 {
		t.Fatalf("observation rows = %d, want 9", got)
	}
	if r.Merged != 9 {
		t.Fatalf("report merged = %d, want 9", r.Merged)
	}
	// Exactly two model calls, one per multi-member cluster; singletons skip it.
	if sum.count() != 2 {
		t.Fatalf("summarizer calls = %d, want 2", sum.count())
	}
	if r.TokensCheap != 6 {
		t.Fatalf("cheap tokens = %d, want 6", r.TokensCheap)
	}
	// Repetition did not inflate importance: cluster B stays 5, not 25.
	var maxImp int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT MAX(importance) FROM memories WHERE namespace = ?`, ns).Scan(&maxImp)
	}); err != nil {
		t.Fatalf("max importance: %v", err)
	}
	if maxImp != 5 {
		t.Fatalf("max importance = %d, want 5 (repetition must not inflate)", maxImp)
	}
	// The folded rows are now recallable; a pending candidate never was.
	got, err := s.Recall(ctx, ns, "schema definition files", nil, 10)
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("recall found no folded memory")
	}
	// No candidate remains pending after the fold.
	_, ids, err := s.PendingCandidates(ctx, ns, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("candidates left pending after fold: %v", ids)
	}
}

// TestFoldReflectsWithEvidence: two distinct notes on the same file are not
// merged, and the fold draws a reflection over them that rests on both rows.
func TestFoldReflectsWithEvidence(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	insert(t, s, "o1", obs(ns, "generated code is overwritten on every make gen", "schema.go", 4))
	insert(t, s, "o2", obs(ns, "tests read the checked in schema fixture directly", "schema.go", 4))

	sum := &fakeSum{merge: "m", lesson: "never hand edit anything tied to schema.go"}
	c := New(s, sum, nil)
	r, err := c.FoldNamespace(ctx, ns)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if got := countMemories(t, s, ns, sqlite.KindObservation); got != 2 {
		t.Fatalf("observation rows = %d, want 2 (distinct notes must not merge)", got)
	}
	if got := countMemories(t, s, ns, sqlite.KindReflection); got != 1 {
		t.Fatalf("reflection rows = %d, want 1", got)
	}
	if r.Reflections != 1 {
		t.Fatalf("report reflections = %d, want 1", r.Reflections)
	}
	// The reflection rests on both observations.
	var edges int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`
			SELECT COUNT(*) FROM memory_evidence e
			JOIN memories m ON m.id = e.memory_id
			WHERE m.kind = ?`, sqlite.KindReflection).Scan(&edges)
	}); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if edges != 2 {
		t.Fatalf("reflection evidence edges = %d, want 2", edges)
	}
}

// TestFoldWritesNoReflectionWithoutLesson: when the model draws no lesson from
// the related notes, the fold writes the observations and no reflection. A
// reflection is never invented.
func TestFoldWritesNoReflectionWithoutLesson(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	insert(t, s, "o1", obs(ns, "generated code is overwritten on every make gen", "schema.go", 4))
	insert(t, s, "o2", obs(ns, "tests read the checked in schema fixture directly", "schema.go", 4))

	sum := &fakeSum{merge: "m", lesson: "   "} // whitespace only: no lesson
	c := New(s, sum, nil)
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if got := countMemories(t, s, ns, sqlite.KindReflection); got != 0 {
		t.Fatalf("reflection rows = %d, want 0 (no lesson, no reflection)", got)
	}
}

// TestFoldEmptyNamespaceSpendsNothing: a fold over a namespace with no pending
// candidates writes nothing and never touches the model.
func TestFoldEmptyNamespaceSpendsNothing(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	sum := &fakeSum{merge: "m", lesson: "l"}
	c := New(s, sum, nil)
	r, err := c.FoldNamespace(ctx, "empty")
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if r.Candidates != 0 || r.Merged != 0 || r.TokensCheap != 0 {
		t.Fatalf("empty fold report = %+v, want all zero", r)
	}
	if sum.count() != 0 {
		t.Fatalf("summarizer called %d times on empty fold, want 0", sum.count())
	}
}

// TestFoldEmitsReport: the emit callback receives a report per folded namespace
// and its wire payload narrows to the memory.folded schema.
func TestFoldEmitsReport(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	insert(t, s, "o1", obs(ns, "the writer goroutine serializes writes", "store.go", 4))

	var got []FoldReport
	c := New(s, &fakeSum{merge: "m", lesson: ""}, func(r FoldReport) { got = append(got, r) })
	if _, err := c.Fold(ctx); err != nil {
		t.Fatalf("fold: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("emitted %d reports, want 1", len(got))
	}
	w := got[0].WirePayload()
	if w.Namespace != ns || w.Merged != 1 || w.Archived != 0 {
		t.Fatalf("wire payload = %+v, want {ns 1 0}", w)
	}
}

// TestFoldSingleFlight: while one fold is in flight a second trigger is a
// no-op, so two triggers never fold the same candidates twice.
func TestFoldSingleFlight(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"
	// Two near-duplicates so the merge invokes the model and the fold blocks
	// inside it, holding the gate for the second trigger to bounce off.
	insert(t, s, "a", obs(ns, "one note that will hold the fold open inside the model", "a.go", 4))
	insert(t, s, "b", obs(ns, "one note that will hold the fold open inside the model call", "a.go", 4))

	sum := &fakeSum{merge: "m", lesson: "", block: make(chan struct{}), entered: make(chan struct{})}
	c := New(s, sum, nil)

	done := make(chan []FoldReport, 1)
	go func() {
		r, err := c.Fold(ctx)
		if err != nil {
			t.Errorf("background fold: %v", err)
		}
		done <- r
	}()

	<-sum.entered // the first fold is now inside the model call, holding the gate
	second, err := c.Fold(ctx)
	if err != nil {
		t.Fatalf("second fold: %v", err)
	}
	if second != nil {
		t.Fatalf("second fold ran while the first held the gate: %+v", second)
	}
	close(sum.block) // let the first fold finish

	first := <-done
	if len(first) != 1 {
		t.Fatalf("first fold reports = %d, want 1", len(first))
	}

	// The two near-duplicates folded to one row, exactly once.
	if got := countMemories(t, s, ns, sqlite.KindObservation); got != 1 {
		t.Fatalf("observation rows = %d, want 1 (folded exactly once)", got)
	}
}

// TestFoldCarriesReflectionCandidate: an ant-authored reflection candidate is
// folded as a reflection that still cites the live observations it named.
func TestFoldCarriesReflectionCandidate(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	ns := "ant_worker"

	// Two live observations the reflection will cite, written directly.
	now := time.Unix(1700000000, 0)
	obsA := sqlite.Memory{ID: "mA", Namespace: ns, Kind: sqlite.KindObservation, Body: "observation A", Importance: 4, TTLClass: sqlite.TTLNormal, CreatedAt: now.Unix(), AccessedAt: now.Unix()}
	obsB := sqlite.Memory{ID: "mB", Namespace: ns, Kind: sqlite.KindObservation, Body: "observation B", Importance: 4, TTLClass: sqlite.TTLNormal, CreatedAt: now.Unix(), AccessedAt: now.Unix()}
	if err := s.InsertMemory(ctx, obsA, nil, nil); err != nil {
		t.Fatalf("insert obsA: %v", err)
	}
	if err := s.InsertMemory(ctx, obsB, nil, nil); err != nil {
		t.Fatalf("insert obsB: %v", err)
	}

	refl := sqlite.Candidate{
		Namespace: ns, Kind: sqlite.KindReflection, Body: "the lesson both observations teach", Importance: 8,
		Anchors:  []sqlite.Anchor{{Kind: "file", Ref: "x.go"}},
		Evidence: []string{"mA", "mB"},
		Source:   sqlite.Source{Ant: "worker"},
	}
	insert(t, s, "R1", refl)

	c := New(s, &fakeSum{merge: "m", lesson: ""}, nil)
	if _, err := c.FoldNamespace(ctx, ns); err != nil {
		t.Fatalf("fold: %v", err)
	}
	// The carried reflection rests on both cited observations.
	var edges int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`
			SELECT COUNT(*) FROM memory_evidence e
			JOIN memories m ON m.id = e.memory_id
			WHERE m.kind = ? AND m.body = ?`, sqlite.KindReflection, refl.Body).Scan(&edges)
	}); err != nil {
		t.Fatalf("count evidence: %v", err)
	}
	if edges != 2 {
		t.Fatalf("carried reflection evidence edges = %d, want 2", edges)
	}
}
