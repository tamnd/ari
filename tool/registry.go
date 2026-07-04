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
	// deferred names a tool whose schema is withheld from the model until a
	// search-and-load step loads it. A deferred tool is registered and
	// resolvable, but it stays out of the schema listing so turn one never
	// carries its schema; this is how MCP tools ride without the up-front
	// context tax (doc 13, D20).
	deferred map[string]bool
	// loaded names a deferred tool the model has loaded this session; once
	// loaded it stays loaded, so a repeatedly used tool pays its schema
	// cost once.
	loaded map[string]bool
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		byName:   make(map[string]Tool),
		deferred: make(map[string]bool),
		loaded:   make(map[string]bool),
	}
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

// RegisterDeferred adds a tool whose schema is withheld until the model
// loads it. The tool is announced by name only and becomes callable after
// Load, so a server with fifty tools the ant never touches costs a line of
// names and nothing more (doc 13, D20).
func (r *Registry) RegisterDeferred(t Tool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.byName[t.Name()]; dup {
		return fmt.Errorf("tool %q already registered", t.Name())
	}
	r.byName[t.Name()] = t
	r.deferred[t.Name()] = true
	return nil
}

// Load marks deferred tools loaded and returns the ones that were newly
// loaded, so the search step can echo their schemas to the model. A name
// that is not a deferred tool is ignored, so a bad query loads nothing.
func (r *Registry) Load(names ...string) []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var newly []Tool
	for _, n := range names {
		if r.deferred[n] && !r.loaded[n] {
			r.loaded[n] = true
			newly = append(newly, r.byName[n])
		}
	}
	return newly
}

// DeferredTools lists the deferred tools not yet loaded, sorted by name, so
// the ant can announce them by name and the search step can match a query
// against them.
func (r *Registry) DeferredTools() []Tool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var out []Tool
	for name := range r.deferred {
		if !r.loaded[name] {
			out = append(out, r.byName[name])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
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

// SchemaNames lists the tools whose schema the model may see this turn:
// every non-deferred tool plus any deferred tool already loaded. A deferred
// tool not yet loaded is absent, so its schema never rides the request.
func (r *Registry) SchemaNames() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.byName))
	for n := range r.byName {
		if r.deferred[n] && !r.loaded[n] {
			continue
		}
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
			if r.deferred[name] {
				sub.deferred[name] = true
			}
			if r.loaded[name] {
				sub.loaded[name] = true
			}
		}
	}
	return sub
}
