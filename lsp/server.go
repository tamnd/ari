package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// maxDiagnosticsPerFile caps how many findings a single file reports, so
// a file with a hundred errors cannot blow the tool-result budget when
// slice 6 folds them into the model-facing result (doc 04 section 3).
const maxDiagnosticsPerFile = 20

// server is one running language server: the JSON-RPC connection, the set
// of documents opened with it, and the diagnostics it has published so
// far, keyed by document URI.
type server struct {
	adapter Adapter
	conn    *rpcConn
	rootURI string

	mu      sync.Mutex
	version map[string]int             // uri -> last sent version
	diags   map[string][]Diagnostic    // uri -> latest published set
	waiters map[string][]chan struct{} // uri -> callers waiting on the next publish
}

func newServer(a Adapter, conn *rpcConn, root string) *server {
	s := &server{
		adapter: a,
		conn:    conn,
		rootURI: pathToURI(root),
		version: map[string]int{},
		diags:   map[string][]Diagnostic{},
		waiters: map[string][]chan struct{}{},
	}
	conn.setHandler(s.onNotify)
	return s
}

// initialize runs the LSP handshake: the initialize request, then the
// initialized notification. A handshake failure is the caller's cue to
// mark the adapter broken.
func (s *server) initialize(ctx context.Context) error {
	params := map[string]any{
		"processId": os.Getpid(),
		"rootUri":   s.rootURI,
		"capabilities": map[string]any{
			"textDocument": map[string]any{
				"publishDiagnostics": map[string]any{},
				"synchronization": map[string]any{
					"didSave": true,
				},
			},
		},
		"workspaceFolders": []map[string]any{
			{"uri": s.rootURI, "name": filepath.Base(s.rootURI)},
		},
	}
	if err := s.conn.call(ctx, "initialize", params, nil); err != nil {
		return err
	}
	return s.conn.notify("initialized", map[string]any{})
}

// touch syncs a file with the server and waits, bounded by ctx, for the
// server to publish diagnostics for it. A wait that the context ends is
// not an error: the caller reads whatever diagnostics are on hand, which
// may be none.
func (s *server) touch(ctx context.Context, path string, kind TouchKind) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // a file we cannot read is nothing to diagnose, not a failure
	}
	uri := pathToURI(path)
	text := string(data)

	wait := s.register(uri)

	s.mu.Lock()
	ver, opened := s.version[uri]
	if !opened {
		ver = 1
	} else {
		ver++
	}
	s.version[uri] = ver
	s.mu.Unlock()

	if !opened {
		err = s.conn.notify("textDocument/didOpen", map[string]any{
			"textDocument": map[string]any{
				"uri": uri, "languageId": s.adapter.LanguageID, "version": ver, "text": text,
			},
		})
	} else {
		err = s.conn.notify("textDocument/didChange", map[string]any{
			"textDocument":   map[string]any{"uri": uri, "version": ver},
			"contentChanges": []map[string]any{{"text": text}},
		})
	}
	if err != nil {
		return err
	}
	if kind == TouchFull {
		if err := s.conn.notify("textDocument/didSave", map[string]any{
			"textDocument": map[string]any{"uri": uri},
			"text":         text,
		}); err != nil {
			return err
		}
	}

	select {
	case <-ctx.Done():
	case <-wait:
	}
	return nil
}

// diagnostics returns the capped error-severity findings for a path.
func (s *server) diagnostics(path string) []Diagnostic {
	uri := pathToURI(path)
	s.mu.Lock()
	all := s.diags[uri]
	s.mu.Unlock()

	var out []Diagnostic
	for _, d := range all {
		if d.Severity == "error" {
			out = append(out, d)
			if len(out) >= maxDiagnosticsPerFile {
				break
			}
		}
	}
	return out
}

// allDiagnostics returns every URI's findings, for slice 6's project-wide
// write reporting and the sidebar counts.
func (s *server) allDiagnostics() map[string][]Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string][]Diagnostic, len(s.diags))
	for uri, ds := range s.diags {
		out[uri] = append([]Diagnostic(nil), ds...)
	}
	return out
}

func (s *server) close() error { return s.conn.close() }

// register returns a channel that closes when the next publishDiagnostics
// for uri arrives.
func (s *server) register(uri string) <-chan struct{} {
	ch := make(chan struct{})
	s.mu.Lock()
	s.waiters[uri] = append(s.waiters[uri], ch)
	s.mu.Unlock()
	return ch
}

// onNotify handles server-to-client notifications. The only one that
// matters in M1 is publishDiagnostics; the rest (progress, log messages)
// are ignored.
func (s *server) onNotify(method string, params json.RawMessage) {
	if method != "textDocument/publishDiagnostics" {
		return
	}
	var p struct {
		URI         string `json:"uri"`
		Diagnostics []struct {
			Range struct {
				Start struct {
					Line      int `json:"line"`
					Character int `json:"character"`
				} `json:"start"`
			} `json:"range"`
			Severity int    `json:"severity"`
			Message  string `json:"message"`
		} `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	ds := make([]Diagnostic, 0, len(p.Diagnostics))
	for _, d := range p.Diagnostics {
		ds = append(ds, Diagnostic{
			Line:     d.Range.Start.Line + 1,
			Col:      d.Range.Start.Character + 1,
			Severity: severityName(d.Severity),
			Message:  strings.TrimSpace(d.Message),
		})
	}
	sort.SliceStable(ds, func(i, j int) bool {
		if ds[i].Line != ds[j].Line {
			return ds[i].Line < ds[j].Line
		}
		return ds[i].Col < ds[j].Col
	})

	s.mu.Lock()
	s.diags[p.URI] = ds
	waiters := s.waiters[p.URI]
	delete(s.waiters, p.URI)
	s.mu.Unlock()

	for _, ch := range waiters {
		close(ch)
	}
}

// severityName maps the LSP severity number to the name the store keeps.
func severityName(n int) string {
	switch n {
	case 1:
		return "error"
	case 2:
		return "warning"
	case 3:
		return "info"
	case 4:
		return "hint"
	default:
		return "error" // an unset severity is treated as an error, the safe read
	}
}

// pathToURI turns a filesystem path into a file:// URI the way gopls
// expects, with a leading slash so a relative path still forms a valid
// absolute URI.
func pathToURI(p string) string {
	p = filepath.ToSlash(p)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}
