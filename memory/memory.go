// Package memory discovers and assembles ari's project memory: the
// ARI.md a repo carries, the AGENTS.md and CLAUDE.md it honors for
// compatibility, their @-imports, and the per-file size cap (doc 01
// section 7.2, D21). The assembled text is injected as a system-reminder
// user message, not appended to the system prompt, so block one stays
// cache-stable (D14); this package only produces the text, the ant wraps
// it.
package memory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DefaultPerFileCap is the soft byte ceiling applied to each source after
// its imports resolve, so one runaway memory file cannot eat the window.
// An oversized file is truncated with a visible marker, never dropped
// silently, and ari doctor warns about it separately (doc 14).
const DefaultPerFileCap = 20_000

// maxImportDepth bounds @-import recursion so a deep or adversarial chain
// terminates even before the cycle guard would catch it.
const maxImportDepth = 10

// memoryNames are the recognized file names at one directory level,
// ordered lowest priority to highest: ARI.md is the native name and wins
// when several exist together, so it is loaded last within a level.
var memoryNames = []string{"CLAUDE.md", "AGENTS.md", "ARI.md"}

// localOverride is the gitignored per-user memory file at the project
// root; it is the highest-priority project source.
const localOverride = "ARI.local.md"

// Options configures discovery and assembly.
type Options struct {
	// Cwd is the working directory the parent walk starts from.
	Cwd string
	// Root is the project root (the git root); the parent walk stops here.
	Root string
	// GlobalDir is the global nest, home of the user-global memory file.
	GlobalDir string
	// PerFileCap overrides DefaultPerFileCap when nonzero.
	PerFileCap int
	// readFile is the file reader, injected by tests. Nil means os.ReadFile.
	readFile func(string) ([]byte, error)
}

func (o Options) read(path string) ([]byte, error) {
	if o.readFile != nil {
		return o.readFile(path)
	}
	return os.ReadFile(path)
}

// Load discovers the memory files with the documented precedence,
// resolves their @-imports, applies the per-file cap, and returns the
// merged text ready to inject as block two's project-memory section. The
// lowest-priority source lands first and the nearest, highest-priority
// source lands last. Empty when nothing is found.
func Load(opts Options) string {
	limit := opts.PerFileCap
	if limit <= 0 {
		limit = DefaultPerFileCap
	}
	var sections []string
	for _, path := range discover(opts) {
		data, err := opts.read(path)
		if err != nil {
			continue // a candidate that does not exist just drops out
		}
		seen := map[string]bool{path: true}
		rendered := strings.TrimSpace(resolveImports(opts, path, string(data), seen, 0))
		if rendered == "" {
			continue
		}
		rendered = applyCap(rendered, limit)
		sections = append(sections, "### "+label(opts, path)+"\n"+rendered)
	}
	return strings.Join(sections, "\n\n")
}

// discover lists the candidate memory paths in merge order, lowest
// priority first: the user-global file, then the project files walking
// from the root down to the cwd so the nearest lands last, then the
// gitignored local override at the root.
func discover(o Options) []string {
	var out []string
	if o.GlobalDir != "" {
		for _, n := range memoryNames {
			out = append(out, filepath.Join(o.GlobalDir, n))
		}
	}
	for _, dir := range dirsRootToCwd(o.Root, o.Cwd) {
		for _, n := range memoryNames {
			out = append(out, filepath.Join(dir, n))
		}
	}
	if o.Root != "" {
		out = append(out, filepath.Join(o.Root, localOverride))
	}
	return out
}

// dirsRootToCwd returns the directories from the project root down to the
// cwd, root first, so a nested package's notes land after and outrank the
// root's. The walk stops at the root and the root is always included; a
// cwd that is not inside the root contributes nothing, so the walk never
// climbs above the project into stray parent files.
func dirsRootToCwd(root, cwd string) []string {
	if root == "" {
		if cwd == "" {
			return nil
		}
		return []string{filepath.Clean(cwd)}
	}
	root = filepath.Clean(root)
	if cwd == "" {
		return []string{root}
	}
	cwd = filepath.Clean(cwd)
	if root == cwd || !within(root, cwd) {
		return []string{root}
	}
	var up []string
	for d := cwd; ; {
		up = append(up, d)
		if d == root {
			break
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	for i, j := 0, len(up)-1; i < j; i, j = i+1, j-1 {
		up[i], up[j] = up[j], up[i]
	}
	return up
}

// within reports whether p is root or a descendant of it.
func within(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || !strings.HasPrefix(rel, "..")
}

// resolveImports inlines the @-imports of one file before the file's own
// body, so an imported file reads as context for the file that pulled it.
// Imports are their own line, resolve only in leaf text (never inside a
// code fence), and are guarded against cycles and runaway depth. The
// caller seeds seen with the file's own path so a file cannot import
// itself.
func resolveImports(o Options, path, text string, seen map[string]bool, depth int) string {
	var imports, body []string
	fence := false
	for line := range strings.SplitSeq(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			fence = !fence
			body = append(body, line)
			continue
		}
		if fence || !isImport(trimmed) {
			body = append(body, line)
			continue
		}
		spec := trimmed[1:]
		target := resolveImportPath(path, spec)
		if target == "" {
			body = append(body, line)
			continue
		}
		if depth >= maxImportDepth {
			body = append(body, fmt.Sprintf("[ari: import %s skipped, import depth limit reached]", spec))
			continue
		}
		if seen[target] {
			body = append(body, fmt.Sprintf("[ari: import %s skipped, already imported (circular)]", spec))
			continue
		}
		data, err := o.read(target)
		if err != nil {
			body = append(body, fmt.Sprintf("[ari: import %s not found]", spec))
			continue
		}
		seen[target] = true
		imports = append(imports, resolveImports(o, target, string(data), seen, depth+1))
	}
	head := strings.Join(imports, "\n\n")
	tail := strings.TrimRight(strings.Join(body, "\n"), "\n")
	switch {
	case head == "":
		return tail
	case tail == "":
		return head
	default:
		return head + "\n\n" + tail
	}
}

// isImport reports whether a trimmed line is an @-import: a bare @token
// with no embedded whitespace, so a mid-sentence @mention or an email is
// never mistaken for one.
func isImport(trimmed string) bool {
	return len(trimmed) > 1 && trimmed[0] == '@' && !strings.ContainsAny(trimmed, " \t")
}

// resolveImportPath turns an import spec into an absolute path: @~/x is
// home-relative, @/x is absolute, and @x or @./x is relative to the
// importing file's directory.
func resolveImportPath(importer, spec string) string {
	switch {
	case strings.HasPrefix(spec, "~/"):
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		return filepath.Join(home, spec[2:])
	case filepath.IsAbs(spec):
		return spec
	default:
		return filepath.Join(filepath.Dir(importer), spec)
	}
}

// applyCap soft-truncates text past the cap and appends a marker naming
// how much was dropped, so an oversized file loses its tail visibly
// rather than silently.
func applyCap(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	over := len(text) - limit
	cut := limit
	// Back off to a rune boundary so the marker never splits a character.
	for cut > 0 && !utf8Start(text[cut]) {
		cut--
	}
	return text[:cut] + fmt.Sprintf("\n\n[ari: memory truncated, %d bytes over the %d-byte cap were dropped; see ari doctor]", over, limit)
}

// utf8Start reports whether b can start a UTF-8 sequence (not a
// continuation byte).
func utf8Start(b byte) bool { return b&0xC0 != 0x80 }

// label names a source in the merged output: relative to the root when it
// lives under it, tagged (global) when it comes from the global nest, and
// otherwise its bare path.
func label(o Options, path string) string {
	if o.GlobalDir != "" && within(o.GlobalDir, path) {
		return filepath.Base(path) + " (global)"
	}
	if o.Root != "" && within(o.Root, path) {
		if rel, err := filepath.Rel(o.Root, path); err == nil {
			return rel
		}
	}
	return path
}
