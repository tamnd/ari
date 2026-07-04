package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	findMaxResult    = 30_000 // bytes, then spill (doc 04 section 3.3)
	findDefaultLimit = 1000   // results, a ceiling that keeps a wide match readable
)

type findArgs struct {
	// Glob selects files by path pattern, like **/*.go. When Content is
	// empty, find returns the matching paths.
	Glob string `json:"glob,omitempty"`

	// Content is a regular expression matched against file contents.
	// When set, find returns matching lines with file and line number.
	Content string `json:"content,omitempty"`

	// Path scopes the search to a subtree. Absolute; defaults to the cwd.
	Path string `json:"path,omitempty"`

	// Limit caps the number of results. Defaults to a sane ceiling.
	Limit int `json:"limit,omitempty"`
}

// FindDisplay is the typed match structure the TUI renders as a
// collapsible tree. It never goes to the model (doc 04 section 11.2).
type FindDisplay struct {
	Files  []FindFile
	Total  int  // matches before the cap
	Capped bool // whether the limit truncated the set
}

// FindFile is one file's slice of the match set.
type FindFile struct {
	Path    string
	Mtime   time.Time
	Matches []FindMatch // empty for a glob-only search
}

// FindMatch is one matching line.
type FindMatch struct {
	Line int
	Text string
}

// findTool is glob and content grep in one tool. D7 folds them together
// because a model that wants to locate code usually wants both, and one
// tool with a mode is cheaper on the prompt budget than two (doc 04
// section 8).
type findTool struct {
	Base
}

// NewFind builds the find tool.
func NewFind() Tool { return findTool{} }

func (findTool) Name() string { return "find" }

func (findTool) Schema() Schema {
	return Schema{
		Name:        "find",
		Description: "Find files by glob, or search file contents by regexp; results are ranked and capped.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"glob": {"type": "string", "description": "Path pattern like **/*.go. Alone, lists matching files newest first."},
				"content": {"type": "string", "description": "Regexp matched against file contents; returns path:line: text."},
				"path": {"type": "string", "description": "Absolute directory to search under. Defaults to the working directory."},
				"limit": {"type": "integer", "description": "Maximum results. Default 1000."}
			}
		}`),
	}
}

func (findTool) IsReadOnly(json.RawMessage) bool        { return true }
func (findTool) IsConcurrencySafe(json.RawMessage) bool { return true }
func (findTool) MaxResultSize() int                     { return findMaxResult }

func (findTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a findArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if a.Glob == "" && a.Content == "" {
		return fmt.Errorf("set glob to find files by name or content to search inside them")
	}
	if a.Content != "" {
		if _, err := regexp.Compile(a.Content); err != nil {
			return fmt.Errorf("content is not a valid regular expression: %v", err)
		}
	}
	if a.Path != "" && !filepath.IsAbs(a.Path) {
		return fmt.Errorf("path must be an absolute path, got %q; resolve it against the working directory first", a.Path)
	}
	if a.Limit < 0 {
		return fmt.Errorf("limit must not be negative")
	}
	return nil
}

func (findTool) Call(ctx context.Context, raw json.RawMessage, tc *ToolContext, _ ProgressFunc) (*Result, error) {
	var a findArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	root := a.Path
	if root == "" {
		root = tc.Cwd
	}
	limit := a.Limit
	if limit == 0 {
		limit = findDefaultLimit
	}

	files, err := walkTree(ctx, root, a.Glob)
	if err != nil {
		return nil, err
	}

	rk := newRanker(tc)
	if a.Content == "" {
		return findPaths(files, limit, rk), nil
	}
	re := regexp.MustCompile(a.Content)
	return findContent(ctx, files, re, limit, rk)
}

// candidate is one file the walk kept.
type candidate struct {
	path  string
	mtime time.Time
}

// walkTree lists the files under root that pass the ignore rules and
// the glob. It skips .git, node_modules, vendor, and .gitignore'd paths
// by default, because a grep that returns a thousand hits from
// node_modules is expensive noise (doc 04 section 8.2).
func walkTree(ctx context.Context, root, glob string) ([]candidate, error) {
	ign := loadIgnore(root)
	var out []candidate
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable entry is skipped, not fatal
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil || rel == "." {
			return nil
		}
		if d.IsDir() {
			if ign.skips(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if ign.skips(rel, false) {
			return nil
		}
		if glob != "" && !globMatch(glob, filepath.ToSlash(rel)) {
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return nil
		}
		out = append(out, candidate{path: p, mtime: info.ModTime()})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// findPaths renders a glob-only search. The ranking pass orders the set so
// the hits a model most likely wants survive the cap: session-touched files
// first, then files nearer the cwd, then source over generated or vendored,
// with modification time newest first breaking any remaining tie (doc 04
// section 8.2).
func findPaths(files []candidate, limit int, rk ranker) *Result {
	keys := rankKeys(files, rk)
	sort.SliceStable(files, func(i, j int) bool {
		ki, kj := keys[files[i].path], keys[files[j].path]
		if ki != kj {
			return ki.less(kj)
		}
		if !files[i].mtime.Equal(files[j].mtime) {
			return files[i].mtime.After(files[j].mtime)
		}
		return files[i].path < files[j].path
	})
	total := len(files)
	capped := total > limit
	if capped {
		files = files[:limit]
	}

	var b strings.Builder
	display := FindDisplay{Total: total, Capped: capped}
	for _, f := range files {
		fmt.Fprintf(&b, "%s\n", f.path)
		display.Files = append(display.Files, FindFile{Path: f.path, Mtime: f.mtime})
	}
	if total == 0 {
		b.WriteString("(no files matched)\n")
	}
	if capped {
		fmt.Fprintf(&b, "\nshowing the top %d of %d matches, tighten the glob or add a path to see the rest\n", limit, total)
	}
	return &Result{Model: b.String(), Display: display}
}

// fileHits is one file's matches during a content search.
type fileHits struct {
	candidate
	matches []FindMatch
}

// findContent renders a content search: matches grouped by file. The
// ranking pass orders the files by the same key as findPaths, session-touched
// then nearer the cwd then source over generated, and only within a tie does
// the older signal apply, densest then freshest, so a truncated result still
// shows the part the model most likely needs (doc 04 section 8.2).
func findContent(ctx context.Context, files []candidate, re *regexp.Regexp, limit int, rk ranker) (*Result, error) {
	var hits []fileHits
	total := 0
	for _, f := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		content, err := os.ReadFile(f.path)
		if err != nil || isBinary(content) {
			continue
		}
		var ms []FindMatch
		for i, line := range splitLines(content) {
			if re.MatchString(line) {
				ms = append(ms, FindMatch{Line: i + 1, Text: line})
			}
		}
		if len(ms) > 0 {
			hits = append(hits, fileHits{candidate: f, matches: ms})
			total += len(ms)
		}
	}
	keys := make(map[string]rankKey, len(hits))
	for _, h := range hits {
		keys[h.path] = rk.key(h.path)
	}
	sort.SliceStable(hits, func(i, j int) bool {
		ki, kj := keys[hits[i].path], keys[hits[j].path]
		if ki != kj {
			return ki.less(kj)
		}
		if len(hits[i].matches) != len(hits[j].matches) {
			return len(hits[i].matches) > len(hits[j].matches)
		}
		if !hits[i].mtime.Equal(hits[j].mtime) {
			return hits[i].mtime.After(hits[j].mtime)
		}
		return hits[i].path < hits[j].path
	})

	var b strings.Builder
	display := FindDisplay{Total: total, Capped: total > limit}
	shown := 0
	for _, h := range hits {
		if shown >= limit {
			break
		}
		df := FindFile{Path: h.path, Mtime: h.mtime}
		for _, m := range h.matches {
			if shown >= limit {
				break
			}
			fmt.Fprintf(&b, "%s:%d: %s\n", h.path, m.Line, m.Text)
			df.Matches = append(df.Matches, m)
			shown++
		}
		display.Files = append(display.Files, df)
	}
	if total == 0 {
		b.WriteString("(no matches)\n")
	}
	if display.Capped {
		fmt.Fprintf(&b, "\nshowing the top %d of %d matches, tighten the glob or add a path to see the rest\n", shown, total)
	}
	return &Result{Model: b.String(), Display: display}, nil
}

// ranker scores a file path for ordering within find's result set. Lower
// sorts earlier. It reads the session file-state map for the touched signal
// and the cwd for the proximity signal, both already on the ToolContext, so
// the ranking reuses state that exists rather than tracking a second history
// (doc 04 section 9).
type ranker struct {
	files *FileState
	cwd   string
}

func newRanker(tc *ToolContext) ranker {
	if tc == nil {
		return ranker{}
	}
	return ranker{files: tc.Files, cwd: tc.Cwd}
}

// rankKey is the ordered ranking tuple for one file. The fields are compared
// in strength order, so a session-touched file always outranks an untouched
// one no matter the other signals, and so on down. Every field is an int, so
// the key is comparable and the ordering is deterministic and golden-testable.
type rankKey struct {
	untouched int // 0 if read or written this session, else 1
	distance  int // directory hops from the cwd, upward hops counted double
	generated int // 0 for source, 1 for generated or vendored
}

// less reports whether a sorts before b, comparing the signals in strength
// order (doc 04 section 8.2).
func (a rankKey) less(b rankKey) bool {
	if a.untouched != b.untouched {
		return a.untouched < b.untouched
	}
	if a.distance != b.distance {
		return a.distance < b.distance
	}
	return a.generated < b.generated
}

// key computes the ranking tuple for one absolute path.
func (r ranker) key(p string) rankKey {
	return rankKey{
		untouched: boolToInt(!r.touched(p)),
		distance:  r.distance(p),
		generated: boolToInt(isGenerated(p)),
	}
}

// touched reports whether the ant read or wrote this path this session.
func (r ranker) touched(p string) bool {
	if r.files == nil {
		return false
	}
	_, ok := r.files.Entry(p)
	return ok
}

// distance counts the directory hops from the cwd to the file. A file in the
// cwd is zero; each level deeper is one; each step above the cwd counts double
// so a match under the working tree beats one outside it.
func (r ranker) distance(p string) int {
	if r.cwd == "" {
		return 0
	}
	rel, err := filepath.Rel(r.cwd, p)
	if err != nil {
		return 1 << 20 // an uncomparable path sorts last
	}
	rel = filepath.ToSlash(rel)
	if rel == "." {
		return 0
	}
	segs := strings.Split(rel, "/")
	hops := len(segs) - 1 // the last segment is the filename
	for _, s := range segs {
		if s == ".." {
			hops++
		}
	}
	return hops
}

// generatedDirs are path segments that mark a subtree as generated or
// vendored output rather than hand-written source.
var generatedDirs = map[string]bool{
	"vendor":       true,
	"node_modules": true,
	"dist":         true,
	"build":        true,
	"generated":    true,
	"gen":          true,
	"out":          true,
	".next":        true,
}

// generatedSuffixes are filename endings that mark a file as generated.
var generatedSuffixes = []string{
	".pb.go", "_generated.go", "_gen.go", ".min.js", ".min.css", ".g.dart",
}

// isGenerated reports whether a path looks like generated or vendored output,
// by a marker directory in the path or a generated-file suffix. It is a
// documented heuristic, not a build-system query, so it stays deterministic.
func isGenerated(p string) bool {
	slash := filepath.ToSlash(p)
	for _, seg := range strings.Split(slash, "/") {
		if generatedDirs[seg] {
			return true
		}
	}
	base := path.Base(slash)
	if strings.Contains(base, ".generated.") {
		return true
	}
	for _, suf := range generatedSuffixes {
		if strings.HasSuffix(base, suf) {
			return true
		}
	}
	return false
}

// rankKeys precomputes the ranking tuple for each candidate, so the sort
// comparator never recomputes a key.
func rankKeys(files []candidate, rk ranker) map[string]rankKey {
	keys := make(map[string]rankKey, len(files))
	for _, f := range files {
		keys[f.path] = rk.key(f.path)
	}
	return keys
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// globMatch matches a slash-separated pattern against a relative path,
// with ** crossing directory boundaries. A pattern without a slash
// matches the basename anywhere in the tree, the way a model writes
// *.go and means everywhere.
func globMatch(pattern, rel string) bool {
	if !strings.Contains(pattern, "/") {
		ok, _ := path.Match(pattern, path.Base(rel))
		return ok
	}
	return matchSegments(strings.Split(pattern, "/"), strings.Split(rel, "/"))
}

func matchSegments(pat, parts []string) bool {
	if len(pat) == 0 {
		return len(parts) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(parts); skip++ {
			if matchSegments(pat[1:], parts[skip:]) {
				return true
			}
		}
		return false
	}
	if len(parts) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], parts[0]); !ok {
		return false
	}
	return matchSegments(pat[1:], parts[1:])
}

// ignoreRules is the quiet-by-default filter: conventional noise
// directories plus the root .gitignore's simple patterns.
type ignoreRules struct {
	patterns []ignorePattern
}

type ignorePattern struct {
	pattern string
	dirOnly bool
	rooted  bool
}

// defaultIgnoreDirs are skipped whether or not a .gitignore names them.
var defaultIgnoreDirs = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".ari":         true,
}

func loadIgnore(root string) ignoreRules {
	var r ignoreRules
	content, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		return r
	}
	for line := range strings.SplitSeq(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "!") {
			continue // negations are out of scope for the simple reader
		}
		p := ignorePattern{pattern: line}
		if strings.HasSuffix(p.pattern, "/") {
			p.dirOnly = true
			p.pattern = strings.TrimSuffix(p.pattern, "/")
		}
		if strings.HasPrefix(p.pattern, "/") {
			p.rooted = true
			p.pattern = strings.TrimPrefix(p.pattern, "/")
		}
		r.patterns = append(r.patterns, p)
	}
	return r
}

func (r ignoreRules) skips(rel string, isDir bool) bool {
	rel = filepath.ToSlash(rel)
	base := path.Base(rel)
	if isDir && defaultIgnoreDirs[base] {
		return true
	}
	for _, p := range r.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if p.rooted {
			if ok, _ := path.Match(p.pattern, rel); ok {
				return true
			}
			continue
		}
		if ok, _ := path.Match(p.pattern, base); ok {
			return true
		}
		if ok, _ := path.Match(p.pattern, rel); ok {
			return true
		}
	}
	return false
}
