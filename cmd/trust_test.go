package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/nest"
)

// TestTrustRoundTrip drives ari trust end to end: trust the workspace, confirm
// the store records it, then revoke it.
func TestTrustRoundTrip(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARI_HOME", home)
	repo := t.TempDir()
	mustMkdir(t, filepath.Join(repo, ".git"))
	chdir(t, repo)

	n, err := nest.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runTrust(&out, false, false); err != nil {
		t.Fatalf("trust: %v", err)
	}
	if !strings.Contains(out.String(), "trusted") {
		t.Errorf("trust output: %s", out.String())
	}
	if !hook.LoadTrust(n.TrustFile()).IsTrusted(n.Root) {
		t.Fatal("workspace not recorded as trusted")
	}

	out.Reset()
	if err := runTrust(&out, true, false); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if hook.LoadTrust(n.TrustFile()).IsTrusted(n.Root) {
		t.Fatal("workspace still trusted after revoke")
	}
}

// TestTrustShowListsRepoHooks proves --show reports the decision and the repo
// hooks it gates without changing trust.
func TestTrustShowListsRepoHooks(t *testing.T) {
	home := t.TempDir()
	t.Setenv("ARI_HOME", home)
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	repo := t.TempDir()
	mustMkdir(t, filepath.Join(repo, ".git"))
	mustMkdir(t, filepath.Join(repo, ".ari"))
	chdir(t, repo)

	body := "[[hooks.PostToolUse]]\nmatcher = \"write\"\ncommand = \"gofmt -w .\"\n"
	if err := os.WriteFile(filepath.Join(repo, ".ari", "config.toml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := runTrust(&out, false, true); err != nil {
		t.Fatalf("show: %v", err)
	}
	s := out.String()
	if !strings.Contains(s, "untrusted") {
		t.Errorf("show should report untrusted: %s", s)
	}
	if !strings.Contains(s, "gofmt") {
		t.Errorf("show should list the repo hook: %s", s)
	}

	n, _ := nest.Resolve(".")
	if hook.LoadTrust(n.TrustFile()).IsTrusted(n.Root) {
		t.Fatal("--show must not change trust")
	}
}
