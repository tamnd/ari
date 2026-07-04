package core

import (
	"testing"

	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/memory"
)

// TestBuildEmbedderNullWhenNoProvider is the FTS-only path: an empty
// embeddings provider resolves to the null embedder, so a machine that clears
// the block runs memory with no endpoint.
func TestBuildEmbedderNullWhenNoProvider(t *testing.T) {
	cfg := &config.Config{
		Providers:  map[string]config.Provider{},
		Embeddings: config.Embeddings{Provider: ""},
	}
	e := BuildEmbedder(cfg)
	if e.Configured() {
		t.Fatalf("empty embeddings provider built a configured embedder, want null")
	}
	if _, ok := e.(memory.NullEmbedder); !ok {
		t.Fatalf("embedder = %T, want memory.NullEmbedder", e)
	}
}

// TestBuildEmbedderOpenAIWhenConfigured is the endpoint path: a named
// provider with a model resolves to a configured embedder carrying that
// model tag.
func TestBuildEmbedderOpenAIWhenConfigured(t *testing.T) {
	cfg := &config.Config{
		Providers: map[string]config.Provider{
			"ollama": {Kind: "openai", BaseURL: "http://localhost:11434/v1"},
		},
		Embeddings: config.Embeddings{Provider: "ollama", Model: "nomic-embed-text", Dim: 768},
	}
	e := BuildEmbedder(cfg)
	if !e.Configured() {
		t.Fatalf("configured embeddings provider built an unconfigured embedder")
	}
	if e.Model() != "nomic-embed-text" {
		t.Fatalf("model = %q, want nomic-embed-text", e.Model())
	}
}
