package provider

import (
	"fmt"
	"sort"
	"strings"
)

// Target is one resolved link in a tier's failover chain: a live provider,
// the model to ask it for, and the cache TTL hint for that target.
type Target struct {
	Provider Provider
	Model    string
	TTL      string
}

// Registry resolves tier names to failover chains of live providers. An
// ant card names a tier, never a model, so a model swap is a config edit
// and the loop never learns a vendor name (D17, doc 10 section 6).
type Registry struct {
	providers map[string]Provider
	tiers     map[string][]Target
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: map[string]Provider{},
		tiers:     map[string][]Target{},
	}
}

// AddProvider registers a live provider under its name.
func (r *Registry) AddProvider(p Provider) {
	r.providers[p.Name()] = p
}

// Provider looks up a registered provider by name.
func (r *Registry) Provider(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// ChainLink is one unresolved link: provider by name, model, cache TTL.
// It mirrors the config shape without importing config.
type ChainLink struct {
	Provider, Model, TTL string
}

// AddTier installs a tier's chain. Every link must reference a provider
// already registered; the error names the missing one so a config typo
// reads as itself, not as a panic later (plan/01 slice 3).
func (r *Registry) AddTier(name string, chain []ChainLink) error {
	targets := make([]Target, 0, len(chain))
	for i, l := range chain {
		p, ok := r.providers[l.Provider]
		if !ok {
			return fmt.Errorf("tier %q link %d: provider %q is not registered", name, i, l.Provider)
		}
		if l.Model == "" {
			return fmt.Errorf("tier %q link %d (%s): empty model", name, i, l.Provider)
		}
		targets = append(targets, Target{Provider: p, Model: l.Model, TTL: l.TTL})
	}
	r.tiers[name] = targets
	return nil
}

// Resolve returns a tier's chain, tried in order until one target answers.
// An unknown tier is a clean error listing what exists.
func (r *Registry) Resolve(tier string) ([]Target, error) {
	chain, ok := r.tiers[tier]
	if !ok {
		known := make([]string, 0, len(r.tiers))
		for k := range r.tiers {
			known = append(known, k)
		}
		sort.Strings(known)
		return nil, fmt.Errorf("unknown tier %q, configured tiers: %s", tier, strings.Join(known, ", "))
	}
	if len(chain) == 0 {
		return nil, fmt.Errorf("tier %q has an empty chain", tier)
	}
	return chain, nil
}

// Tiers lists the configured tier names, sorted, for doctor output.
func (r *Registry) Tiers() []string {
	out := make([]string, 0, len(r.tiers))
	for k := range r.tiers {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
