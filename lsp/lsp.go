// Package lsp is the language-server seam: one client interface, one
// JSON-RPC-over-stdio transport, and an adapter registry with gopls as
// the single M1 adapter. Its whole job in M1 is the diagnostics feedback
// loop for the edit and write tools (doc 04, non-goal N5); a second
// server later is a config row plus a small adapter, never a change to
// this interface (plan 02 slice 5).
//
// The client is off unless a run enables it, matching the
// disabled-by-default posture a language server's process cost earns
// (doc 13, research/claude_code_opencode.md B.3). When it is off, or when
// no adapter matches a path, or when the server is absent or slow, every
// operation degrades to zero diagnostics and never fails the edit.
package lsp

import (
	"context"
	"path/filepath"
	"strings"
)

// TouchKind tells the client how hard to sync a file with the server: a
// light document update after a surgical edit, or a fuller save-level
// sync after a whole-file write.
type TouchKind int

const (
	// TouchDocument is the light update an edit sends: didChange only.
	TouchDocument TouchKind = iota
	// TouchFull is the fuller sync a write sends: didChange plus didSave.
	TouchFull
)

func (k TouchKind) String() string {
	switch k {
	case TouchFull:
		return "full"
	default:
		return "document"
	}
}

// Diagnostic is one finding the server reported, normalized to the
// 1-based line and column the model-facing "ERROR [line:col] message"
// wants. Only error-severity diagnostics ride back to the model in M1;
// the store keeps every severity so the UI can count warnings too.
type Diagnostic struct {
	Line, Col int    // 1-based
	Severity  string // "error", "warning", "info", or "hint"
	Message   string
}

// LSPClient is the seam doc 04's edit and write tools call. They touch a
// file after changing it and read its diagnostics; they never speak to a
// specific server. gopls is one adapter behind this interface.
type LSPClient interface {
	// Touch opens or updates the document with the matching server and
	// waits, bounded, for diagnostics to settle. It returns nil when no
	// adapter matches, when the server is absent, or when the wait times
	// out: a missing or slow server is zero diagnostics, not a failed edit.
	Touch(ctx context.Context, path string, kind TouchKind) error

	// Diagnostics returns the current error-severity diagnostics for a
	// path, capped, empty when the server is absent or has not reported.
	Diagnostics(path string) []Diagnostic
}

// Adapter maps a language to its server binary and the file extensions it
// owns. gopls is the only one wired in M1; a second is another value in
// the registry (plan 02 slice 5).
type Adapter struct {
	Name        string   // stable id, also the process key ("gopls")
	Command     string   // binary looked up on PATH
	Args        []string // extra launch args
	Extensions  []string // ".go"; a leading dot, lowercased
	RootMarkers []string // "go.work", "go.mod"; the first found sets the root
	LanguageID  string   // the LSP languageId sent on didOpen ("go")
}

// GoplsAdapter is the built-in Go adapter. It discovers gopls on PATH and
// degrades cleanly to no diagnostics when gopls is absent.
func GoplsAdapter() Adapter {
	return Adapter{
		Name:        "gopls",
		Command:     "gopls",
		Extensions:  []string{".go"},
		RootMarkers: []string{"go.work", "go.mod"},
		LanguageID:  "go",
	}
}

// Registry resolves a path to the adapter that owns its extension.
type Registry struct {
	byExt    map[string]Adapter
	adapters []Adapter
}

// NewRegistry builds a registry from the given adapters, last write wins
// on a shared extension.
func NewRegistry(adapters ...Adapter) *Registry {
	r := &Registry{byExt: map[string]Adapter{}}
	for _, a := range adapters {
		r.Add(a)
	}
	return r
}

// DefaultRegistry is the M1 registry: gopls and nothing else.
func DefaultRegistry() *Registry { return NewRegistry(GoplsAdapter()) }

// Add registers an adapter for each of its extensions.
func (r *Registry) Add(a Adapter) {
	r.adapters = append(r.adapters, a)
	for _, ext := range a.Extensions {
		r.byExt[strings.ToLower(ext)] = a
	}
}

// forPath returns the adapter owning a path's extension, if any.
func (r *Registry) forPath(path string) (Adapter, bool) {
	a, ok := r.byExt[strings.ToLower(filepath.Ext(path))]
	return a, ok
}
