package lsp

import (
	"fmt"
	"strings"
)

// maxProjectDiagnostics caps how many other-file findings a write folds
// into its model-facing result, so a write that breaks three callers
// reports three errors, not the whole repo's backlog (doc 04 section 3).
const maxProjectDiagnostics = 10

// Format renders one file's diagnostics as the model-facing block doc 04
// settled on: a <diagnostics> element naming the file, one ERROR line per
// finding with its 1-based line and column. Empty when there are none, so
// a clean file adds nothing to the result.
func Format(path string, ds []Diagnostic) string {
	if len(ds) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<diagnostics file=%q>\n", path)
	for _, d := range ds {
		fmt.Fprintf(&b, "%s [%d:%d] %s\n", strings.ToUpper(d.Severity), d.Line, d.Col, d.Message)
	}
	b.WriteString("</diagnostics>")
	return b.String()
}

// ProjectDiagnostics returns error-severity findings for every file the
// ready servers have reported, excluding the given path, capped across
// files. It is what write uses to look outward: a whole-file overwrite is
// the likeliest cause of a break in a caller elsewhere (doc 04 section 6).
func (s *Service) ProjectDiagnostics(exclude string) map[string][]Diagnostic {
	if !s.enabled {
		return nil
	}
	excludeURI := pathToURI(exclude)
	s.mu.Lock()
	servers := make([]*server, 0, len(s.servers))
	for _, srv := range s.servers {
		servers = append(servers, srv)
	}
	s.mu.Unlock()

	out := map[string][]Diagnostic{}
	budget := maxProjectDiagnostics
	for _, srv := range servers {
		for uri, ds := range srv.allDiagnostics() {
			if uri == excludeURI || budget <= 0 {
				continue
			}
			var errs []Diagnostic
			for _, d := range ds {
				if d.Severity == "error" {
					errs = append(errs, d)
					if budget--; budget <= 0 {
						break
					}
				}
			}
			if len(errs) > 0 {
				out[uriToPath(uri)] = errs
			}
		}
	}
	return out
}

// uriToPath reverses pathToURI for the model-facing file label.
func uriToPath(uri string) string {
	return strings.TrimPrefix(uri, "file://")
}
