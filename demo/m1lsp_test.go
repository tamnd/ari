package demo

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/ari/lsp"
)

// fixedTime pins the timestamps the demo writes (trust records) so the run
// is deterministic.
var fixedTime = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

// contentLSP is a content-keyed stand-in for gopls: on a touch it reads the
// file from disk and reports an "undefined: time" error when the file calls
// time.Now() without importing the time package, exactly the mistake the
// demo's first edit makes. It clears once the import lands. This models the
// one diagnostic the walkthrough needs without a live server whose timing
// would make the release gate flaky; the real adapter is proven by the LSP
// fixture suite.
type contentLSP struct {
	mu    sync.Mutex
	diags map[string][]lsp.Diagnostic
	seen  map[string]bool // paths that ever reported an error
}

func newContentLSP() *contentLSP {
	return &contentLSP{diags: map[string][]lsp.Diagnostic{}, seen: map[string]bool{}}
}

// Touch re-reads the file and recomputes its diagnostics, the way a real
// server reacts to a didChange.
func (c *contentLSP) Touch(_ context.Context, path string, _ lsp.TouchKind) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil // an unreadable file is zero diagnostics, never a failed edit
	}
	src := string(data)
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(src, "time.Now(") && !strings.Contains(src, `"time"`) {
		c.diags[path] = []lsp.Diagnostic{{
			Line: lineOf(src, "time.Now("), Col: 1, Severity: "error",
			Message: "undefined: time",
		}}
		c.seen[path] = true
		return nil
	}
	delete(c.diags, path)
	return nil
}

// Diagnostics returns the current error diagnostics for a path.
func (c *contentLSP) Diagnostics(path string) []lsp.Diagnostic {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.diags[path]
}

// sawError reports whether the path ever carried the undefined-symbol error.
func (c *contentLSP) sawError(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen[path]
}

// lineOf returns the 1-based line the substring first appears on, or 1.
func lineOf(src, sub string) int {
	idx := strings.Index(src, sub)
	if idx < 0 {
		return 1
	}
	return strings.Count(src[:idx], "\n") + 1
}
