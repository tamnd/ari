package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"math"
	"sort"
	"strings"
	"time"
)

// Recall constants. The candidate pool bounds how many FTS matches feed the
// fusion, and the cosine window bounds how many same-namespace rows the
// brute-force vector scan touches, so a hot query never scans the whole table
// even as it grows toward the hundred-thousand-row ceiling D10 sets.
const (
	ftsPool      = 200
	cosineWindow = 500
	rrfK         = 60  // the reciprocal-rank-fusion constant, the paper's 60
	rrfWFTS      = 1.0 // BM25 weighted above cosine: coding queries are identifier-heavy
	rrfWVec      = 0.7
)

// Park's alphas. They default to one apiece as in the generative-agents
// paper; the ship-gate fixtures (slice 12) tune them through config. Recency
// decays per day rather than per hour because a codebase moves slower than a
// simulated town (recommendation 5).
var (
	parkWRecency    = 1.0
	parkWImportance = 1.0
	parkWRelevance  = 1.0
	// recencyHalfLifeDays sets the per-day decay base for a normal row: one not
	// touched in this many days has half the recency of one touched today,
	// before min-max normalization across the candidate set. A fast row decays
	// far quicker and a pinned row does not decay at all, so the evaporation
	// clock is the ttl_class, not one rate for every row (doc 07, D11).
	recencyHalfLifeDays = 30.0
	fastHalfLifeDays    = 1.0
)

// staleWeight is the factor a demoted row's Park score is multiplied by, so a
// memory the invalidation pass marked stale sinks below its live peers without
// being dropped. The demotion is reversible: clearing the flag restores the
// full score (D11, research recommendation 6).
const staleWeight = 0.5

// halfLifeDays is the recency half-life for a ttl class. A pinned row returns
// positive infinity, which makes its recency term a constant one: it never
// evaporates, because the consolidator re-justifies it at each fold instead.
func halfLifeDays(ttlClass string) float64 {
	switch ttlClass {
	case TTLFast, TTLSession:
		return fastHalfLifeDays
	case TTLPinned:
		return math.Inf(1)
	default:
		return recencyHalfLifeDays
	}
}

// scored carries a candidate row through fusion and ranking: its raw Park
// components before normalization, and the final Park score after.
type scored struct {
	m          Memory
	relevance  float64 // RRF-fused rank, the relevance term
	recencyRaw float64 // decay of accessed_at, pre-normalization
	park       float64
}

// Recall runs hybrid recall for one query in one namespace and returns the
// rows worth surfacing, most relevant first, within budget. It runs the cheap
// FTS5 BM25 query first, scopes cosine to the FTS matches plus a bounded
// same-namespace window, fuses the two ranked lists by reciprocal rank
// fusion, ranks the survivors by Park's triple, and bumps the access stats of
// what it returns so a recalled memory stays fresh (the pheromone deposit of
// research section 4). A nil queryVec skips the cosine stage, so the same code
// path serves the FTS-only world without a branch the caller must know about.
// Archived rows and rows outside ns never enter scoring, so a forgotten
// memory does not resurface and one ant's recall never leaks another's.
func (s *Store) Recall(ctx context.Context, ns, query string, queryVec []float32, budget int) ([]Memory, error) {
	return s.recallAt(ctx, ns, query, queryVec, budget, time.Now())
}

func (s *Store) recallAt(ctx context.Context, ns, query string, queryVec []float32, budget int, now time.Time) ([]Memory, error) {
	if budget <= 0 {
		budget = 10
	}
	ftsRanked, err := s.ftsSearch(ctx, ns, query)
	if err != nil {
		return nil, err
	}
	var vecRanked []string
	if len(queryVec) > 0 {
		vecRanked, err = s.cosineSearch(ctx, ns, queryVec, ftsRanked)
		if err != nil {
			return nil, err
		}
	}

	relevance := fuseRRF(ftsRanked, vecRanked)
	if len(relevance) == 0 {
		return nil, nil
	}
	ids := make([]string, 0, len(relevance))
	for id := range relevance {
		ids = append(ids, id)
	}
	rows, err := s.loadMemories(ctx, ids)
	if err != nil {
		return nil, err
	}

	cands := make([]scored, 0, len(rows))
	for _, m := range rows {
		age := now.Sub(time.Unix(m.AccessedAt, 0)).Hours() / 24
		if age < 0 {
			age = 0
		}
		half := halfLifeDays(m.TTLClass)
		recencyRaw := 1.0 // a pinned row's half-life is infinite: no decay
		if !math.IsInf(half, 1) {
			recencyRaw = math.Pow(0.5, age/half)
		}
		cands = append(cands, scored{
			m:          m,
			relevance:  relevance[m.ID],
			recencyRaw: recencyRaw,
		})
	}
	rankByPark(cands)

	if len(cands) > budget {
		cands = cands[:budget]
	}
	out := make([]Memory, len(cands))
	returned := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.m
		returned[i] = c.m.ID
	}
	if err := s.bumpAccess(ctx, returned, now); err != nil {
		return nil, err
	}
	return out, nil
}

// ftsSearch runs the BM25 query over memories_fts scoped to ns, returning the
// matching ids best first. The query is built as an OR of quoted tokens so a
// token with a dot or slash (schema.go, pkg/foo) is matched literally rather
// than parsed as FTS5 syntax, the same guarded quoting slice 2 introduced.
func (s *Store) ftsSearch(ctx context.Context, ns, query string) ([]string, error) {
	match := ftsQuery(query)
	if match == "" {
		return nil, nil
	}
	var ids []string
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `
			SELECT m.id
			FROM memories_fts f
			JOIN memories m ON m.rowid = f.rowid
			WHERE memories_fts MATCH ? AND m.namespace = ? AND m.archived_at IS NULL
			ORDER BY bm25(memories_fts)
			LIMIT ?`, match, ns, ftsPool)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			ids = append(ids, id)
		}
		return rows.Err()
	})
	return ids, err
}

// ftsQuery turns a natural-language query into an FTS5 MATCH expression: each
// whitespace token is quoted as a phrase and the phrases are OR'd, so any term
// contributes to the BM25 rank and punctuation in a token is inert.
func ftsQuery(query string) string {
	fields := strings.Fields(query)
	terms := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.Trim(f, `"`)
		if f == "" {
			continue
		}
		terms = append(terms, `"`+f+`"`)
	}
	return strings.Join(terms, " OR ")
}

// cosineSearch computes cosine similarity between queryVec and the embeddings
// of a bounded candidate set: the FTS matches plus the most recently accessed
// same-namespace rows that carry a vector. It returns the candidate ids in
// descending similarity, the vector half of the fusion.
func (s *Store) cosineSearch(ctx context.Context, ns string, queryVec []float32, ftsIDs []string) ([]string, error) {
	want := make(map[string]struct{}, len(ftsIDs)+cosineWindow)
	for _, id := range ftsIDs {
		want[id] = struct{}{}
	}
	type sim struct {
		id    string
		score float64
	}
	var sims []sim
	err := s.Read(ctx, func(db *sql.DB) error {
		// The recent same-namespace window with vectors, unioned with any FTS
		// match not already in that window, keeps the scan bounded while still
		// scoring every keyword hit.
		rows, err := db.QueryContext(ctx, `
			SELECT id, embedding FROM memories
			WHERE namespace = ? AND archived_at IS NULL AND embedding IS NOT NULL
			ORDER BY accessed_at DESC
			LIMIT ?`, ns, cosineWindow)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		seen := make(map[string]struct{})
		for rows.Next() {
			var id string
			var blob []byte
			if err := rows.Scan(&id, &blob); err != nil {
				return err
			}
			seen[id] = struct{}{}
			delete(want, id)
			sims = append(sims, sim{id: id, score: cosine(queryVec, decodeVector(blob))})
		}
		if err := rows.Err(); err != nil {
			return err
		}
		// Pull the FTS matches the window missed, so a keyword hit that is old
		// still gets a cosine rank.
		for id := range want {
			var blob []byte
			err := db.QueryRowContext(ctx,
				`SELECT embedding FROM memories WHERE id = ? AND embedding IS NOT NULL`, id).Scan(&blob)
			if err == sql.ErrNoRows {
				continue
			}
			if err != nil {
				return err
			}
			if _, ok := seen[id]; ok {
				continue
			}
			sims = append(sims, sim{id: id, score: cosine(queryVec, decodeVector(blob))})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(sims, func(i, j int) bool { return sims[i].score > sims[j].score })
	ids := make([]string, len(sims))
	for i, s := range sims {
		ids[i] = s.id
	}
	return ids, nil
}

// fuseRRF fuses two ranked id lists by reciprocal rank fusion, the relevance
// term Park ranks on. A row present in only one list scores from that list
// alone, so an FTS-only match and a vector-only match both survive fusion,
// and when the vector list is empty RRF degenerates to the BM25 ranking.
func fuseRRF(fts, vec []string) map[string]float64 {
	score := make(map[string]float64)
	for rank, id := range fts {
		score[id] += rrfWFTS / float64(rrfK+rank+1)
	}
	for rank, id := range vec {
		score[id] += rrfWVec / float64(rrfK+rank+1)
	}
	return score
}

// rankByPark min-max normalizes the three Park components across the
// candidate set and sorts by the weighted sum, highest first. Normalizing
// over the live candidates rather than absolute scales keeps one loud
// component from swamping the others regardless of the query's shape.
func rankByPark(cands []scored) {
	relLo, relHi := spanOf(cands, func(c scored) float64 { return c.relevance })
	recLo, recHi := spanOf(cands, func(c scored) float64 { return c.recencyRaw })
	impLo, impHi := spanOf(cands, func(c scored) float64 { return float64(c.m.Importance) })
	for i := range cands {
		rel := norm(cands[i].relevance, relLo, relHi)
		rec := norm(cands[i].recencyRaw, recLo, recHi)
		imp := norm(float64(cands[i].m.Importance), impLo, impHi)
		cands[i].park = parkWRelevance*rel + parkWRecency*rec + parkWImportance*imp
		if cands[i].m.Stale {
			cands[i].park *= staleWeight
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].park != cands[j].park {
			return cands[i].park > cands[j].park
		}
		return cands[i].m.ID > cands[j].m.ID // newer ULID first on a tie
	})
}

func spanOf(cands []scored, f func(scored) float64) (lo, hi float64) {
	lo, hi = math.Inf(1), math.Inf(-1)
	for _, c := range cands {
		v := f(c)
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
	}
	return lo, hi
}

// norm maps v into [0,1] against the candidate span. When the span is zero the
// component cannot discriminate, so every row gets zero from it and the other
// two components decide the order.
func norm(v, lo, hi float64) float64 {
	if hi <= lo {
		return 0
	}
	return (v - lo) / (hi - lo)
}

// bumpAccess refreshes accessed_at and increments access_count for the
// returned rows through the single writer, the pheromone deposit: a memory
// that keeps getting recalled stays fresh, an unused one decays.
func (s *Store) bumpAccess(ctx context.Context, ids []string, now time.Time) error {
	if len(ids) == 0 {
		return nil
	}
	return s.Write(ctx, func(tx *sql.Tx) error {
		ph, args := placeholders(ids)
		args = append([]any{now.Unix()}, args...)
		_, err := tx.ExecContext(ctx,
			`UPDATE memories SET accessed_at = ?, access_count = access_count + 1 WHERE id IN (`+ph+`)`,
			args...)
		return err
	})
}

// loadMemories reads the full rows for a set of ids, everything the caller
// needs to render a recall result except the embedding blob, which recall has
// already consumed.
func (s *Store) loadMemories(ctx context.Context, ids []string) ([]Memory, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	ph, args := placeholders(ids)
	var out []Memory
	err := s.Read(ctx, func(db *sql.DB) error {
		rows, err := db.QueryContext(ctx, `
			SELECT id, namespace, kind, label, body, COALESCE(embed_model, ''),
			       importance, created_at, accessed_at, access_count,
			       source_ant, COALESCE(source_task, ''), COALESCE(anchor_commit, ''),
			       ttl_class, read_only, pinned, stale
			FROM memories WHERE id IN (`+ph+`)`, args...)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var m Memory
			var readOnly, pinned, stale int
			if err := rows.Scan(
				&m.ID, &m.Namespace, &m.Kind, &m.Label, &m.Body, &m.EmbedModel,
				&m.Importance, &m.CreatedAt, &m.AccessedAt, &m.AccessCount,
				&m.SourceAnt, &m.SourceTask, &m.AnchorCommit,
				&m.TTLClass, &readOnly, &pinned, &stale,
			); err != nil {
				return err
			}
			m.ReadOnly = readOnly == 1
			m.Pinned = pinned == 1
			m.Stale = stale == 1
			out = append(out, m)
		}
		return rows.Err()
	})
	return out, err
}

// placeholders builds an IN clause's "?, ?, ..." and its args from a slice of
// ids.
func placeholders(ids []string) (string, []any) {
	marks := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		marks[i] = "?"
		args[i] = id
	}
	return strings.Join(marks, ", "), args
}

// cosine is the similarity between two vectors, zero when either is empty or
// degenerate so a missing or zero embedding never poisons the ranking.
func cosine(a, b []float32) float64 {
	if len(a) == 0 || len(a) != len(b) {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

// decodeVector unpacks a little-endian float32 BLOB written by encodeVector.
func decodeVector(b []byte) []float32 {
	if len(b)%4 != 0 {
		return nil
	}
	v := make([]float32, len(b)/4)
	for i := range v {
		v[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[i*4:]))
	}
	return v
}
