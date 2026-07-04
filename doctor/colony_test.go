package doctor

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// makeColony creates a colony.db at path with schema_migrations recording the
// given versions, so a test can stand up a database at any schema level without
// the real tables. It closes the store so the doctor opens its own view of the
// committed file.
func makeColony(t *testing.T, path string, versions ...int) {
	t.Helper()
	s, err := sqlite.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Write(context.Background(), func(tx *sql.Tx) error {
		if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); err != nil {
			return err
		}
		for _, v := range versions {
			if _, err := tx.Exec(`INSERT INTO schema_migrations (version, applied_at) VALUES (?, 0)`, v); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatalf("seed schema_migrations: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
}

// TestColonyMemoryAtHeadIsClean: a colony.db migrated to the head version reads
// clean.
func TestColonyMemoryAtHeadIsClean(t *testing.T) {
	ctx := freshNest(t)
	// A real migrate lands the head version.
	s, err := sqlite.Open(ctx.Nest.ColonyDB())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	f := findingFor(t, New().Run(ctx), "colony memory")
	if f.Status != StatusOK {
		t.Fatalf("at-head colony status = %v (%s), want ok", f.Status, f.Reason)
	}
}

// TestColonyMemoryAheadSchemaIsCritical: a colony.db past this build's head was
// written by a newer ari and must not be run against.
func TestColonyMemoryAheadSchemaIsCritical(t *testing.T) {
	ctx := freshNest(t)
	head, err := sqlite.HeadVersion()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	makeColony(t, ctx.Nest.ColonyDB(), head+1)

	f := findingFor(t, New().Run(ctx), "colony memory")
	if f.Status != StatusCritical {
		t.Fatalf("ahead-schema status = %v (%s), want critical", f.Status, f.Reason)
	}
	if f.Manual == "" {
		t.Error("an ahead-schema finding must carry manual guidance")
	}
}

// TestColonyMemoryBehindSchemaWarns: a colony.db below head migrates forward on
// the next run, so it is a warning, not a failure.
func TestColonyMemoryBehindSchemaWarns(t *testing.T) {
	ctx := freshNest(t)
	head, err := sqlite.HeadVersion()
	if err != nil {
		t.Fatalf("head: %v", err)
	}
	if head < 2 {
		t.Skip("need at least two migrations to model a behind-head colony")
	}
	makeColony(t, ctx.Nest.ColonyDB(), 1)

	f := findingFor(t, New().Run(ctx), "colony memory")
	if f.Status != StatusWarn {
		t.Fatalf("behind-schema status = %v (%s), want warn", f.Status, f.Reason)
	}
}

// TestColonyMemoryInRepoIsLeak: a colony.db inside the workspace would be
// committed, so it is a critical D16 leak.
func TestColonyMemoryInRepoIsLeak(t *testing.T) {
	ctx := freshNest(t)
	leak := filepath.Join(ctx.Nest.Root, "colony.db")
	if err := os.WriteFile(leak, []byte("stray"), 0o600); err != nil {
		t.Fatal(err)
	}

	f := findingFor(t, New().Run(ctx), "colony memory")
	if f.Status != StatusCritical {
		t.Fatalf("in-repo colony status = %v (%s), want critical", f.Status, f.Reason)
	}
	if f.Manual == "" {
		t.Error("a leak finding must carry manual guidance")
	}
}
