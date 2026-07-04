package colony

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	memsqlite "github.com/tamnd/ari/memory/sqlite"
)

// fakeEmbedder is a configured embedder whose vector depends on both the
// text and its own model tag, so a model change moves the vector and the
// lazy re-embed test can see it.
type fakeEmbedder struct{ model string }

func (f fakeEmbedder) Configured() bool { return true }
func (f fakeEmbedder) Model() string    { return f.model }
func (f fakeEmbedder) Embed(_ context.Context, text string) ([]float32, error) {
	return []float32{float32(len(text)), float32(len(f.model))}, nil
}

// openStore brings up a migrated colony.db and a card store over a fresh
// ants directory, both torn down with the test.
func openStore(t *testing.T, emb Embedder) (CardStore, string) {
	t.Helper()
	ctx := context.Background()
	db, err := memsqlite.Open(filepath.Join(t.TempDir(), "colony.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Start(ctx); err != nil {
		t.Fatalf("start db: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	antsDir := filepath.Join(t.TempDir(), "ants")
	return NewCardStore(db, antsDir, emb), antsDir
}

// TestCardRoundTrip is the slice 2 DoD: a card written to disk comes back
// through Load unchanged and still validates.
func TestCardRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	want := WorkerCard()
	if err := store.Upsert(ctx, want); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := store.Load(ctx, want.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round-trip changed the card:\n got %+v\nwant %+v", got, want)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("loaded card must still validate: %v", err)
	}
}

// TestUpsertWritesFiles pins the on-disk shape: card.json is pretty-printed
// and SKILL.md is rendered beside it, both under <name>-<id>.
func TestUpsertWritesFiles(t *testing.T) {
	ctx := context.Background()
	store, antsDir := openStore(t, fakeEmbedder{model: "m1"})
	c := WorkerCard()
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	dir := filepath.Join(antsDir, c.Name+"-"+c.ID)
	cardJSON, err := os.ReadFile(filepath.Join(dir, "card.json"))
	if err != nil {
		t.Fatalf("read card.json: %v", err)
	}
	if !json.Valid(cardJSON) {
		t.Error("card.json is not valid JSON")
	}
	if !reflect.DeepEqual(cardJSON, mustIndent(t, c)) {
		t.Error("card.json is not the pretty-printed card")
	}
	skill, err := os.ReadFile(filepath.Join(dir, "SKILL.md"))
	if err != nil {
		t.Fatalf("read SKILL.md: %v", err)
	}
	if len(skill) == 0 {
		t.Error("SKILL.md is empty")
	}
}

// TestListCarriesDenormalizedColumns is the DoD that the router can
// prefilter without rehydrating card_json: the row carries the classes,
// tools, signals, prefers, and skill vector directly.
func TestListCarriesDenormalizedColumns(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	c := WorkerCard()
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 row, got %d", len(rows))
	}
	r := rows[0]
	if !reflect.DeepEqual(r.Classes, c.Discovery.Classes) {
		t.Errorf("classes column = %v, want %v", r.Classes, c.Discovery.Classes)
	}
	if !reflect.DeepEqual(r.Tools, c.Tools) {
		t.Errorf("tools column = %v, want %v", r.Tools, c.Tools)
	}
	if !reflect.DeepEqual(r.Signals, c.Discovery.Signals) {
		t.Errorf("signals column = %v, want %v", r.Signals, c.Discovery.Signals)
	}
	if len(r.SkillVec) == 0 {
		t.Error("skill_vec column is empty; the summary was not embedded")
	}
	if r.Status != c.Status || r.Tier != c.Tier {
		t.Errorf("status/tier = %s/%s, want %s/%s", r.Status, r.Tier, c.Status, c.Tier)
	}
}

// TestHandEditReloadPicksUpChange is D11 for cards: a human edit to
// card.json is the highest-provenance change and the next Load reads it,
// because content always comes from the file.
func TestHandEditReloadPicksUpChange(t *testing.T) {
	ctx := context.Background()
	store, antsDir := openStore(t, fakeEmbedder{model: "m1"})
	c := WorkerCard()
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("upsert: %v", err)
	}

	path := filepath.Join(antsDir, c.Name+"-"+c.ID, "card.json")
	edited := c
	edited.Discovery.Summary = "A hand-edited summary that the reload must pick up."
	if err := os.WriteFile(path, mustIndent(t, edited), 0o644); err != nil {
		t.Fatalf("hand edit: %v", err)
	}

	got, err := store.Load(ctx, c.ID)
	if err != nil {
		t.Fatalf("load after edit: %v", err)
	}
	if got.Discovery.Summary != edited.Discovery.Summary {
		t.Errorf("reload did not pick up the hand edit: got %q", got.Discovery.Summary)
	}
}

// TestLazyReEmbedOnModelChange is the DoD that a load re-embeds lazily when
// the embed model has changed: the row's vector and model tag move to the
// new embedder without a hand-triggered re-registration.
func TestLazyReEmbedOnModelChange(t *testing.T) {
	ctx := context.Background()
	// Write with model m1.
	store1, antsDir := openStore(t, fakeEmbedder{model: "m1"})
	c := WorkerCard()
	if err := store1.Upsert(ctx, c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	before := store1.(*sqliteCardStore)
	firstModel, firstVec := readRow(t, before, c.ID)

	// A second store over the same db and ants dir, but a newer model.
	store2 := NewCardStore(before.db, antsDir, fakeEmbedder{model: "m22"})
	if _, err := store2.Load(ctx, c.ID); err != nil {
		t.Fatalf("load with new model: %v", err)
	}
	afterModel, afterVec := readRow(t, store2.(*sqliteCardStore), c.ID)

	if firstModel != "m1" {
		t.Fatalf("first embed model = %q, want m1", firstModel)
	}
	if afterModel != "m22" {
		t.Errorf("embed model after reload = %q, want m22 (lazy re-embed)", afterModel)
	}
	if reflect.DeepEqual(firstVec, afterVec) {
		t.Error("vector did not change after the model changed")
	}
}

// TestSetStatusDoesNotRewriteFile is the row-first-stats DoD: a status
// change writes the row and leaves card.json byte-for-byte untouched.
func TestSetStatusDoesNotRewriteFile(t *testing.T) {
	ctx := context.Background()
	store, antsDir := openStore(t, fakeEmbedder{model: "m1"})
	c := WorkerCard()
	if err := store.Upsert(ctx, c); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	path := filepath.Join(antsDir, c.Name+"-"+c.ID, "card.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	if err := store.SetStatus(ctx, c.ID, StatusArchived); err != nil {
		t.Fatalf("set status: %v", err)
	}

	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("SetStatus rewrote card.json; status is row-first, not file content")
	}
	got, err := store.Load(ctx, c.ID)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.Status != StatusArchived {
		t.Errorf("status after SetStatus = %s, want archived", got.Status)
	}
}

// TestLoadUnknownCard reports a clean not-found for an id the store never
// wrote.
func TestLoadUnknownCard(t *testing.T) {
	ctx := context.Background()
	store, _ := openStore(t, fakeEmbedder{model: "m1"})
	if _, err := store.Load(ctx, "ghost"); err == nil {
		t.Error("Load of an unknown id must error")
	}
}

// mustIndent renders a card the same way the store writes card.json, so the
// file-shape assertions compare like with like.
func mustIndent(t *testing.T, c Card) []byte {
	t.Helper()
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		t.Fatalf("indent: %v", err)
	}
	return append(data, '\n')
}

// readRow reads a card row's embed model and vector straight from the db,
// so the re-embed test can assert on what the store actually persisted.
func readRow(t *testing.T, s *sqliteCardStore, id string) (string, []float32) {
	t.Helper()
	var model string
	var vec []byte
	err := s.db.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(`SELECT embed_model, skill_vec FROM cards WHERE id = ?`, id).Scan(&model, &vec)
	})
	if err != nil {
		t.Fatalf("read row: %v", err)
	}
	return model, decodeVec(vec)
}
