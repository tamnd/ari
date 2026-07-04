package sqlite

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// migration is one numbered schema step. Once a migration ships in a
// release it is never edited; a schema change is a new numbered file,
// because a colony.db in the wild is a file a user will not throw away and
// the runner must always walk it forward from whatever version it is at.
type migration struct {
	version int
	name    string
	sql     string
}

// loadMigrations reads the embedded SQL files, parses the leading four-digit
// version from each name, and returns them in version order.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, err
	}
	var out []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".sql") {
			continue
		}
		if len(name) < 4 {
			return nil, fmt.Errorf("migration %q: name is too short to carry a version", name)
		}
		v, err := strconv.Atoi(name[:4])
		if err != nil {
			return nil, fmt.Errorf("migration %q: name must start with a four-digit version", name)
		}
		data, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		out = append(out, migration{version: v, name: name, sql: string(data)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	return out, nil
}

// Migrate brings colony.db up to the head schema version, applying every
// embedded migration newer than the recorded version in order, each in its
// own transaction on the writer so a partial migration never half-lands. It
// is idempotent: a database already at head is a no-op, so every colony
// startup can call it (doc 03 slice 2, D10).
func (s *Store) Migrate(ctx context.Context) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
			version    INTEGER PRIMARY KEY,
			applied_at INTEGER NOT NULL
		)`)
		return err
	}); err != nil {
		return fmt.Errorf("preparing schema_migrations: %w", err)
	}
	current, err := s.schemaVersion(ctx)
	if err != nil {
		return err
	}
	for _, m := range migs {
		if m.version <= current {
			continue
		}
		if err := s.applyMigration(ctx, m); err != nil {
			return fmt.Errorf("applying migration %s: %w", m.name, err)
		}
	}
	return nil
}

// schemaVersion returns the highest applied migration version, or zero on a
// fresh database. It reads through the WAL pool, which sees the just-created
// schema_migrations table because the writer committed it.
func (s *Store) schemaVersion(ctx context.Context) (int, error) {
	var version int
	err := s.Read(ctx, func(db *sql.DB) error {
		// COALESCE turns the no-rows MAX (NULL) into zero without a nil scan.
		return db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version)
	})
	return version, err
}

// applyMigration runs one migration's SQL and records its version in the
// same transaction, so the schema change and the version bump land together
// or not at all.
func (s *Store) applyMigration(ctx context.Context, m migration) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(m.sql); err != nil {
			return err
		}
		_, err := tx.Exec(
			`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().Unix(),
		)
		return err
	})
}
