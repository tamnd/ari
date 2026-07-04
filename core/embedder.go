package core

import (
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/memory"
)

// BuildEmbedder resolves the configured embedder from validated config: the
// named embeddings provider gives an OpenAI-compatible embedder, and an empty
// provider gives the null embedder so recall runs FTS-only. Config validation
// already refused an embeddings block that names a provider it never defined,
// so the lookup here is total (plan/03 slice 3, D10, D17).
func BuildEmbedder(cfg *config.Config) memory.Embedder {
	e := cfg.Embeddings
	if e.Provider == "" {
		return memory.NullEmbedder{}
	}
	p := cfg.Providers[e.Provider]
	return memory.NewOpenAIEmbedder(p.BaseURL, p.APIKey, e.Model, e.Dim)
}
