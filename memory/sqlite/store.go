// Package sqlite is the colony.db substrate: one modernc SQLite file per
// project, opened in WAL mode, with every write funneled through a single
// goroutine and reads served from their own pool so recall never waits
// behind a fold (doc 03 slice 1, D10). It is pure Go, no cgo, so ari stays
// a single static binary on every platform the release ships to.
//
// The shape mirrors the journal writer (journal.Journal): Open prepares the
// file, Start brings the writer goroutine up, Close drains and closes it,
// so the colony controls the goroutine's lifetime exactly as it does the
// journal's.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sync"

	_ "modernc.org/sqlite" // registers the pure-Go "sqlite" driver
)

// ErrClosed is returned to a caller whose write did not run because the
// store was closing. It is not a write failure; the write was never
// attempted, so a caller may treat it as a shutdown signal.
var ErrClosed = errors.New("memory store is closed")

// ErrNotStarted is returned by Write before Start brings the writer up, so
// a caller that writes too early gets a clear reason rather than a hang.
var ErrNotStarted = errors.New("memory store is not started")

// Store owns the write channel and the read pool. It is the only thing in
// the tree that holds the write connection to colony.db: every mutation
// runs on the one writer goroutine, and reads run on the WAL read pool that
// never blocks behind a write (D10).
type Store struct {
	path   string
	writes chan writeReq
	reads  *sql.DB // read-only pool, WAL readers never block the writer
	write  *sql.DB // capped to one connection; only the writer goroutine uses it

	mu      sync.Mutex
	started bool
	quit    chan struct{}
	done    chan struct{}
}

// Open prepares the store at path, creating the file and its parent if
// missing and setting the WAL pragmas on every connection. It starts no
// goroutine until Start, so a caller subscribes to the colony before the
// writer can produce anything, the same contract Colony.Open keeps.
func Open(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	write, err := sql.Open("sqlite", writeDSN(path))
	if err != nil {
		return nil, err
	}
	// One underlying connection for writes: the goroutine serializes them and
	// the cap keeps SQLite's single-writer rule honest at the pool layer too.
	write.SetMaxOpenConns(1)
	if err := write.Ping(); err != nil {
		_ = write.Close()
		return nil, err
	}
	reads, err := sql.Open("sqlite", readDSN(path))
	if err != nil {
		_ = write.Close()
		return nil, err
	}
	reads.SetMaxOpenConns(4)
	return &Store{
		path:   path,
		write:  write,
		reads:  reads,
		writes: make(chan writeReq, 256),
		quit:   make(chan struct{}),
		done:   make(chan struct{}),
	}, nil
}

// Start brings the writer goroutine up. Separate from Open so the colony
// controls goroutine lifetimes (doc 01 section 4.1).
func (s *Store) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	s.started = true
	go s.run()
	return nil
}

// Write runs fn inside a transaction on the single writer goroutine and
// blocks for the result, so callers get errors synchronously while every
// write stays serialized (D10). A write submitted during shutdown returns
// ErrClosed without running.
func (s *Store) Write(ctx context.Context, fn func(tx *sql.Tx) error) error {
	s.mu.Lock()
	started := s.started
	s.mu.Unlock()
	if !started {
		return ErrNotStarted
	}
	reply := make(chan error, 1)
	req := writeReq{ctx: ctx, fn: fn, reply: reply}
	select {
	case s.writes <- req:
	case <-s.quit:
		return ErrClosed
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-reply:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Read runs fn against the read pool, which serves the last committed WAL
// snapshot without waiting on the writer. The pool is query-only, so a fn
// that tries to mutate fails rather than racing the single writer.
func (s *Store) Read(ctx context.Context, fn func(db *sql.DB) error) error {
	return fn(s.reads)
}

// Close drains the writer, then closes both connection pools. Idempotent
// and safe from a signal handler, like Colony.Close.
func (s *Store) Close() error {
	s.mu.Lock()
	if s.started {
		s.started = false
		close(s.quit)
		s.mu.Unlock()
		<-s.done
	} else {
		s.mu.Unlock()
	}
	err := s.write.Close()
	if rerr := s.reads.Close(); err == nil {
		err = rerr
	}
	return err
}

// writeDSN builds the DSN for the write connection: WAL for concurrent
// readers, a busy timeout so a brief lock waits rather than erroring,
// foreign keys on so the schema's references are enforced, and NORMAL
// synchronous, the safe-and-fast WAL default (D10).
func writeDSN(path string) string {
	return dsn(path,
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
	)
}

// readDSN is the write pragmas plus query_only, so a read connection can
// never mutate the file and the single-writer invariant holds even if a
// read closure is buggy.
func readDSN(path string) string {
	return dsn(path,
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(1)",
		"synchronous(NORMAL)",
		"query_only(1)",
	)
}

// dsn renders a modernc file DSN with the given pragmas applied on every
// new connection.
func dsn(path string, pragmas ...string) string {
	q := url.Values{}
	for _, p := range pragmas {
		q.Add("_pragma", p)
	}
	return "file:" + path + "?" + q.Encode()
}
