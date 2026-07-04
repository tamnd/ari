package memory

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// fakeEmbedder is a configured embedder whose output the test controls: a
// fixed model tag, a vector-per-text function, and an optional error to stand
// in for a down endpoint.
type fakeEmbedder struct {
	model string
	vec   func(text string) []float32
	err   error
}

func (f fakeEmbedder) Configured() bool { return true }
func (f fakeEmbedder) Model() string    { return f.model }
func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.vec(text), nil
}

// store spins a migrated colony.db for the embed tests.
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

func obs(id, model string, vec []float32) sqlite.Memory {
	return sqlite.Memory{
		ID: id, Namespace: "ant_worker", Kind: sqlite.KindObservation,
		Label: "rule " + id, Body: "body for " + id, Importance: 5,
		CreatedAt: 1000, AccessedAt: 1000, SourceAnt: "worker", TTLClass: sqlite.TTLFast,
		Embedding: vec, EmbedModel: model,
	}
}

// TestNullEmbedderWritesNoVector: with no endpoint, Vectorize returns no
// vector and no model tag, so the row lands FTS-only.
func TestNullEmbedderWritesNoVector(t *testing.T) {
	vec, model := Vectorize(context.Background(), NullEmbedder{}, "run make gen after editing schema.go")
	if vec != nil || model != "" {
		t.Fatalf("null embedder produced vec=%v model=%q, want none", vec, model)
	}
}

// TestOpenAIEmbedderReturnsVector: a real /v1/embeddings shape decodes into a
// vector, and Vectorize stamps the model tag alongside it.
func TestOpenAIEmbedderReturnsVector(t *testing.T) {
	want := make([]float32, 768)
	for i := range want {
		want[i] = float32(i) / 768
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/embeddings" {
			t.Errorf("path = %q, want /embeddings", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(embedResponse{Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: want}}})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "nomic-embed-text", 768)
	vec, model := Vectorize(context.Background(), e, "hello")
	if len(vec) != 768 {
		t.Fatalf("vec dim = %d, want 768", len(vec))
	}
	if model != "nomic-embed-text" {
		t.Fatalf("model = %q, want nomic-embed-text", model)
	}
}

// TestDimMismatchIsAnError: a vector of the wrong width is refused, so a
// misconfigured model is caught at the first call.
func TestDimMismatchIsAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(embedResponse{Data: []struct {
			Embedding []float32 `json:"embedding"`
		}{{Embedding: []float32{1, 2, 3}}}})
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "nomic-embed-text", 768)
	if _, err := e.Embed(context.Background(), "hello"); err == nil {
		t.Fatal("a 3-dim vector against dim 768 was accepted, want an error")
	}
}

// TestDownEndpointDegradesToFTSOnly: a configured endpoint that errors leaves
// Vectorize returning no vector and no error to the caller, so a killed
// endpoint mid-run degrades to FTS-only rather than dropping the write.
func TestDownEndpointDegradesToFTSOnly(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	}))
	defer srv.Close()

	e := NewOpenAIEmbedder(srv.URL, "", "nomic-embed-text", 768)
	vec, model := Vectorize(context.Background(), e, "hello")
	if vec != nil || model != "" {
		t.Fatalf("down endpoint produced vec=%v model=%q, want none", vec, model)
	}
}

// TestReEmbedStaleRefreshesChangedModel: rows written under an old model are
// re-embedded when the configured model changes, and a second pass is a
// no-op, the lazy migration recommendation 14 names.
func TestReEmbedStaleRefreshesChangedModel(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	for _, id := range []string{"A", "B"} {
		if err := s.InsertMemory(ctx, obs(id, "old-model", []float32{0.1, 0.2}), nil, nil); err != nil {
			t.Fatalf("insert %s: %v", id, err)
		}
	}
	fresh := fakeEmbedder{model: "new-model", vec: func(string) []float32 { return []float32{1, 1, 1, 1} }}

	n, err := ReEmbedStale(ctx, s, fresh)
	if err != nil {
		t.Fatalf("re-embed: %v", err)
	}
	if n != 2 {
		t.Fatalf("re-embedded %d rows, want 2", n)
	}
	if got := staleCount(t, s, "new-model"); got != 0 {
		t.Fatalf("%d rows still stale after re-embed, want 0", got)
	}

	again, err := ReEmbedStale(ctx, s, fresh)
	if err != nil {
		t.Fatalf("second re-embed: %v", err)
	}
	if again != 0 {
		t.Fatalf("second re-embed touched %d rows, want 0", again)
	}
}

// TestReEmbedStalePicksUpNullTag: a row written while the endpoint was down
// carries no model tag, so the next pass on a live endpoint embeds it.
func TestReEmbedStalePicksUpNullTag(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	// A row written FTS-only: no vector, no model tag.
	if err := s.InsertMemory(ctx, obs("C", "", nil), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	fresh := fakeEmbedder{model: "nomic-embed-text", vec: func(string) []float32 { return []float32{2, 2} }}

	n, err := ReEmbedStale(ctx, s, fresh)
	if err != nil {
		t.Fatalf("re-embed: %v", err)
	}
	if n != 1 {
		t.Fatalf("re-embedded %d rows, want 1", n)
	}
}

// TestReEmbedStaleNullEmbedderIsNoop: a machine with no endpoint never churns
// its rows.
func TestReEmbedStaleNullEmbedderIsNoop(t *testing.T) {
	s := store(t)
	ctx := context.Background()
	if err := s.InsertMemory(ctx, obs("D", "some-model", []float32{0.3}), nil, nil); err != nil {
		t.Fatalf("insert: %v", err)
	}
	n, err := ReEmbedStale(ctx, s, NullEmbedder{})
	if err != nil {
		t.Fatalf("re-embed: %v", err)
	}
	if n != 0 {
		t.Fatalf("null embedder re-embedded %d rows, want 0", n)
	}
}

// staleCount reports how many live rows do not carry model, the query
// ReEmbedStale drives.
func staleCount(t *testing.T, s *sqlite.Store, model string) int {
	t.Helper()
	rows, err := s.StaleEmbeddings(context.Background(), model, 0)
	if err != nil {
		t.Fatalf("stale: %v", err)
	}
	return len(rows)
}
