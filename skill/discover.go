package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Options configures discovery across the three layers.
type Options struct {
	// Root is the project root; project skills live under Root/.ari/skills
	// and commands under Root/.ari/commands, walked from Root down to Cwd
	// the way project memory is (D21).
	Root string
	// Cwd is where the project walk ends, so a nested package's skills
	// outrank the root's.
	Cwd string
	// GlobalDir is the user nest; user skills and commands live under it.
	GlobalDir string
	// Builtins is the bundled skill tree, may be nil. A test injects a
	// fstest.MapFS; the binary injects its embedded FS.
	Builtins fs.FS
	// readFile is injected by tests. Nil means os.ReadFile.
	readFile func(string) ([]byte, error)
	// readDir is injected by tests. Nil means os.ReadDir.
	readDir func(string) ([]os.DirEntry, error)
}

// Discover scans the builtin, user, and project layers and returns every
// loadable skill and command, higher-priority scopes shadowing lower ones
// by name. Frontmatter is read; bodies are not. A malformed entry becomes a
// warning, never a failure: one broken skill must not take the session down
// (doc 13 section 2.3).
func Discover(o Options) ([]Skill, []Warning) {
	// Lowest priority first so a later insert into the map shadows an
	// earlier one: builtins, then user, then project root-to-cwd.
	var found []Skill
	var warns []Warning

	addBuiltins(o, &found, &warns)
	if o.GlobalDir != "" {
		scanScope(o, filepath.Join(o.GlobalDir, "skills"), filepath.Join(o.GlobalDir, "commands"), ScopeUser, &found, &warns)
	}
	for _, dir := range dirsRootToCwd(o.Root, o.Cwd) {
		base := filepath.Join(dir, ".ari")
		scanScope(o, filepath.Join(base, "skills"), filepath.Join(base, "commands"), ScopeProject, &found, &warns)
	}

	// Shadow by name, last (highest priority) wins, then sort for a stable
	// listing.
	byName := map[string]Skill{}
	for _, s := range found {
		byName[s.Name] = s
	}
	out := make([]Skill, 0, len(byName))
	for _, s := range byName {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, warns
}

// scanScope reads the skills and commands directories of one scope.
func scanScope(o Options, skillsDir, commandsDir string, scope Scope, found *[]Skill, warns *[]Warning) {
	for _, ent := range o.dirEntries(skillsDir) {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue // .candidates and dotfiles are excluded from the scan
		}
		path := filepath.Join(skillsDir, ent.Name(), "SKILL.md")
		if s, w, ok := o.load(path, ent.Name(), KindSkill, scope); ok {
			*found = append(*found, s)
		} else if w != nil {
			*warns = append(*warns, *w)
		}
	}
	for _, ent := range o.dirEntries(commandsDir) {
		if ent.IsDir() || !strings.HasSuffix(ent.Name(), ".md") || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		path := filepath.Join(commandsDir, ent.Name())
		name := strings.TrimSuffix(ent.Name(), ".md")
		if s, w, ok := o.load(path, name, KindCommand, scope); ok {
			*found = append(*found, s)
		} else if w != nil {
			*warns = append(*warns, *w)
		}
	}
}

// addBuiltins scans the embedded builtin tree, if any, as its own scope.
func addBuiltins(o Options, found *[]Skill, warns *[]Warning) {
	if o.Builtins == nil {
		return
	}
	entries, err := fs.ReadDir(o.Builtins, "skills")
	if err != nil {
		return // no builtin skills dir is fine, not an error
	}
	for _, ent := range entries {
		if !ent.IsDir() || strings.HasPrefix(ent.Name(), ".") {
			continue
		}
		path := "skills/" + ent.Name() + "/SKILL.md"
		data, err := fs.ReadFile(o.Builtins, path)
		if err != nil {
			continue
		}
		if s, w, ok := loadBytes(string(data), path, ent.Name(), KindSkill, ScopeBuiltin, nil); ok {
			*found = append(*found, s)
		} else if w != nil {
			*warns = append(*warns, *w)
		}
	}
}

// load reads and parses one on-disk skill or command file.
func (o Options) load(path, fallbackName string, kind Kind, scope Scope) (Skill, *Warning, bool) {
	data, err := o.read(path)
	if err != nil {
		return Skill{}, nil, false // an absent file is not a warning
	}
	return loadBytes(string(data), path, fallbackName, kind, scope, o.readFile)
}

// loadBytes parses the frontmatter and builds a Skill, returning a warning
// for a malformed file so discovery continues past it.
func loadBytes(data, path, fallbackName string, kind Kind, scope Scope, read func(string) ([]byte, error)) (Skill, *Warning, bool) {
	front, body, err := splitFrontmatter(data)
	if err != nil {
		return Skill{}, &Warning{Path: path, Reason: err.Error()}, false
	}
	fm, err := parseFrontmatter(front)
	if err != nil {
		return Skill{}, &Warning{Path: path, Reason: err.Error()}, false
	}
	s, err := fromFrontmatter(fm, body, fallbackName)
	if err != nil {
		return Skill{}, &Warning{Path: path, Reason: err.Error()}, false
	}
	s.Kind = kind
	s.Scope = scope
	s.Path = path
	s.read = read
	return s, nil, true
}

func (o Options) read(path string) ([]byte, error) {
	if o.readFile != nil {
		return o.readFile(path)
	}
	return os.ReadFile(path)
}

func (o Options) dirEntries(dir string) []os.DirEntry {
	if o.readDir != nil {
		ents, err := o.readDir(dir)
		if err != nil {
			return nil
		}
		return ents
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	return ents
}

// dirsRootToCwd returns the directories from the project root down to the
// cwd, root first so the nearest lands last and shadows. It mirrors the
// project-memory walk exactly, since skills follow the same precedence.
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
