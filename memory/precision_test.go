package memory

import (
	"context"
	"encoding/json"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

// This is one of the four M2 ship-gate fixtures (spec 2085 slice 12): the
// recall precision gate. It seeds a corpus of topically distinct coding
// memories, then for every authored query asserts the owning row comes back
// first. It runs the recall twice, once with vectors on (the hybrid path) and
// once with the query vector nil (the FTS-only path), and holds each to its own
// precision-at-one floor. The corpus and thresholds are the regression tripwire:
// a change to ranking, fusion, or FTS quoting that drops a query below the floor
// fails the build.
//
// Everything runs offline. The embedder is a deterministic hashing bag of words
// (no network, no model), so CI reproduces the same vectors on every machine.

// corpusRow is one seeded memory and the queries that should recall it first.
type corpusRow struct {
	ID      string   `json:"id"`
	Label   string   `json:"label"`
	Body    string   `json:"body"`
	File    string   `json:"file"`
	Queries []string `json:"queries"`
}

type corpus struct {
	Rows []corpusRow `json:"rows"`
}

// hashingVec is a deterministic embedding: it tokenizes to lowercase words of
// length three or more and hashes each into a fixed-width bag of words. Two
// texts that share rare words land close in cosine space, which is all the
// hybrid path needs to reinforce the lexical match. It is pure and offline, so
// the fixture never depends on an embedding endpoint.
const hashDim = 128

func hashingVec(text string) []float32 {
	v := make([]float32, hashDim)
	alnum := func(r rune) bool { return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') }
	for _, tok := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !alnum(r) }) {
		if len(tok) < 3 {
			continue
		}
		h := fnv.New32a()
		_, _ = h.Write([]byte(tok))
		v[h.Sum32()%hashDim]++
	}
	return v
}

func loadCorpus(t *testing.T) corpus {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "precision_corpus.json"))
	if err != nil {
		t.Fatalf("read corpus: %v", err)
	}
	var c corpus
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatalf("parse corpus: %v", err)
	}
	return c
}

// seedCorpus writes every row as one live memory in the shared namespace, with
// the hashing embedding and uniform importance, so the recency and importance
// terms of Park's triple contribute nothing and relevance alone decides the
// order. That isolates what this fixture measures: lexical and vector match
// quality, not decay. The rows are pinned on purpose: a pinned row's recency
// term is a constant one, so the access bump every recall lands does not make an
// earlier query's hits look fresher to a later query and skew the ranking.
func seedCorpus(t *testing.T, s *sqlite.Store, c corpus) {
	t.Helper()
	ctx := context.Background()
	const at = 1_700_000_000
	for _, r := range c.Rows {
		m := sqlite.Memory{
			ID: r.ID, Namespace: "ant_worker", Kind: sqlite.KindObservation,
			Label: r.Label, Body: r.Body, Importance: 5,
			CreatedAt: at, AccessedAt: at, SourceAnt: "worker", TTLClass: sqlite.TTLPinned, Pinned: true,
			Embedding: hashingVec(r.Label + "\n" + r.Body), EmbedModel: "fixture-hash",
		}
		var anchors []sqlite.Anchor
		if r.File != "" {
			anchors = []sqlite.Anchor{{Kind: "file", Ref: r.File}}
		}
		if err := s.InsertMemory(ctx, m, anchors, nil); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}
}

// precisionAt1 runs every query and reports the fraction whose top hit is the
// expected row. withVectors picks the path: true fuses BM25 and cosine, false
// passes a nil query vector so recall degenerates to BM25 alone.
func precisionAt1(t *testing.T, s *sqlite.Store, c corpus, withVectors bool) (float64, int, []string) {
	t.Helper()
	ctx := context.Background()
	var pairs, hits int
	var misses []string
	for _, r := range c.Rows {
		for _, q := range r.Queries {
			pairs++
			var qv []float32
			if withVectors {
				qv = hashingVec(q)
			}
			out, err := s.Recall(ctx, "ant_worker", q, qv, 5)
			if err != nil {
				t.Fatalf("recall %q: %v", q, err)
			}
			if len(out) > 0 && out[0].ID == r.ID {
				hits++
			} else {
				top := "none"
				if len(out) > 0 {
					top = out[0].ID
				}
				misses = append(misses, r.ID+" <- "+q+" (got "+top+")")
			}
		}
	}
	return float64(hits) / float64(pairs), pairs, misses
}

// TestRecallPrecisionHybrid: the hybrid path clears the higher floor. Fusing the
// vector rank onto BM25 must not knock the right row off the top.
func TestRecallPrecisionHybrid(t *testing.T) {
	s := store(t)
	c := loadCorpus(t)
	seedCorpus(t, s, c)

	p, pairs, misses := precisionAt1(t, s, c, true)
	if pairs < 50 {
		t.Fatalf("corpus has %d query pairs, the gate needs at least 50", pairs)
	}
	if p <= 0.8 {
		t.Fatalf("hybrid precision@1 = %.3f over %d pairs, want > 0.8\nmisses:\n  %s",
			p, pairs, strings.Join(misses, "\n  "))
	}
}

// TestRecallPrecisionFTSOnly: with no query vector recall falls back to BM25,
// and the lexical match alone still clears the FTS-only floor. This is the path
// a machine with no embedding endpoint runs.
func TestRecallPrecisionFTSOnly(t *testing.T) {
	s := store(t)
	c := loadCorpus(t)
	seedCorpus(t, s, c)

	p, pairs, misses := precisionAt1(t, s, c, false)
	if pairs < 50 {
		t.Fatalf("corpus has %d query pairs, the gate needs at least 50", pairs)
	}
	if p <= 0.7 {
		t.Fatalf("fts-only precision@1 = %.3f over %d pairs, want > 0.7\nmisses:\n  %s",
			p, pairs, strings.Join(misses, "\n  "))
	}
}
