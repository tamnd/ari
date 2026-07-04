package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
)

// Kind classifies a memory row. The storage layer owns its column
// vocabulary; the domain types the loop and the consolidator speak map onto
// these strings.
type Kind = string

const (
	KindObservation Kind = "observation"
	KindReflection  Kind = "reflection"
	KindSkill       Kind = "skill"
	KindPin         Kind = "pin"
)

// TTL class names the decay policy a row lives under (doc 07): pinned never
// decays, normal decays per day, fast per hour, session evaporates when its
// originating session ends.
const (
	TTLPinned  = "pinned"
	TTLNormal  = "normal"
	TTLFast    = "fast"
	TTLSession = "session"
)

// Memory is one live memory row as the store reads and writes it. It mirrors
// the memories table column for column; the higher layers translate their
// domain types to and from this shape.
type Memory struct {
	ID           string
	Namespace    string
	Kind         Kind
	Label        string
	Body         string
	Embedding    []float32 // nil when FTS-only
	EmbedModel   string
	Importance   int
	CreatedAt    int64
	AccessedAt   int64
	AccessCount  int
	SourceAnt    string
	SourceTask   string
	AnchorCommit string
	TTLClass     string
	ReadOnly     bool
	Pinned       bool
}

// Anchor ties a memory to a file, symbol, or command, with the content hash
// at write time so the staleness pass can tell which anchors a commit
// invalidated.
type Anchor struct {
	Kind     string // file | symbol | command
	Ref      string
	FileHash string
}

// InsertMemory writes one live memory row with its anchors and evidence
// edges in a single transaction. A reflection with no evidence edge is
// refused before any write, the earliest of the three enforcement points of
// the no-evidence-no-reflection rule (D11): the loop refuses it at the tool
// boundary, the store refuses it here, and the consolidator refuses it again
// at fold time.
func (s *Store) InsertMemory(ctx context.Context, m Memory, anchors []Anchor, evidence []string) error {
	if m.Kind == KindReflection && len(evidence) == 0 {
		return fmt.Errorf("a reflection needs at least one piece of evidence: name the observations it rests on before recording it")
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		var embedding any // NULL when no vector, so FTS-only rows stay honest
		if len(m.Embedding) > 0 {
			embedding = encodeVector(m.Embedding)
		}
		var embedModel any
		if m.EmbedModel != "" {
			embedModel = m.EmbedModel
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO memories (
				id, namespace, kind, label, body, embedding, embed_model,
				importance, created_at, accessed_at, access_count,
				source_ant, source_task, anchor_commit, ttl_class, read_only, pinned
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			m.ID, m.Namespace, m.Kind, m.Label, m.Body, embedding, embedModel,
			m.Importance, m.CreatedAt, m.AccessedAt, m.AccessCount,
			m.SourceAnt, nullString(m.SourceTask), nullString(m.AnchorCommit),
			m.TTLClass, boolToInt(m.ReadOnly), boolToInt(m.Pinned),
		); err != nil {
			return err
		}
		for _, a := range anchors {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO memory_anchor (memory_id, kind, ref, file_hash) VALUES (?, ?, ?, ?)`,
				m.ID, a.Kind, a.Ref, nullString(a.FileHash),
			); err != nil {
				return err
			}
		}
		for _, ev := range evidence {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO memory_evidence (memory_id, evidence_id) VALUES (?, ?)`,
				m.ID, ev,
			); err != nil {
				return err
			}
		}
		return nil
	})
}

// encodeVector packs a float32 slice into a little-endian BLOB, the on-disk
// form of an embedding. Cosine similarity in slice 4 decodes it back.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		binary.LittleEndian.PutUint32(b[i*4:], math.Float32bits(f))
	}
	return b
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
