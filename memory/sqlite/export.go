package sqlite

import (
	"context"
	"database/sql"
)

// ExportRow is one live memory as the export renders it: the text a human reads
// and edits, the anchors and provenance that round-trip, and the read_only and
// verified flags the render marks so a developer sees which rows a human already
// pinned and which the machine has confirmed. It is a projection, not the full
// Memory, because export needs the durable fields, not the recall bookkeeping.
type ExportRow struct {
	ID         string
	Kind       string
	Namespace  string
	Label      string
	Body       string
	Importance int
	SourceAnt  string
	SourceTask string
	ReadOnly   bool
	Verified   bool
	Anchors    []Anchor
}

// ExportRows returns a namespace's live rows with their anchors, ordered by
// creation then id so the export is stable across runs. Archived rows are left
// out, because export is the working set a human curates, and a forgotten row
// stays in the file without cluttering the page a developer edits.
func (s *Store) ExportRows(ctx context.Context, ns string) ([]ExportRow, error) {
	var out []ExportRow
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `
			SELECT m.id, m.kind, m.namespace, m.label, m.body, m.importance,
			       m.source_ant, COALESCE(m.source_task, ''),
			       m.read_only, (m.verified_at IS NOT NULL),
			       COALESCE(a.kind, ''), COALESCE(a.ref, ''), COALESCE(a.file_hash, '')
			FROM memories m
			LEFT JOIN memory_anchor a ON a.memory_id = m.id
			WHERE m.namespace = ? AND m.archived_at IS NULL
			ORDER BY m.created_at, m.id, a.rowid`, ns)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		var cur *ExportRow
		for rows.Next() {
			var r ExportRow
			var readOnly, verified int
			var akind, aref, ahash string
			if err := rows.Scan(&r.ID, &r.Kind, &r.Namespace, &r.Label, &r.Body, &r.Importance,
				&r.SourceAnt, &r.SourceTask, &readOnly, &verified,
				&akind, &aref, &ahash); err != nil {
				return err
			}
			if cur == nil || cur.ID != r.ID {
				r.ReadOnly = readOnly == 1
				r.Verified = verified == 1
				out = append(out, r)
				cur = &out[len(out)-1]
			}
			if akind != "" || aref != "" {
				cur.Anchors = append(cur.Anchors, Anchor{Kind: akind, Ref: aref, FileHash: ahash})
			}
		}
		return rows.Err()
	})
	return out, err
}

// UpdateMemoryText rewrites a live row's label and body and marks it read_only,
// the effect of a human editing an exported memory: from here the consolidator
// leaves the row exactly as written, because a human edit outranks the machine
// (D11). It returns whether a live, non-archived row in the namespace matched
// the id, so import can tell an applied edit from a stale one.
func (s *Store) UpdateMemoryText(ctx context.Context, ns, id, label, body string) (bool, error) {
	var updated bool
	err := s.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE memories SET label = ?, body = ?, read_only = 1
			WHERE id = ? AND namespace = ? AND archived_at IS NULL`,
			label, body, id, ns)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		updated = n > 0
		return nil
	})
	return updated, err
}
