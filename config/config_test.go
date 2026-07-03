package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/nest"
)

// testNest builds a nest rooted in temp dirs with the given file bodies.
func testNest(t *testing.T, user, project, local string) nest.Nest {
	t.Helper()
	tmp := t.TempDir()
	n := nest.Nest{Global: filepath.Join(tmp, "home"), Root: filepath.Join(tmp, "repo"), Key: "k"}
	if err := os.MkdirAll(n.Global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(n.ProjectDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path, body string) {
		if body == "" {
			return
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(n.GlobalConfig(), user)
	write(n.ProjectConfig(), project)
	write(n.LocalConfig(), local)
	return n
}

func TestDefaultsWithOnlyEnvKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	n := testNest(t, "", "", "")
	c, err := Load(n, FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["anthropic"].APIKey != "sk-test" {
		t.Error("env interpolation did not run")
	}
	if c.Mode != "ask" {
		t.Errorf("default mode = %q, want ask", c.Mode)
	}
	if c.Automation.Enabled {
		t.Error("automation must default off (D19)")
	}
	if c.UI.Theme != "dark" {
		t.Errorf("default theme = %q, want dark", c.UI.Theme)
	}
}

func TestPrecedence(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	user := "[ui]\ntheme = \"light\"\n"
	project := "[ui]\ntheme = \"dark\"\n[journal]\nretain_days = 7\n"
	local := "[journal]\nretain_days = 3\n"
	n := testNest(t, user, project, local)

	c, err := Load(n, FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Theme != "dark" {
		t.Errorf("project must override user: theme = %q", c.UI.Theme)
	}
	if c.Journal.RetainDays != 3 {
		t.Errorf("local must override project: retain = %d", c.Journal.RetainDays)
	}
	if c.Origin("ui.theme") != "project" || c.Origin("journal.retain_days") != "local" {
		t.Errorf("provenance wrong: %q %q", c.Origin("ui.theme"), c.Origin("journal.retain_days"))
	}

	c, err = Load(n, FlagOverrides{Theme: "light", Mode: "plan"})
	if err != nil {
		t.Fatal(err)
	}
	if c.UI.Theme != "light" || c.Origin("ui.theme") != "flag" {
		t.Error("flags must override every file")
	}
	if c.Mode != "plan" {
		t.Errorf("mode = %q", c.Mode)
	}
}

func TestUnknownKeyWarnsNotCrashes(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	n := testNest(t, "[future_feature]\nknob = true\n", "", "")
	c, err := Load(n, FlagOverrides{})
	if err != nil {
		t.Fatalf("unknown key crashed the load: %v", err)
	}
	if len(c.Warnings()) == 0 || !strings.Contains(c.Warnings()[0], "future_feature") {
		t.Errorf("expected a warning naming the key, got %v", c.Warnings())
	}
}

func TestMissingEnvForReferencedProvider(t *testing.T) {
	if old, had := os.LookupEnv("ANTHROPIC_API_KEY"); had {
		if err := os.Unsetenv("ANTHROPIC_API_KEY"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Setenv("ANTHROPIC_API_KEY", old); err != nil {
				t.Error(err)
			}
		})
	}
	n := testNest(t, "", "", "")
	_, err := Load(n, FlagOverrides{})
	if err == nil {
		t.Fatal("expected a load error for the missing key")
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error must name the variable: %v", err)
	}
	if strings.Contains(err.Error(), "sk-") {
		t.Error("error leaked a value")
	}
}

func TestValidationListsEverything(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	user := `
[tier.frontier]
chain = [
  { provider = "nope", model = "m" },
  { provider = "anthropic", model = "" },
]
[tier.mid]
chain = []
`
	n := testNest(t, user, "", "")
	_, err := Load(n, FlagOverrides{})
	if err == nil {
		t.Fatal("expected validation errors")
	}
	for _, want := range []string{"undefined provider", "empty model", "empty chain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validation missed %q in: %v", want, err)
		}
	}
}

func TestCustomProviderAndTier(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	user := `
[provider.gamingpc]
kind = "openai"
base_url = "http://100.71.238.128:11434/v1"

[tier.local]
chain = [
  { provider = "gamingpc", model = "qwen3-coder:30b" },
]
`
	n := testNest(t, user, "", "")
	c, err := Load(n, FlagOverrides{})
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers["gamingpc"].BaseURL == "" {
		t.Error("custom provider lost")
	}
	if c.Tiers["local"].Chain[0].Provider != "gamingpc" {
		t.Error("tier override lost")
	}
	if c.Origin("tier.local") != "user" {
		t.Errorf("origin = %q", c.Origin("tier.local"))
	}
}

func TestModelFlagOverridesFrontierHead(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test")
	n := testNest(t, "", "", "")
	c, err := Load(n, FlagOverrides{Model: "claude-sonnet-5"})
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Tiers["frontier"].Chain[0].Model; got != "claude-sonnet-5" {
		t.Errorf("frontier head = %q", got)
	}
}
