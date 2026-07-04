package sqlite

import (
	"context"
	"database/sql"
)

// PinnedRow is one pinned memory as the index renders it: its handle, its
// write-time importance for ordering, and its anchor references. It is a narrow
// projection, not a full Memory, because the pinned index needs only what fits
// on one line.
type PinnedRow struct {
	ID         string
	Label      string
	Importance int
	Anchors    []string // "kind:ref", in a stable order
}

// PinnedRows returns a namespace's pinned, non-archived rows with their anchors,
// ordered by importance then id so the render is deterministic across folds. A
// pinned row that was archived by forget drops out, so the index never shows a
// retired pin.
func (s *Store) PinnedRows(ctx context.Context, ns string) ([]PinnedRow, error) {
	var out []PinnedRow
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `
			SELECT m.id, m.label, m.importance, COALESCE(a.kind, ''), COALESCE(a.ref, '')
			FROM memories m
			LEFT JOIN memory_anchor a ON a.memory_id = m.id
			WHERE m.namespace = ? AND m.pinned = 1 AND m.archived_at IS NULL
			ORDER BY m.importance DESC, m.id, a.rowid`, ns)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var cur *PinnedRow
		for rows.Next() {
			var id, label, kind, ref string
			var importance int
			if err := rows.Scan(&id, &label, &importance, &kind, &ref); err != nil {
				return err
			}
			if cur == nil || cur.ID != id {
				out = append(out, PinnedRow{ID: id, Label: label, Importance: importance})
				cur = &out[len(out)-1]
			}
			if kind != "" || ref != "" {
				cur.Anchors = append(cur.Anchors, kind+":"+ref)
			}
		}
		return rows.Err()
	})
	return out, err
}
