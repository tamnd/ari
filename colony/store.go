package colony

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// ErrNoCard is returned when a card id has no row and no file. It is a
// clean not-found a caller can branch on, not a store failure.
var ErrNoCard = errors.New("colony: no card with that id")

// Embedder turns a card's Discovery.Summary into the SkillVec the router
// scores on. It is the same shape package memory uses, restated here so the
// kernel does not import the memory tree; any embedder satisfies it by
// structure. The null embedder (Configured false) is a valid state: the
// colony routes on the class and signal prefilters alone until an endpoint
// is configured.
type Embedder interface {
	Configured() bool
	Model() string
	Embed(ctx context.Context, text string) ([]float32, error)
}

// substrate is the slice of the colony.db store this package needs: the
// single-writer Write and the WAL Read pool. memory/sqlite.Store satisfies
// it by structure, so the kernel takes a handle without importing the
// substrate package (D10).
type substrate interface {
	Read(ctx context.Context, fn func(db *sql.DB) error) error
	Write(ctx context.Context, fn func(tx *sql.Tx) error) error
}

// CardRow is the denormalized prefilter surface: the columns the router
// scans to narrow the candidate set without rehydrating card_json (doc 06
// section 2.3). SkillVec is the derived embedding, which lives only in the
// row, never in the git-friendly card.json.
type CardRow struct {
	ID         string
	Name       string
	Status     CardStatus
	Tier       ModelTier
	Classes    []TaskClass
	Tools      []string
	SkillVec   []float32
	EmbedModel string
	Signals    []string
	Prefers    []TaskClass
}

// CardStore persists cards with D11's editable-wins discipline: authored
// content is file-first so it diffs in git, live status and the derived
// embedding are row-first so they do not thrash the file every task.
type CardStore interface {
	// Upsert writes the authored content to card.json and SKILL.md and the
	// routing columns to the row, embedding Discovery.Summary if an endpoint
	// is configured.
	Upsert(ctx context.Context, c Card) error
	// Load reads a card back: authored content from card.json (the source of
	// truth), live status from the row, re-embedding lazily when the embed
	// model has changed since the row was written.
	Load(ctx context.Context, id string) (Card, error)
	// List returns the denormalized prefilter rows for every card.
	List(ctx context.Context) ([]CardRow, error)
	// SetStatus writes a card's live status to the row only, never rewriting
	// card.json, because status is a live field and not authored content.
	SetStatus(ctx context.Context, id string, status CardStatus) error
}

// sqliteCardStore is the CardStore backed by colony.db and the project
// nest's ants directory.
type sqliteCardStore struct {
	db      substrate
	antsDir string
	emb     Embedder
}

// NewCardStore builds a CardStore over the colony.db handle, the project
// nest's ants directory (nest.AntsDir), and an embedder. A null embedder is
// fine; cards then carry an empty vector and route on the prefilters alone.
func NewCardStore(db substrate, antsDir string, emb Embedder) CardStore {
	return &sqliteCardStore{db: db, antsDir: antsDir, emb: emb}
}

// Upsert writes the file first, then the row. The file is the git artifact,
// the row is the query surface, and content flows file to row here so the
// two agree after a registration or a hand-edit reload.
func (s *sqliteCardStore) Upsert(ctx context.Context, c Card) error {
	if err := c.Validate(); err != nil {
		return err
	}
	vec, model := s.vectorize(ctx, c.Discovery.Summary)
	if err := writeCardFiles(s.antsDir, c); err != nil {
		return err
	}
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		return upsertRow(tx, c, vec, model)
	})
}

// Load reconciles the file and the row. It reads content from card.json,
// which is authoritative, takes the live status from the row, and if the
// embedder's model no longer matches what the row was embedded with, it
// re-embeds from the current summary and writes the fresh vector back to the
// row only, leaving the file untouched.
func (s *sqliteCardStore) Load(ctx context.Context, id string) (Card, error) {
	name, status, rowModel, err := s.rowMeta(ctx, id)
	if err != nil {
		return Card{}, err
	}
	c, err := readCardFile(s.antsDir, name, id)
	if err != nil {
		return Card{}, err
	}
	c.Status = status
	if s.emb.Configured() && s.emb.Model() != rowModel {
		vec, model := s.vectorize(ctx, c.Discovery.Summary)
		if err := s.db.Write(ctx, func(tx *sql.Tx) error {
			_, werr := tx.ExecContext(ctx,
				`UPDATE cards SET skill_vec = ?, embed_model = ? WHERE id = ?`,
				encodeVec(vec), model, id)
			return werr
		}); err != nil {
			return Card{}, err
		}
	}
	return c, nil
}

// List returns every card's prefilter row, status index order, so the
// router scans the small columns without touching card_json.
func (s *sqliteCardStore) List(ctx context.Context) ([]CardRow, error) {
	var out []CardRow
	err := s.db.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx,
			`SELECT id, name, status, tier, classes, tools, skill_vec, embed_model, signals, prefers
			   FROM cards ORDER BY name`)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var r CardRow
			var classes, tools, signals, prefers string
			var vec []byte
			if err := rows.Scan(&r.ID, &r.Name, &r.Status, &r.Tier,
				&classes, &tools, &vec, &r.EmbedModel, &signals, &prefers); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(classes), &r.Classes); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(tools), &r.Tools); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(signals), &r.Signals); err != nil {
				return err
			}
			if err := json.Unmarshal([]byte(prefers), &r.Prefers); err != nil {
				return err
			}
			r.SkillVec = decodeVec(vec)
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

// SetStatus writes the live status to the row only. It never rewrites
// card.json, because status is not authored content: a human editing the
// file owns the summary and the tools, the colony owns the status.
func (s *sqliteCardStore) SetStatus(ctx context.Context, id string, status CardStatus) error {
	return s.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `UPDATE cards SET status = ? WHERE id = ?`, status, id)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNoCard
		}
		return nil
	})
}

// vectorize embeds text when an endpoint is configured, returning the vector
// and the model tag stamped on the row. A null embedder returns no vector
// and an empty model, which is a valid unembedded state.
func (s *sqliteCardStore) vectorize(ctx context.Context, text string) ([]float32, string) {
	if !s.emb.Configured() {
		return nil, ""
	}
	vec, err := s.emb.Embed(ctx, text)
	if err != nil {
		return nil, ""
	}
	return vec, s.emb.Model()
}

// rowMeta reads the small columns Load needs before it reads the file: the
// name (to locate the ant directory), the live status, and the embed model
// (to decide on a lazy re-embed).
func (s *sqliteCardStore) rowMeta(ctx context.Context, id string) (name string, status CardStatus, model string, err error) {
	err = s.db.Read(ctx, func(db *sql.DB) error {
		row := db.QueryRowContext(ctx, `SELECT name, status, embed_model FROM cards WHERE id = ?`, id)
		serr := row.Scan(&name, &status, &model)
		if errors.Is(serr, sql.ErrNoRows) {
			return ErrNoCard
		}
		return serr
	})
	return name, status, model, err
}

// upsertRow writes the denormalized routing columns and the full card JSON.
func upsertRow(tx *sql.Tx, c Card, vec []float32, model string) error {
	classes, err := json.Marshal(c.Discovery.Classes)
	if err != nil {
		return err
	}
	tools, err := json.Marshal(c.Tools)
	if err != nil {
		return err
	}
	signals, err := json.Marshal(c.Discovery.Signals)
	if err != nil {
		return err
	}
	prefers, err := json.Marshal(c.Discovery.Prefers)
	if err != nil {
		return err
	}
	cardJSON, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = tx.Exec(
		`INSERT INTO cards
		   (id, name, status, tier, classes, tools, skill_vec, embed_model, signals, prefers, card_json, born, revised)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   name = excluded.name, status = excluded.status, tier = excluded.tier,
		   classes = excluded.classes, tools = excluded.tools, skill_vec = excluded.skill_vec,
		   embed_model = excluded.embed_model, signals = excluded.signals, prefers = excluded.prefers,
		   card_json = excluded.card_json, revised = excluded.revised`,
		c.ID, c.Name, c.Status, c.Tier, string(classes), string(tools),
		encodeVec(vec), model, string(signals), string(prefers), string(cardJSON),
		c.Born.Unix(), c.Revised.Unix())
	return err
}

// encodeVec packs a float32 vector little-endian. It always returns a
// non-nil slice so the NOT NULL skill_vec column takes an empty blob rather
// than SQL NULL for an unembedded card.
func encodeVec(vec []float32) []byte {
	b := make([]byte, 4*len(vec))
	for i, f := range vec {
		binary.LittleEndian.PutUint32(b[4*i:], math.Float32bits(f))
	}
	return b
}

// decodeVec is the inverse of encodeVec; a zero-length blob decodes to a nil
// vector, the unembedded state.
func decodeVec(b []byte) []float32 {
	if len(b) < 4 {
		return nil
	}
	vec := make([]float32, len(b)/4)
	for i := range vec {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return vec
}

// antDir is the per-ant directory, <antsDir>/<name>-<id>, holding card.json
// and SKILL.md.
func antDir(antsDir, name, id string) string {
	return filepath.Join(antsDir, fmt.Sprintf("%s-%s", name, id))
}

// writeCardFiles writes card.json and SKILL.md for a card, creating the ant
// directory. card.json is pretty-printed so it diffs cleanly in git.
func writeCardFiles(antsDir string, c Card) error {
	dir := antDir(antsDir, c.Name, c.ID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, "card.json"), data, 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(renderSkill(c)), 0o644)
}

// readCardFile reads card.json for the ant, the authoritative source for
// authored content.
func readCardFile(antsDir, name, id string) (Card, error) {
	data, err := os.ReadFile(filepath.Join(antDir(antsDir, name, id), "card.json"))
	if err != nil {
		return Card{}, err
	}
	var c Card
	if err := json.Unmarshal(data, &c); err != nil {
		return Card{}, fmt.Errorf("card %s: card.json is not valid: %w", id, err)
	}
	return c, nil
}
