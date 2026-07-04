package skill

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"testing/fstest"
)

// dirEnt is a synthetic os.DirEntry for the in-memory dir reader.
type dirEnt struct {
	name string
	dir  bool
}

func (d dirEnt) Name() string               { return d.name }
func (d dirEnt) IsDir() bool                { return d.dir }
func (d dirEnt) Type() fs.FileMode          { return 0 }
func (d dirEnt) Info() (fs.FileInfo, error) { return nil, nil }

// mapDirReader lists the immediate children of a directory from a flat file
// map, marking a child a directory when the map holds anything beneath it.
func mapDirReader(files map[string]string) func(string) ([]os.DirEntry, error) {
	return func(dir string) ([]os.DirEntry, error) {
		dir = filepath.Clean(dir)
		seen := map[string]bool{}
		var out []os.DirEntry
		for path := range files {
			rel, err := filepath.Rel(dir, path)
			if err != nil || strings.HasPrefix(rel, "..") || rel == "." {
				continue
			}
			parts := strings.Split(rel, string(filepath.Separator))
			name := parts[0]
			if seen[name] {
				continue
			}
			seen[name] = true
			out = append(out, dirEnt{name: name, dir: len(parts) > 1})
		}
		if len(out) == 0 {
			return nil, os.ErrNotExist
		}
		sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
		return out, nil
	}
}

// TestDiscoverThreeLayers: skills come from builtin, user, and project, and a
// project skill shadows a user and builtin namesake by name.
func TestDiscoverThreeLayers(t *testing.T) {
	files := map[string]string{
		"/home/u/.ari/skills/onlyuser/SKILL.md":  "---\nname: onlyuser\ndescription: from the user nest\n---\nx",
		"/home/u/.ari/skills/shared/SKILL.md":    "---\nname: shared\ndescription: user version\n---\nx",
		"/repo/.ari/skills/onlyproject/SKILL.md": "---\nname: onlyproject\ndescription: from the repo\n---\nx",
		"/repo/.ari/skills/shared/SKILL.md":      "---\nname: shared\ndescription: project version\n---\nx",
		"/repo/.ari/commands/ship.md":            "---\ndescription: a slash command\n---\nrun $ARGUMENTS",
	}
	builtins := fstest.MapFS{
		"skills/onlybuiltin/SKILL.md": {Data: []byte("---\nname: onlybuiltin\ndescription: bundled\n---\nx")},
		"skills/shared/SKILL.md":      {Data: []byte("---\nname: shared\ndescription: builtin version\n---\nx")},
	}
	got, warns := Discover(Options{
		Root:      "/repo",
		Cwd:       "/repo",
		GlobalDir: "/home/u/.ari",
		Builtins:  builtins,
		readFile:  mapReader(files),
		readDir:   mapDirReader(files),
	})
	if len(warns) != 0 {
		t.Fatalf("unexpected warnings: %v", warns)
	}
	by := map[string]Skill{}
	for _, s := range got {
		by[s.Name] = s
	}
	for _, name := range []string{"onlyuser", "onlyproject", "onlybuiltin", "shared", "ship"} {
		if _, ok := by[name]; !ok {
			t.Errorf("missing skill %q; got %v", name, keys(by))
		}
	}
	if by["shared"].Scope != ScopeProject || by["shared"].Description != "project version" {
		t.Errorf("project skill did not shadow: %+v", by["shared"])
	}
	if by["onlyuser"].Scope != ScopeUser {
		t.Errorf("onlyuser scope = %q", by["onlyuser"].Scope)
	}
	if by["onlybuiltin"].Scope != ScopeBuiltin {
		t.Errorf("onlybuiltin scope = %q", by["onlybuiltin"].Scope)
	}
	if by["ship"].Kind != KindCommand {
		t.Errorf("ship kind = %q", by["ship"].Kind)
	}
}

// TestDiscoverNestedShadowsRoot: a skill in a nested package outranks the
// root's namesake, the closest-dir-wins rule.
func TestDiscoverNestedShadowsRoot(t *testing.T) {
	files := map[string]string{
		"/repo/.ari/skills/x/SKILL.md":     "---\nname: x\ndescription: root version\n---\nb",
		"/repo/pkg/.ari/skills/x/SKILL.md": "---\nname: x\ndescription: nested version\n---\nb",
	}
	got, _ := Discover(Options{
		Root:     "/repo",
		Cwd:      "/repo/pkg",
		readFile: mapReader(files),
		readDir:  mapDirReader(files),
	})
	if len(got) != 1 || got[0].Description != "nested version" {
		t.Fatalf("nested did not shadow root: %+v", got)
	}
}

// TestMalformedSkillIsWarning: a broken file becomes a warning and the good
// skills beside it still load.
func TestMalformedSkillIsWarning(t *testing.T) {
	files := map[string]string{
		"/repo/.ari/skills/good/SKILL.md": "---\nname: good\ndescription: fine\n---\nb",
		"/repo/.ari/skills/bad/SKILL.md":  "---\nname: Bad Name!!\n---\nb",
	}
	got, warns := Discover(Options{
		Root:     "/repo",
		Cwd:      "/repo",
		readFile: mapReader(files),
		readDir:  mapDirReader(files),
	})
	if len(got) != 1 || got[0].Name != "good" {
		t.Fatalf("good skill lost: %+v", got)
	}
	if len(warns) != 1 || !strings.Contains(warns[0].Path, "bad") {
		t.Fatalf("expected one warning for the bad skill, got %v", warns)
	}
}

// TestBodiesAbsentUntilInvoked: discovery reads frontmatter only; a body is
// read on demand through Body and matches the file.
func TestBodiesAbsentUntilInvoked(t *testing.T) {
	files := map[string]string{
		"/repo/.ari/skills/x/SKILL.md": "---\nname: x\ndescription: d\n---\nthe secret body",
	}
	reads := 0
	counting := func(path string) ([]byte, error) {
		reads++
		return mapReader(files)(path)
	}
	got, _ := Discover(Options{
		Root:     "/repo",
		Cwd:      "/repo",
		readFile: counting,
		readDir:  mapDirReader(files),
	})
	if len(got) != 1 {
		t.Fatalf("want 1 skill, got %d", len(got))
	}
	afterDiscover := reads
	body, err := got[0].Body()
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(body) != "the secret body" {
		t.Errorf("body = %q", body)
	}
	if reads != afterDiscover+1 {
		t.Errorf("Body did not read exactly once on demand: discover=%d total=%d", afterDiscover, reads)
	}
}

func keys(m map[string]Skill) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
