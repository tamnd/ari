package memory

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// write drops a file with its parent dirs, for building a memory tree.
func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestPrecedenceGlobalRootNested checks the merge order: the global file
// first, the root next, the nested-package file last, so the nearest file
// wins on salience. ARI.md beats the compat names at the same level.
func TestPrecedenceGlobalRootNested(t *testing.T) {
	root := t.TempDir()
	global := t.TempDir()
	sub := filepath.Join(root, "pkg", "inner")

	write(t, filepath.Join(global, "ARI.md"), "global rule")
	write(t, filepath.Join(root, "CLAUDE.md"), "root claude rule")
	write(t, filepath.Join(root, "ARI.md"), "root ari rule")
	write(t, filepath.Join(sub, "ARI.md"), "nested rule")

	got := Load(Options{Cwd: sub, Root: root, GlobalDir: global})

	// Order: global, then root (CLAUDE before ARI), then nested.
	for _, pair := range [][2]string{
		{"global rule", "root claude rule"},
		{"root claude rule", "root ari rule"},
		{"root ari rule", "nested rule"},
	} {
		if strings.Index(got, pair[0]) >= strings.Index(got, pair[1]) {
			t.Fatalf("%q should precede %q in:\n%s", pair[0], pair[1], got)
		}
	}
	// The native name and the compat name are both present, labeled.
	if !strings.Contains(got, "### CLAUDE.md") || !strings.Contains(got, "### ARI.md") {
		t.Fatalf("sources must be labeled by their path:\n%s", got)
	}
	if !strings.Contains(got, "### ARI.md (global)") {
		t.Fatalf("the global source must be tagged:\n%s", got)
	}
	if !strings.Contains(got, filepath.Join("pkg", "inner", "ARI.md")) {
		t.Fatalf("the nested source must be labeled relative to root:\n%s", got)
	}
}

// TestImportsInlineBeforeBody: an @-import is pulled in before the body of
// the file that imported it, resolving relative to that file's directory.
func TestImportsInlineBeforeBody(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "shared", "style.md"), "shared style guidance")
	write(t, filepath.Join(root, "ARI.md"), "before\n@shared/style.md\nafter")

	got := Load(Options{Cwd: root, Root: root})

	iImport := strings.Index(got, "shared style guidance")
	iBefore := strings.Index(got, "before")
	iAfter := strings.Index(got, "after")
	if iImport < 0 || iBefore < 0 || iAfter < 0 {
		t.Fatalf("missing content:\n%s", got)
	}
	if iImport > iBefore || iImport > iAfter {
		t.Fatalf("import must land before the importing file's body:\n%s", got)
	}
	if strings.Contains(got, "@shared/style.md") {
		t.Fatalf("the import line itself must be consumed:\n%s", got)
	}
}

// TestImportInsideFenceIgnored: an @-line inside a code fence is literal
// text, not an import.
func TestImportInsideFenceIgnored(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "secret.md"), "SHOULD NOT APPEAR")
	write(t, filepath.Join(root, "ARI.md"), "text\n```\n@secret.md\n```\nmore")

	got := Load(Options{Cwd: root, Root: root})
	if strings.Contains(got, "SHOULD NOT APPEAR") {
		t.Fatalf("an @-line in a code fence must not import:\n%s", got)
	}
	if !strings.Contains(got, "@secret.md") {
		t.Fatalf("the fenced @-line must survive as literal text:\n%s", got)
	}
}

// TestCircularImportBroken: A imports B imports A terminates with a marker
// instead of looping.
func TestCircularImportBroken(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a.md"), "A body\n@b.md")
	write(t, filepath.Join(root, "b.md"), "B body\n@a.md")
	write(t, filepath.Join(root, "ARI.md"), "@a.md")

	got := Load(Options{Cwd: root, Root: root})
	if !strings.Contains(got, "A body") || !strings.Contains(got, "B body") {
		t.Fatalf("both files should resolve once:\n%s", got)
	}
	if !strings.Contains(got, "already imported (circular)") {
		t.Fatalf("the cycle must be marked, not followed:\n%s", got)
	}
}

// TestMissingImportMarked: an import that does not resolve leaves a marker
// rather than failing the whole load.
func TestMissingImportMarked(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "ARI.md"), "keep this\n@nope.md")
	got := Load(Options{Cwd: root, Root: root})
	if !strings.Contains(got, "keep this") {
		t.Fatalf("the rest of the file must survive:\n%s", got)
	}
	if !strings.Contains(got, "[ari: import nope.md not found]") {
		t.Fatalf("a missing import must be marked:\n%s", got)
	}
}

// TestPerFileCapCountsImports: the cap applies after imports resolve, so a
// small ARI.md cannot smuggle a large context in through an import; the
// truncation is soft, with a visible marker.
func TestPerFileCapCountsImports(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", 5000)
	write(t, filepath.Join(root, "big.md"), big)
	write(t, filepath.Join(root, "ARI.md"), "tiny\n@big.md")

	got := Load(Options{Cwd: root, Root: root, PerFileCap: 1000})
	if !strings.Contains(got, "memory truncated") {
		t.Fatalf("an oversized import must trip the cap:\n%.200s", got)
	}
	if strings.Count(got, "x") >= 5000 {
		t.Fatal("the big import should have been truncated by the cap")
	}
}

// TestEmptyWhenNothingFound: no memory files means an empty string, so
// block two renders its placeholder.
func TestEmptyWhenNothingFound(t *testing.T) {
	root := t.TempDir()
	if got := Load(Options{Cwd: root, Root: root, GlobalDir: t.TempDir()}); got != "" {
		t.Fatalf("want empty, got %q", got)
	}
}

// TestCwdOutsideRootStaysContained: a cwd that is not under the root must
// not make the walk climb into stray parent files; discovery falls back
// to the root alone, so the root memory is always found.
func TestCwdOutsideRootStaysContained(t *testing.T) {
	root := t.TempDir()
	other := t.TempDir()
	write(t, filepath.Join(root, "ARI.md"), "root only")
	write(t, filepath.Join(filepath.Dir(other), "ARI.md"), "parent of other")

	dirs := dirsRootToCwd(root, other)
	if len(dirs) != 1 || dirs[0] != filepath.Clean(root) {
		t.Fatalf("cwd outside root should fall back to the root, got %v", dirs)
	}

	// And Load still finds the root file, never the parent-of-other file.
	got := Load(Options{Cwd: other, Root: root})
	if !strings.Contains(got, "root only") || strings.Contains(got, "parent of other") {
		t.Fatalf("discovery must stay contained to the root:\n%s", got)
	}
}
