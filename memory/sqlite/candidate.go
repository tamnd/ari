package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Source is the provenance stamped on a candidate: which ant proposed it, in
// which task, and the commit the working tree sat at when it did. It travels
// with the candidate through the pending stage so a folded row can cite where
// it came from without the consolidator reconstructing it.
type Source struct {
	Ant    string
	Task   string
	Commit string
}

// Candidate is a proposed memory a worker ant emits during or after a task,
// from a deliberate remember call or from the loop harvesting an observation.
// It is never written to live memory directly; InsertCandidate appends it to
// the pending table and the consolidator folds it at idle or session end, so
// nothing an ant proposes is recallable until a fold has weighed it (D12).
type Candidate struct {
	Namespace  string
	Kind       Kind // observation | reflection
	Body       string
	Importance int      // 1..10; by kind for the harvester, by the model for remember
	Anchors    []Anchor // file | symbol | command, with file hashes
	Evidence   []string // ids of the observations a reflection rests on
	Source     Source
}

// InsertCandidate appends one candidate to the pending table with its anchors
// and evidence in a single transaction, stamping created_at from the clock so
// the consolidator can order intake. A reflection with no evidence is refused
// before any write, the D11 no-evidence-no-reflection rule at its middle
// enforcement point: the tool boundary refuses it first, this refuses it
// second, the consolidator refuses it again at fold time.
func (s *Store) InsertCandidate(ctx context.Context, id string, c Candidate) error {
	if c.Kind == KindReflection && len(c.Evidence) == 0 {
		return fmt.Errorf("a reflection needs at least one piece of evidence: name the observations it rests on before proposing it")
	}
	now := time.Now().Unix()
	return s.Write(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memory_candidates (
				id, namespace, kind, body, importance,
				source_ant, source_task, anchor_commit, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, c.Namespace, c.Kind, c.Body, c.Importance,
			c.Source.Ant, nullString(c.Source.Task), nullString(c.Source.Commit), now,
		); err != nil {
			return err
		}
		for _, a := range c.Anchors {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO candidate_anchor (candidate_id, kind, ref, file_hash) VALUES (?, ?, ?, ?)`,
				id, a.Kind, a.Ref, nullString(a.FileHash),
			); err != nil {
				return err
			}
		}
		for _, ev := range c.Evidence {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO candidate_evidence (candidate_id, evidence_id) VALUES (?, ?)`,
				id, ev,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// PendingCandidates returns the unfolded candidates in one namespace, oldest
// first, the work queue the consolidator drains at a fold. A limit of zero or
// less returns every pending row. The anchors and evidence stay in their own
// tables until the fold needs them, so this read is a cheap scan of the
// index-backed pending set.
func (s *Store) PendingCandidates(ctx context.Context, ns string, limit int) ([]Candidate, []string, error) {
	var cands []Candidate
	var ids []string
	err := s.Read(ctx, func(db *sql.DB) error {
		q := `SELECT id, namespace, kind, body, importance,
			source_ant, COALESCE(source_task, ''), COALESCE(anchor_commit, '')
			FROM memory_candidates
			WHERE namespace = ? AND folded_at IS NULL
			ORDER BY created_at`
		args := []any{ns}
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
			var id string
			var c Candidate
			if err := rows.Scan(&id, &c.Namespace, &c.Kind, &c.Body, &c.Importance,
				&c.Source.Ant, &c.Source.Task, &c.Source.Commit); err != nil {
				return err
			}
			cands = append(cands, c)
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return cands, ids, err
}
