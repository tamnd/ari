package sqlite

import (
	"context"
	"database/sql"
)

// FileAnchor is one file anchor of a row, the path and the content hash stored
// when the memory was written, the pair the invalidation pass compares against
// the file's current state.
type FileAnchor struct {
	Ref  string
	Hash string
}

// StaleRow is a live memory the invalidation pass weighs: its id, the commit it
// was true at, whether it is already demoted, and its file anchors. Rows with
// no file anchor are not returned, because staleness is about files changing
// under a memory, and an unanchored row evaporates on its clock instead.
type StaleRow struct {
	ID           string
	AnchorCommit string
	Stale        bool
	Files        []FileAnchor
}

// StaleRows returns the anchored, non-archived, non-read-only rows in a
// namespace with their file anchors attached, the input to the fold's
// invalidation pass. Human-edited rows are excluded because the consolidator
// must not touch them. Anchors come back grouped per row in id order.
func (s *Store) StaleRows(ctx context.Context, ns string) ([]StaleRow, error) {
	var out []StaleRow
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `
			SELECT m.id, COALESCE(m.anchor_commit, ''), m.stale, a.ref, COALESCE(a.file_hash, '')
			FROM memories m
			JOIN memory_anchor a ON a.memory_id = m.id AND a.kind = 'file'
			WHERE m.namespace = ? AND m.archived_at IS NULL AND m.read_only = 0
			ORDER BY m.id`, ns)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var cur *StaleRow
		for rows.Next() {
			var id, commit, ref, hash string
			var stale int
			if err := rows.Scan(&id, &commit, &stale, &ref, &hash); err != nil {
				return err
			}
			if cur == nil || cur.ID != id {
				out = append(out, StaleRow{ID: id, AnchorCommit: commit, Stale: stale == 1})
				cur = &out[len(out)-1]
			}
			cur.Files = append(cur.Files, FileAnchor{Ref: ref, Hash: hash})
		}
		return rows.Err()
	})
	return out, err
}

// SetStale sets or clears a row's demotion flag. Clearing it advances
// verified_at to the commit the anchors were confirmed against, the field M4's
// verifier reads and this pass writes when a stale row's files match again.
func (s *Store) SetStale(ctx context.Context, id string, stale bool, verifiedAt string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		if verifiedAt != "" {
			_, err := tx.ExecContext(ctx,
				`UPDATE memories SET stale = ?, verified_at = ? WHERE id = ?`,
				boolToInt(stale), verifiedAt, id)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE memories SET stale = ? WHERE id = ?`, boolToInt(stale), id)
		return err
	})
}
