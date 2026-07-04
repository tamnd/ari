package sqlite

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"time"
)

// NewID returns a time-sortable memory id: a microsecond timestamp big-endian,
// then random entropy, hex-encoded. It sorts by creation like a ULID so the
// recall tie-break stays meaningful, without pulling a ULID dependency in.
func NewID(now time.Time) string {
	var b [12]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMicro()))
	_, _ = rand.Read(b[8:])
	return hex.EncodeToString(b[:])
}

// Folded is one live row the consolidator writes at a fold: the memory itself,
// its anchors, and its evidence. A reflection synthesized in this fold cites
// the merged rows it rests on by their index in the batch (Evidence), and a
// reflection carried over from a candidate cites existing memory rows by id
// (EvidenceIDs). CommitFold assigns the merged ids and wires both kinds of
// edge, so the consolidator never authors an id or invents an evidence
// pointer by hand.
type Folded struct {
	Memory      Memory
	Anchors     []Anchor
	Evidence    []int    // indices into the merged slice; for a fold-synthesized reflection
	EvidenceIDs []string // existing memory ids; for a reflection carried from a candidate
}

// PendingNamespaces lists the namespaces that have at least one unfolded
// candidate, the set a fold cycle iterates. It is a cheap index-backed scan so
// a quiet colony with nothing pending pays almost nothing to learn it has
// nothing to do.
func (s *Store) PendingNamespaces(ctx context.Context) ([]string, error) {
	var out []string
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx,
			`SELECT DISTINCT namespace FROM memory_candidates WHERE folded_at IS NULL ORDER BY namespace`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var ns string
			if err := rows.Scan(&ns); err != nil {
				return err
			}
			out = append(out, ns)
		}
		return rows.Err()
	})
	return out, err
}

// CommitFold writes the fold's output and retires its candidates in one
// transaction, so a fold either lands whole or not at all and a crash mid-fold
// never leaves half a consolidation behind. It assigns each merged row a fresh
// sortable id, wires each reflection's evidence to the ids of the merged rows
// it rests on, and stamps folded_at on the consumed candidates so the next
// fold does not see them again. A reflection with no evidence is refused here
// too, the third and last enforcement of the no-evidence rule (D11).
func (s *Store) CommitFold(ctx context.Context, merged []Folded, candidateIDs []string, now time.Time) error {
	return s.Write(ctx, func(tx *sql.Tx) error {
		ids := make([]string, len(merged))
		for i := range merged {
			ids[i] = NewID(now.Add(time.Duration(i) * time.Microsecond))
		}
		for i := range merged {
			f := &merged[i]
			m := f.Memory
			m.ID = ids[i]
			if m.CreatedAt == 0 {
				m.CreatedAt = now.Unix()
			}
			if m.AccessedAt == 0 {
				m.AccessedAt = now.Unix()
			}
			var evidence []string
			for _, idx := range f.Evidence {
				if idx < 0 || idx >= len(ids) {
					return fmt.Errorf("fold evidence index %d out of range", idx)
				}
				evidence = append(evidence, ids[idx])
			}
			evidence = append(evidence, f.EvidenceIDs...)
			if m.Kind == KindReflection && len(evidence) == 0 {
				return fmt.Errorf("fold tried to write a reflection with no evidence: %q", m.Label)
			}
			if err := insertMemoryTx(ctx, tx, m, f.Anchors, evidence); err != nil {
				return err
			}
		}
		for _, id := range candidateIDs {
			if _, err := tx.ExecContext(ctx,
				`UPDATE memory_candidates SET folded_at = ? WHERE id = ?`, now.Unix(), id,
			); err != nil {
				return err
			}
		}
		return nil
	})
}
