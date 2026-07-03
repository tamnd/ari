package core

import (
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/anthropic"
	"github.com/tamnd/ari/provider/openai"
)

// BuildRegistry turns validated config into live providers and tier
// chains. Config validation already refused unknown kinds and dangling
// references, so a failure here means the config changed underneath us
// and is worth an ErrConfig, not a panic (plan/01 slice 3, D17).
func BuildRegistry(cfg *config.Config) (*provider.Registry, error) {
	reg := provider.NewRegistry()
	for name, p := range cfg.Providers {
		switch p.Kind {
		case "anthropic":
			reg.AddProvider(anthropic.New(name, p.BaseURL, p.APIKey))
		case "openai":
			reg.AddProvider(openai.New(name, p.BaseURL, p.APIKey))
		default:
			return nil, Errf(ErrConfig, "provider %q has unknown kind %q", name, p.Kind)
		}
	}
	for name, t := range cfg.Tiers {
		links := make([]provider.ChainLink, len(t.Chain))
		for i, tg := range t.Chain {
			links[i] = provider.ChainLink{Provider: tg.Provider, Model: tg.Model, TTL: tg.TTL}
		}
		if err := reg.AddTier(name, links); err != nil {
			return nil, Wrap(ErrConfig, err, "building tier chains")
		}
	}
	return reg, nil
}
