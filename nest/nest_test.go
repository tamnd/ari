package nest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveInRepo(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	sub := filepath.Join(repo, "a", "b")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARI_HOME", filepath.Join(tmp, "arihome"))

	n, err := Resolve(sub)
	if err != nil {
		t.Fatal(err)
	}
	if n.Root != repo {
		t.Errorf("root = %q, want repo root %q", n.Root, repo)
	}
	if n.Global != filepath.Join(tmp, "arihome") {
		t.Errorf("global = %q ignored ARI_HOME", n.Global)
	}
	// Two directories in the same repo share a key.
	n2, err := Resolve(repo)
	if err != nil {
		t.Fatal(err)
	}
	if n2.Key != n.Key {
		t.Errorf("keys differ within one repo: %q vs %q", n.Key, n2.Key)
	}
}

func TestResolveOutsideRepo(t *testing.T) {
	tmp := t.TempDir()
	dir := filepath.Join(tmp, "plain")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ARI_HOME", filepath.Join(tmp, "arihome"))
	n, err := Resolve(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Guard against a .git above TempDir on a developer machine.
	if n.Root != dir && !strings.HasPrefix(dir, n.Root) {
		t.Errorf("root = %q, want %q or an ancestor", n.Root, dir)
	}
}

func TestKey(t *testing.T) {
	k := Key("/Users/x/github/repo")
	if k != "-Users-x-github-repo" {
		t.Errorf("key = %q", k)
	}
	long := "/" + strings.Repeat("a/", 200)
	lk := Key(long)
	if len(lk) > 100 {
		t.Errorf("long key not truncated: %d bytes", len(lk))
	}
	if Key(long) != lk {
		t.Error("key not deterministic")
	}
	if Key(long+"x") == lk {
		t.Error("distinct long paths collided")
	}
}

func TestPathsAndEnsure(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("ARI_HOME", filepath.Join(tmp, "h"))
	n, err := Resolve(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.EnsureGlobal(); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(n.AuthDir())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("auth dir mode = %o, want 0700", fi.Mode().Perm())
	}
	if got := n.ColonyDB(); !strings.HasPrefix(got, n.ProjectStateDir()) {
		t.Errorf("colony.db outside project state dir: %q", got)
	}
	if got := n.LocalConfig(); got != filepath.Join(n.Root, ".ari", "local.toml") {
		t.Errorf("local config = %q", got)
	}
}
