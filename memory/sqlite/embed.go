package sqlite

import (
	"context"
	"database/sql"
)

// StaleEmbeddings returns live memories whose embed_model tag does not match
// model, the rows a re-embed pass on a configured endpoint refreshes. A NULL
// tag never matches, so a row written while the endpoint was down is
// included; a row already at model is skipped so a fold does no needless
// work. A limit of zero or less returns every stale row.
//
// It reads label and body so the caller can rebuild the embedding text
// without a second query, and leaves the vector columns to SetEmbedding.
func (s *Store) StaleEmbeddings(ctx context.Context, model string, limit int) ([]Memory, error) {
	var out []Memory
	err := s.Read(ctx, func(db *sql.DB) error {
		q := `SELECT id, namespace, kind, label, body, COALESCE(embed_model, '')
			FROM memories
			WHERE archived_at IS NULL AND (embed_model IS NULL OR embed_model != ?)
			ORDER BY created_at`
		args := []any{model}
		if limit > 0 {
			q += ` LIMIT ?`
			args = append(args, limit)
		}
		rows, err := db.QueryContext(ctx, q, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var m Memory
			if err := rows.Scan(&m.ID, &m.Namespace, &m.Kind, &m.Label, &m.Body, &m.EmbedModel); err != nil {
				return err
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// SetEmbedding stamps a fresh vector and model tag on one row, the write the
// re-embed pass makes for each stale row. It runs on the single writer like
// every other mutation, so a re-embed during a live turn never contends with
// the reader pool serving recall.
func (s *Store) SetEmbedding(ctx context.Context, id string, vec []float32, model string) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		var embedding any
		if len(vec) > 0 {
			embedding = encodeVector(vec)
		}
		var embedModel any
		if model != "" {
			embedModel = model
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE memories SET embedding = ?, embed_model = ? WHERE id = ?`,
			embedding, embedModel, id)
		return err
	})
}
