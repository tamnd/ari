package doctor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/nest"
)

// freshNest points a nest at a temp dir and returns a context whose config
// loaded cleanly, the shape doctor sees on a healthy fresh install.
func freshNest(t *testing.T) *Context {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())
	// The default config routes the frontier tier at anthropic, so a load
	// needs the key present; doctor's own runs tolerate its absence.
	t.Setenv("ANTHROPIC_API_KEY", "test-key")
	root := t.TempDir()
	n, err := nest.Resolve(root)
	if err != nil {
		t.Fatalf("resolve nest: %v", err)
	}
	cfg, lerr := config.Load(n, config.FlagOverrides{})
	if lerr != nil {
		t.Fatalf("load config: %v", lerr)
	}
	return &Context{Nest: n, Config: cfg}
}

// findingFor returns the finding for a named check, failing if absent.
func findingFor(t *testing.T, r Report, check string) Finding {
	t.Helper()
	for _, f := range r.Findings {
		if f.Check == check {
			return f
		}
	}
	t.Fatalf("no finding for %q", check)
	return Finding{}
}

func TestFreshNestIsClean(t *testing.T) {
	ctx := freshNest(t)
	r := New().Run(ctx)
	if got := r.Worst(); got != StatusOK {
		t.Fatalf("fresh nest worst = %v, want ok", got)
	}
	for _, f := range r.Findings {
		if f.Status != StatusOK {
			t.Errorf("check %q not ok on fresh nest: %s", f.Check, f.Reason)
		}
	}
}

func TestLiteralSecretIsCritical(t *testing.T) {
	ctx := freshNest(t)
	body := "[provider.anthropic]\nkind = \"anthropic\"\nbase_url = \"https://api.anthropic.com\"\napi_key = \"sk-ant-not-a-reference-000\"\n"
	if err := os.WriteFile(ctx.Nest.GlobalConfig(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, New().Run(ctx), "secrets in config")
	if f.Status != StatusCritical {
		t.Fatalf("literal key status = %v, want critical", f.Status)
	}
	if f.Manual == "" {
		t.Error("a literal-secret finding must carry manual guidance")
	}
	// The reason names the key but never the value (D16).
	if want := "sk-ant-not-a-reference-000"; strings.Contains(f.Reason, want) {
		t.Error("doctor leaked the secret value into a finding")
	}
}

func TestEnvReferenceIsClean(t *testing.T) {
	ctx := freshNest(t)
	body := "[provider.anthropic]\napi_key = \"${ANTHROPIC_API_KEY}\"\n"
	if err := os.WriteFile(ctx.Nest.GlobalConfig(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, New().Run(ctx), "secrets in config")
	if f.Status != StatusOK {
		t.Fatalf("env reference status = %v, want ok", f.Status)
	}
}

func TestWorldReadableAuthIsCriticalAndFixable(t *testing.T) {
	ctx := freshNest(t)
	if err := os.MkdirAll(ctx.Nest.AuthDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	cred := filepath.Join(ctx.Nest.AuthDir(), "anthropic.json")
	if err := os.WriteFile(cred, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, New().Run(ctx), "nest permissions")
	if f.Status != StatusCritical {
		t.Fatalf("world-readable credential status = %v, want critical", f.Status)
	}
	if f.Fix == nil {
		t.Fatal("a loose-permission finding must offer a fix")
	}
	if err := f.Fix(ctx); err != nil {
		t.Fatalf("fix: %v", err)
	}
	info, err := os.Stat(cred)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("after fix credential mode = %o, want 600", info.Mode().Perm())
	}
	if again := findingFor(t, New().Run(ctx), "nest permissions"); again.Status != StatusOK {
		t.Fatalf("after fix status = %v, want ok", again.Status)
	}
}

func TestLocalConfigGitignoreWarnsAndFixes(t *testing.T) {
	ctx := freshNest(t)
	// Make the root look like a repo with a project nest but no ignore.
	if err := os.MkdirAll(filepath.Join(ctx.Nest.Root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ctx.Nest.ProjectDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, New().Run(ctx), "local config gitignore")
	if f.Status != StatusWarn {
		t.Fatalf("missing ignore status = %v, want warn", f.Status)
	}
	if f.Fix == nil {
		t.Fatal("the ignore finding must offer a fix")
	}
	if err := f.Fix(ctx); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if again := findingFor(t, New().Run(ctx), "local config gitignore"); again.Status != StatusOK {
		t.Fatalf("after fix status = %v, want ok", again.Status)
	}
	data, err := os.ReadFile(filepath.Join(ctx.Nest.Root, ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), ".ari/local.toml") {
		t.Errorf("gitignore did not gain the ignore line, got %q", data)
	}
}

func TestGitignoreAlreadyCoveredIsClean(t *testing.T) {
	ctx := freshNest(t)
	if err := os.MkdirAll(filepath.Join(ctx.Nest.Root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ctx.Nest.ProjectDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ctx.Nest.Root, ".gitignore"), []byte("# junk\n.ari/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if f := findingFor(t, New().Run(ctx), "local config gitignore"); f.Status != StatusOK {
		t.Fatalf("covered ignore status = %v, want ok", f.Status)
	}
}

func TestJournalGapIsCritical(t *testing.T) {
	ctx := freshNest(t)
	dir := ctx.Nest.JournalDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two events with a hole where Seq 2 should be: an edited history.
	lines := "{\"v\":1,\"seq\":1,\"type\":\"hello\",\"time\":\"2026-01-01T00:00:00Z\"}\n" +
		"{\"v\":1,\"seq\":3,\"type\":\"log\",\"time\":\"2026-01-01T00:00:01Z\"}\n"
	if err := os.WriteFile(filepath.Join(dir, "events-00001.jsonl"), []byte(lines), 0o644); err != nil {
		t.Fatal(err)
	}
	f := findingFor(t, New().Run(ctx), "journal continuity")
	if f.Status != StatusCritical {
		t.Fatalf("journal gap status = %v, want critical", f.Status)
	}
}

func TestConfigLoadErrorIsCritical(t *testing.T) {
	ctx := freshNest(t)
	ctx.Config = nil
	ctx.LoadErr = os.ErrInvalid
	if f := findingFor(t, New().Run(ctx), "config health"); f.Status != StatusCritical {
		t.Fatalf("load error status = %v, want critical", f.Status)
	}
}

func TestFullAutoDefaultWarns(t *testing.T) {
	ctx := freshNest(t)
	ctx.Config.Mode = "full-auto"
	if f := findingFor(t, New().Run(ctx), "permission mode"); f.Status != StatusWarn {
		t.Fatalf("full-auto default status = %v, want warn", f.Status)
	}
}

// withProjectHook rewrites the project config to carry one repo hook and
// reloads, so the trust check sees a repo hook to gate.
func withProjectHook(t *testing.T, ctx *Context) {
	t.Helper()
	if err := os.MkdirAll(ctx.Nest.ProjectDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "[[hooks.PostToolUse]]\nmatcher = \"write\"\ncommand = \"gofmt -w .\"\n"
	if err := os.WriteFile(ctx.Nest.ProjectConfig(), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(ctx.Nest, config.FlagOverrides{})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	ctx.Config = cfg
}

func TestUntrustedWorkspaceWithRepoHookWarns(t *testing.T) {
	ctx := freshNest(t)
	withProjectHook(t, ctx)
	f := findingFor(t, New().Run(ctx), "workspace trust")
	if f.Status != StatusWarn {
		t.Fatalf("untrusted repo hook status = %v, want warn", f.Status)
	}
	if !strings.Contains(f.Reason, "gofmt") {
		t.Errorf("finding should name the repo hook: %s", f.Reason)
	}
}

func TestTrustedWorkspaceWithRepoHookIsOK(t *testing.T) {
	ctx := freshNest(t)
	withProjectHook(t, ctx)
	if err := hook.LoadTrust(ctx.Nest.TrustFile()).Trust(ctx.Nest.Root, time.Unix(1, 0)); err != nil {
		t.Fatal(err)
	}
	if f := findingFor(t, New().Run(ctx), "workspace trust"); f.Status != StatusOK {
		t.Fatalf("trusted repo hook status = %v, want ok (%s)", f.Status, f.Reason)
	}
}
