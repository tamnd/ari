package sqlite

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// open brings a store up in a temp dir with a trivial table, so the writer
// tests exercise the channel and transaction path without the real schema,
// which arrives in slice 2.
func open(t *testing.T) *Store {
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
	err = s.Write(context.Background(), func(tx *sql.Tx) error {
		_, err := tx.Exec(`CREATE TABLE t (id INTEGER PRIMARY KEY, v TEXT NOT NULL)`)
		return err
	})
	if err != nil {
		t.Fatalf("create table: %v", err)
	}
	return s
}

// TestConcurrentWritersNoBusy is the D10 invariant: fifty goroutines write
// ten thousand rows through the single writer with zero SQLITE_BUSY, and
// every row reads back, because reads run on the WAL pool and writes
// serialize on one goroutine.
func TestConcurrentWritersNoBusy(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	const writers, perWriter = 50, 200
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := range writers {
		wg.Add(1)
		go func(base int) {
			defer wg.Done()
			for i := range perWriter {
				id := base*perWriter + i
				err := s.Write(ctx, func(tx *sql.Tx) error {
					_, err := tx.Exec(`INSERT INTO t (id, v) VALUES (?, ?)`, id, "row")
					return err
				})
				if err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			if strings.Contains(strings.ToUpper(err.Error()), "BUSY") {
				t.Fatalf("write hit SQLITE_BUSY: %v", err)
			}
			t.Fatalf("write: %v", err)
		}
	}

	var count int
	err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t`).Scan(&count)
	})
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if want := writers * perWriter; count != want {
		t.Fatalf("row count = %d, want %d", count, want)
	}
}

// TestWALModeSet confirms the file is actually in WAL mode, so the
// concurrent-reader promise the store rests on is real, not assumed.
func TestWALModeSet(t *testing.T) {
	s := open(t)
	var mode string
	err := s.Read(context.Background(), func(db *sql.DB) error {
		return db.QueryRow(`PRAGMA journal_mode`).Scan(&mode)
	})
	if err != nil {
		t.Fatalf("pragma: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Fatalf("journal_mode = %q, want wal", mode)
	}
}

// TestReadSeesCommittedWrites is the read-after-write path: a value written
// through the writer is visible on the read pool, which is the WAL snapshot
// working as designed.
func TestReadSeesCommittedWrites(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO t (id, v) VALUES (1, 'hello')`)
		return err
	}); err != nil {
		t.Fatalf("write: %v", err)
	}
	var v string
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT v FROM t WHERE id = 1`).Scan(&v)
	}); err != nil {
		t.Fatalf("read: %v", err)
	}
	if v != "hello" {
		t.Fatalf("read v = %q, want hello", v)
	}
}

// TestRollbackOnError proves a failing write closure rolls back: the second
// insert violates the primary key, and the transaction leaves no partial
// state behind.
func TestRollbackOnError(t *testing.T) {
	s := open(t)
	ctx := context.Background()
	if err := s.Write(ctx, func(tx *sql.Tx) error {
		_, err := tx.Exec(`INSERT INTO t (id, v) VALUES (7, 'a')`)
		return err
	}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	err := s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO t (id, v) VALUES (8, 'b')`); err != nil {
			return err
		}
		// Collides with id 7, so the whole transaction must roll back.
		_, err := tx.Exec(`INSERT INTO t (id, v) VALUES (7, 'c')`)
		return err
	})
	if err == nil {
		t.Fatal("expected a primary-key error, got nil")
	}
	var count int
	if err := s.Read(ctx, func(db *sql.DB) error {
		return db.QueryRowContext(ctx, `SELECT COUNT(*) FROM t WHERE id = 8`).Scan(&count)
	}); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("row 8 survived a rolled-back transaction: count = %d", count)
	}
}

// TestWriteAfterCloseIsRejected confirms a write submitted once the store is
// closed returns cleanly rather than panicking on a closed channel.
func TestWriteAfterCloseIsRejected(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "colony.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := s.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = s.Write(context.Background(), func(tx *sql.Tx) error { return nil })
	if err != ErrClosed && err != ErrNotStarted {
		t.Fatalf("write after close = %v, want ErrClosed or ErrNotStarted", err)
	}
}
