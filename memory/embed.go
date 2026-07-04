package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/tamnd/ari/memory/sqlite"
)

// Embedder turns text into a float32 vector for hybrid recall. Two shapes
// satisfy it, chosen by config: an OpenAI-compatible endpoint and the null
// embedder. The product promise is that memory works with no endpoint and
// works better with one, never that it needs one (D10).
type Embedder interface {
	// Configured reports whether a real endpoint backs this embedder. When
	// it is false the store runs FTS-only and no row carries a vector.
	Configured() bool

	// Model is the tag stamped on every row this embedder vectorizes, the
	// hook the lazy re-embed pass keys on when the endpoint or model changes
	// (D17, recommendation 14).
	Model() string

	// Embed returns a vector for text, or an error when a configured endpoint
	// is unreachable. The null embedder returns (nil, nil): no vector and no
	// error, because having no endpoint is a valid state, not a failure.
	Embed(ctx context.Context, text string) ([]float32, error)
}

// NullEmbedder is the no-endpoint path. Recall falls back to BM25 over FTS5,
// which is strong on its own for the identifier-heavy queries a coding memory
// answers, which is exactly why D10 weights BM25 high; so memory is useful
// with nothing configured at all.
type NullEmbedder struct{}

func (NullEmbedder) Configured() bool                                 { return false }
func (NullEmbedder) Model() string                                    { return "" }
func (NullEmbedder) Embed(context.Context, string) ([]float32, error) { return nil, nil }

// OpenAIEmbedder calls the /v1/embeddings endpoint, the sibling of the chat
// shape the provider layer already speaks, which covers Ollama, llama.cpp
// with --embeddings, LM Studio, and the tailnet box.
type OpenAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
	client  *http.Client
}

// NewOpenAIEmbedder builds the endpoint-backed embedder. Local endpoints pass
// an empty key. A dim of zero disables the dimension check; a positive dim is
// asserted against every returned vector so a wrong model is caught at the
// first call rather than as silently bad recall.
func NewOpenAIEmbedder(baseURL, apiKey, model string, dim int) *OpenAIEmbedder {
	return &OpenAIEmbedder{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		dim:     dim,
		client:  &http.Client{},
	}
}

func (e *OpenAIEmbedder) Configured() bool { return true }
func (e *OpenAIEmbedder) Model() string    { return e.model }

type embedRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embedResponse struct {
	Data []struct {
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed posts one input and returns its vector. Every failure is an error the
// caller decides how to treat; Vectorize turns it into a degraded FTS-only
// write, and the re-embed pass leaves the row for the next fold.
func (e *OpenAIEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	body, err := json.Marshal(embedRequest{Model: e.model, Input: []string{text}})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if e.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.apiKey)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embed endpoint unreachable: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("embed endpoint returned %s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	var out embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decoding embed response: %w", err)
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("embed endpoint returned no vector")
	}
	vec := out.Data[0].Embedding
	if e.dim > 0 && len(vec) != e.dim {
		return nil, fmt.Errorf("embed endpoint returned a %d-dim vector, config expects %d: check the model", len(vec), e.dim)
	}
	return vec, nil
}

// Vectorize embeds text for a memory write, degrading to no vector when the
// endpoint is down so a flaky endpoint never drops a memory: the row lands
// FTS-only with a NULL model tag, and the next fold's re-embed pass picks it
// up because NULL does not match the configured model. It returns the vector
// and the model tag to stamp, both zero when there is no vector.
func Vectorize(ctx context.Context, e Embedder, text string) (vec []float32, model string) {
	if !e.Configured() {
		return nil, ""
	}
	v, err := e.Embed(ctx, text)
	if err != nil || len(v) == 0 {
		return nil, ""
	}
	return v, e.Model()
}

// ReEmbedStale refreshes every row whose embed_model tag does not match the
// configured embedder, the lazy migration D17 names: change the endpoint or
// the model and rows re-embed at the next fold, no flag-day rewrite. A null
// embedder is a no-op, so a machine with no endpoint never churns, and a row
// the endpoint still cannot embed is left for the fold after this one. It
// returns how many rows it refreshed.
func ReEmbedStale(ctx context.Context, s *sqlite.Store, e Embedder) (int, error) {
	if !e.Configured() {
		return 0, nil
	}
	stale, err := s.StaleEmbeddings(ctx, e.Model(), 0)
	if err != nil {
		return 0, err
	}
	var n int
	for _, m := range stale {
		vec, err := e.Embed(ctx, embedText(m))
		if err != nil || len(vec) == 0 {
			continue
		}
		if err := s.SetEmbedding(ctx, m.ID, vec, e.Model()); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// embedText is the text an embedding is taken over: the label and body
// together, the same fields FTS5 indexes, so vector and lexical recall see
// the same content.
func embedText(m sqlite.Memory) string {
	if m.Label == "" {
		return m.Body
	}
	return m.Label + "\n" + m.Body
}
