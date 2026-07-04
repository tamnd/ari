package mcp

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverMergesLayersAndServerOverride(t *testing.T) {
	root := t.TempDir()
	global := t.TempDir()
	nested := filepath.Join(root, "pkg", "inner")

	writeFile(t, filepath.Join(global, "mcp.toml"), `
deny = ["evil__*"]
[servers.sqlite]
command = "global-sqlite"
`)
	writeFile(t, filepath.Join(root, ".ari", "mcp.toml"), `
deny = ["docs__delete"]
[servers.sqlite]
command = "project-sqlite"
[servers.docs]
command = "docs-server"
`)
	writeFile(t, filepath.Join(nested, ".ari", "mcp.toml"), `
[servers.local]
command = "local-server"
`)

	cfg, err := Discover(Options{Root: root, Cwd: nested, GlobalDir: global})
	if err != nil {
		t.Fatal(err)
	}
	// The project layer overrides the global sqlite command; nested adds local.
	if cfg.Servers["sqlite"].Command != "project-sqlite" {
		t.Fatalf("sqlite command = %q, want the project override", cfg.Servers["sqlite"].Command)
	}
	if cfg.Servers["docs"].Command != "docs-server" || cfg.Servers["local"].Command != "local-server" {
		t.Fatalf("servers = %+v, want docs and local present", cfg.Servers)
	}
	// The denylist is the union across layers.
	if !cfg.Denied("evil__anything") || !cfg.Denied("docs__delete") {
		t.Fatalf("deny union missing an entry: %v", cfg.Deny)
	}
	if cfg.Denied("docs__read") {
		t.Fatal("docs__read must not be denied")
	}
}

func TestDiscoverMalformedIsError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".ari", "mcp.toml"), "this is not = valid = toml =")
	if _, err := Discover(Options{Root: root, Cwd: root}); err == nil {
		t.Fatal("a malformed mcp.toml must surface as an error, not a silent skip")
	}
}

func TestDiscoverNoConfigIsEmpty(t *testing.T) {
	root := t.TempDir()
	cfg, err := Discover(Options{Root: root, Cwd: root})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 0 {
		t.Fatalf("servers = %+v, want none", cfg.Servers)
	}
}

func TestDeniedServerGlob(t *testing.T) {
	cfg := Config{Deny: []string{"sqlite__*", "docs__delete"}}
	cases := map[string]bool{
		"sqlite__query":  true,
		"sqlite__write":  true,
		"docs__delete":   true,
		"docs__read":     false,
		"other__sqlite_": false,
	}
	for name, want := range cases {
		if got := cfg.Denied(name); got != want {
			t.Errorf("Denied(%q) = %v, want %v", name, got, want)
		}
	}
}
