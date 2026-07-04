package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
)

// migrated brings a store up and runs the migrations to head, the state
// every schema test starts from.
func migrated(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "colony.db"))
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

// tableExists reports whether a table or virtual table is present.
func tableExists(t *testing.T, s *Store, name string) bool {
	t.Helper()
	var got string
	err := s.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(
			`SELECT name FROM sqlite_master WHERE type IN ('table','view') AND name = ?`, name,
		).Scan(&got)
	})
	if err == sql.ErrNoRows {
		return false
	}
	if err != nil {
		t.Fatalf("sqlite_master lookup: %v", err)
	}
	return got == name
}

// TestFreshDatabaseMigratesToHead is the fresh-db path: every table the two
// migrations declare exists, and the recorded version is the head.
func TestFreshDatabaseMigratesToHead(t *testing.T) {
	s := migrated(t)
	for _, name := range []string{
		"memories", "memory_anchor", "memory_evidence", "memories_fts",
		"memory_candidates", "candidate_anchor", "candidate_evidence",
		"cards", "blackboard", "schema_migrations",
	} {
		if !tableExists(t, s, name) {
			t.Errorf("table %q missing after migrate", name)
		}
	}
	v, err := s.schemaVersion(context.Background())
	if err != nil {
		t.Fatalf("schemaVersion: %v", err)
	}
	if v != 5 {
		t.Fatalf("head version = %d, want 5", v)
	}
}

// TestMigrateIsIdempotent runs the migrations twice and asserts the second
// run is a no-op, so every colony startup can call Migrate safely.
func TestMigrateIsIdempotent(t *testing.T) {
	s := migrated(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var count int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&count)
	}); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if count != 5 {
		t.Fatalf("schema_migrations rows = %d, want 5 (no re-apply)", count)
	}
}

// TestMigrateFromVersionZeroUpgradesInOrder simulates an older colony.db
// that only has the version table at zero, and confirms Migrate walks it
// forward to head.
func TestMigrateFromVersionZeroUpgradesInOrder(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "colony.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`)
		return err
	}); err != nil {
		t.Fatalf("seed version table: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if !tableExists(t, s, "memories") || !tableExists(t, s, "memory_candidates") {
		t.Fatal("migrate from zero did not build both migrations")
	}
	v, _ := s.schemaVersion(ctx)
	if v != 5 {
		t.Fatalf("version after upgrade = %d, want 5", v)
	}
}
