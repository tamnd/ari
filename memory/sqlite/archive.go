package sqlite

import (
	"context"
	"database/sql"
	"time"
)

// MemoryLabel returns the label of one live row in a namespace, so the forget
// tool can render the row it would archive to the permission pipeline before it
// acts. found is false when no live row in the namespace has that id.
func (s *Store) MemoryLabel(ctx context.Context, ns, id string) (string, bool, error) {
	var label string
	var found bool
	err := s.Read(ctx, func(db *sql.DB) error {
		row := db.QueryRowContext(ctx,
			`SELECT label FROM memories WHERE id = ? AND namespace = ? AND archived_at IS NULL`, id, ns)
		switch err := row.Scan(&label); err {
		case nil:
			found = true
			return nil
		case sql.ErrNoRows:
			return nil
		default:
			return err
		}
	})
	return label, found, err
}

// ArchiveMemory retires one row by stamping archived_at, forget's archival: the
// row drops out of recall and the pinned index but stays in the file, because
// nothing in ari is ever deleted (D11, D13). It returns the archived row's label
// and whether a row was affected, so an id that names nothing live reports it
// rather than silently succeeding. Archiving an already-archived row is a no-op
// that reports archived=false.
func (s *Store) ArchiveMemory(ctx context.Context, ns, id string) (string, bool, error) {
	var label string
	var archived bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx,
			`SELECT label FROM memories WHERE id = ? AND namespace = ? AND archived_at IS NULL`, id, ns)
		switch err := row.Scan(&label); err {
		case nil:
		case sql.ErrNoRows:
			return nil
		default:
			return err
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE memories SET archived_at = ? WHERE id = ? AND namespace = ?`,
			time.Now().Unix(), id, ns); err != nil {
			return err
		}
		archived = true
		return nil
	})
	return label, archived, err
}
