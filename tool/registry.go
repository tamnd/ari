package tool

import (
	"fmt"
	"sort"
	"sync"
)

// Registry resolves a tool by the name the model calls. The six core
// tools are registered at startup; skills and MCP tools attach later
// (doc 13) through the same Register call, so nothing about the
// registry knows they are special. It is deliberately dumb: it maps
// names to tools, rejects duplicates, filters to an ant's allowlist,
// and does nothing else (doc 04 section 13.1).
type Registry struct {
	mu     sync.RWMutex
	byName map[string]Tool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{byName: make(map[string]Tool)}
}

// Register adds a tool, refusing a duplicate name.
func (r *Registry) Register(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[t.Name()]; dup {
		return fmt.Errorf("tool %q already registered", t.Name())
	}
	r.byName[t.Name()] = t
	return nil
}

// Resolve returns the tool the model named.
func (r *Registry) Resolve(name string) (Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.byName[name]
	return t, ok
}

// Names lists the registered tools, sorted, for prompts and errors.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ForAllowlist returns the subset a worker ant may call, filtered by
// its card's Commands allowlist (doc 04 section 12.1). A tool absent
// from the allowlist is not in the returned registry, so the model
// never sees it in the tool list and cannot call it.
func (r *Registry) ForAllowlist(allowed []string) *Registry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	sub := NewRegistry()
	for _, name := range allowed {
		if t, ok := r.byName[name]; ok {
			sub.byName[name] = t
		}
	}
	return sub
}
