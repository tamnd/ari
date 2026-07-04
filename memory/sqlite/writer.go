package sqlite

import (
	"context"
	"database/sql"
)

// writeReq is one unit of work for the single writer goroutine. fn runs
// inside a transaction; the writer sends the error back on reply so callers
// block for the result while all writes stay serialized on one goroutine
// (D10). ctx carries the caller's cancellation into the transaction.
type writeReq struct {
	ctx   context.Context
	fn    func(tx *sql.Tx) error
	reply chan error
}

// run is the single writer goroutine. It drains the write channel, runs
// each request in its own transaction, and returns the result to the
// caller. On shutdown it closes done after replying ErrClosed to anything
// already queued, so no caller is left blocked on a reply that never comes.
func (s *Store) run() {
	defer close(s.done)
	for {
		select {
		case req := <-s.writes:
			req.reply <- s.do(req)
		case <-s.quit:
			s.drain()
			return
		}
	}
}

// drain answers every request already sitting in the channel with
// ErrClosed, so a write accepted just before quit does not hang its caller.
func (s *Store) drain() {
	for {
		select {
		case req := <-s.writes:
			req.reply <- ErrClosed
		default:
			return
		}
	}
}

// do runs one request in a transaction: begin, apply fn, commit, or roll
// back on any error. A rollback error is subordinate to the fn error that
// caused it, so the caller sees the reason the write failed.
func (s *Store) do(req writeReq) error {
	tx, err := s.write.BeginTx(req.ctx, nil)
	if err != nil {
		return err
	}
	if err := req.fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
