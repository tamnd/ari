package tool

import (
	"context"
	"sort"
	"strings"

	"github.com/tamnd/ari/lsp"
)

// diagnose syncs a file with the language server through the seam and
// returns its error-severity diagnostics. A nil client, an unmatched
// path, or a slow server all yield nothing: the change already landed on
// disk, so diagnostics are a bonus, never a gate.
func diagnose(ctx context.Context, tc *ToolContext, path string, kind lsp.TouchKind) []lsp.Diagnostic {
	if tc == nil || tc.LSP == nil {
		return nil
	}
	_ = tc.LSP.Touch(ctx, path, kind)
	return tc.LSP.Diagnostics(path)
}

// warm kicks a full sync of a file the model just read, in the background,
// so the language server has parsed and type-checked it before the edit
// that usually follows lands. It never blocks the read and never surfaces
// an error: a warm that fails just means the first edit pays the parse
// cost instead (doc 04 section 6). The context is detached so a finished
// read does not cancel the warm mid-flight.
func warm(tc *ToolContext, path string) {
	if tc == nil || tc.LSP == nil {
		return
	}
	client := tc.LSP
	go func() { _ = client.Touch(context.Background(), path, lsp.TouchFull) }()
}

// projectDiagnostics returns the other files that newly carry errors after
// a write, when the client can look outward. It is empty for a client that
// only implements the two-method seam.
func projectDiagnostics(tc *ToolContext, path string) map[string][]lsp.Diagnostic {
	if tc == nil || tc.LSP == nil {
		return nil
	}
	pd, ok := tc.LSP.(projectDiagnoser)
	if !ok {
		return nil
	}
	return pd.ProjectDiagnostics(path)
}

// appendDiagnostics folds a file's diagnostics onto a model-facing result
// as the doc 04 block, returning the text unchanged when there are none.
func appendDiagnostics(model, path string, ds []lsp.Diagnostic) string {
	block := lsp.Format(path, ds)
	if block == "" {
		return model
	}
	return model + "\n\n" + block
}

// appendProjectDiagnostics folds the other-file diagnostics onto a write's
// result, one block per file in a stable path order so the model reads a
// deterministic to-fix list.
func appendProjectDiagnostics(model string, byFile map[string][]lsp.Diagnostic) string {
	if len(byFile) == 0 {
		return model
	}
	paths := make([]string, 0, len(byFile))
	for p := range byFile {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var b strings.Builder
	b.WriteString(model)
	for _, p := range paths {
		if block := lsp.Format(p, byFile[p]); block != "" {
			b.WriteString("\n\n")
			b.WriteString(block)
		}
	}
	return b.String()
}
