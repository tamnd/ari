// The M1 walkthrough as a runnable replay gate (spec 2085 plan/02 section
// 5). The m1demo module under fixtures/ is the demo repo. The scripted
// provider below drives the whole M1 spine over it: the ant edits greeter.go
// and in the edit references time.Now() without importing time, the language
// server flags the undefined symbol in the edit result, the ant reads the
// diagnostic and corrects it, the ARI.md house rule fires a go test that
// passes, and a /changelog skill adds a CHANGELOG entry. If the loop stops
// working, this test goes red and the release does not ship (D22, D23).
//
// The diagnostics come from a content-keyed fake, not a live gopls, so the
// gate stays deterministic: a release check must not flake on a language
// server's startup timing. The real gopls adapter is proven by the LSP
// fixture suite (plan 02 slices 5 and 6); this test proves the loop wiring
// from the tool layer up.
package demo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/lsp"
	"github.com/tamnd/ari/nest"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// m1Script is the self-correcting edit loop as scripted provider turns: read
// the file, edit it into an undefined-symbol mistake, read the diagnostic and
// add the import, run the house-rule test, then invoke the changelog skill and
// write the entry.
func m1Responses() []scripted.Response {
	return []scripted.Response{
		{
			Text: "Reading greeter.go before I change it.",
			Calls: []provider.ToolCall{
				{ID: "c-read", Name: "read", Input: `{"file_path":"%ROOT%/greeter/greeter.go"}`},
			},
			Usage: provider.Usage{Input: 60, Output: 12},
			Stop:  "tool_use",
		},
		{
			Text: "Adding a time-of-day tag to the greeting.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-bad", Name: "edit", Input: `{"file_path":"%ROOT%/greeter/greeter.go","old_string":"// Greet returns a friendly hello for name.\nfunc Greet(name string) string {\n\treturn \"Hello, \" + name + \"!\"\n}","new_string":"// Greet returns a friendly hello for name, tagged with the time of day.\nfunc Greet(name string) string {\n\treturn \"Hello, \" + name + \"! Good \" + timeOfDay() + \".\"\n}\n\n// timeOfDay is a coarse label for the current part of the day.\nfunc timeOfDay() string {\n\tif time.Now().Hour() < 18 {\n\t\treturn \"day\"\n\t}\n\treturn \"evening\"\n}"}`},
			},
			Usage: provider.Usage{Input: 120, Output: 40},
			Stop:  "tool_use",
		},
		{
			Text: "The diagnostic says time is undefined; I forgot the import. Adding it.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-fix", Name: "edit", Input: `{"file_path":"%ROOT%/greeter/greeter.go","old_string":"package greeter\n","new_string":"package greeter\n\nimport \"time\"\n"}`},
			},
			Usage: provider.Usage{Input: 150, Output: 20},
			Stop:  "tool_use",
		},
		{
			Text: "ARI.md says run the tests after a change, so running them.",
			Calls: []provider.ToolCall{
				{ID: "c-test", Name: "sh", Input: `{"command":"go test ./...","description":"Run the module tests"}`},
			},
			Usage: provider.Usage{Input: 170, Output: 16},
			Stop:  "tool_use",
		},
		{
			Text: "Loading the changelog skill to record this in the house format.",
			Calls: []provider.ToolCall{
				{ID: "c-skill", Name: "skill", Input: `{"name":"changelog","args":"Added a time-of-day tag to the greeting"}`},
			},
			Usage: provider.Usage{Input: 185, Output: 18},
			Stop:  "tool_use",
		},
		{
			Text: "Writing the changelog entry.",
			Calls: []provider.ToolCall{
				{ID: "c-changelog", Name: "write", Input: `{"file_path":"%ROOT%/CHANGELOG.md","content":"# Changelog\n\n## Unreleased\n\n- Added a time-of-day tag to the greeting.\n"}`},
			},
			Usage: provider.Usage{Input: 200, Output: 22},
			Stop:  "tool_use",
		},
		{
			Text:  "Done. Greet now includes the time of day, the tests pass, and the changelog records it.",
			Usage: provider.Usage{Input: 220, Output: 26},
			Stop:  "end_turn",
		},
	}
}

// TestM1DemoReplay drives the whole M1 spine over the m1demo fixture and
// asserts the self-correcting loop landed: the language server saw the
// undefined-symbol mistake and then cleared, the tests passed, and the
// changelog entry exists.
func TestM1DemoReplay(t *testing.T) {
	root := copyFixtureNamed(t, "m1demo")

	raw := make([]json.RawMessage, len(m1Responses()))
	for i, r := range m1Responses() {
		b, err := json.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		raw[i] = json.RawMessage(strings.ReplaceAll(string(b), "%ROOT%", root))
	}
	p, err := scripted.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}

	fake := newContentLSP()
	c := openM1Colony(t, root, p, fake)

	ctx := context.Background()
	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: "Change Greet to include the time of day, then update the changelog.", Mode: core.ModeFullAuto}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	assertSelfCorrected(t, fake, root)
	assertM1Stream(t, evs)
}

// assertSelfCorrected pins the loop: the fake language server reported the
// undefined-symbol error at least once (the natural mistake), the file it
// reports on now has no errors (the correction landed), and the changelog
// entry is on disk.
func assertSelfCorrected(t *testing.T, fake *contentLSP, root string) {
	t.Helper()
	greeter := filepath.Join(root, "greeter", "greeter.go")
	if !fake.sawError(greeter) {
		t.Error("the language server never flagged the undefined time symbol, so the demo did not exercise the diagnostic")
	}
	if diags := fake.Diagnostics(greeter); len(diags) != 0 {
		t.Errorf("greeter.go still carries diagnostics after the correction: %+v", diags)
	}
	src, err := os.ReadFile(greeter)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(src), `import "time"`) {
		t.Errorf("the import fix did not land:\n%s", src)
	}
	changelog, err := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
	if err != nil {
		t.Fatalf("changelog was not written: %v", err)
	}
	if !strings.Contains(string(changelog), "- Added a time-of-day tag to the greeting.") {
		t.Errorf("changelog entry missing:\n%s", changelog)
	}
}

// assertM1Stream reconstructs the turn from the event stream the way a
// headless -p --json consumer does and pins its shape: the tool order, the
// passing test, and a clean finish.
func assertM1Stream(t *testing.T, evs []event.Event) {
	t.Helper()
	var tools []string
	var shPassed bool
	for _, e := range evs {
		switch e.Type {
		case event.TypeToolStart:
			var ts event.ToolStart
			mustDecode(t, e, &ts)
			tools = append(tools, ts.Tool)
		case event.TypeToolEnd:
			var te event.ToolEnd
			mustDecode(t, e, &te)
			if te.Tool == "sh" && te.OK {
				shPassed = true
			}
		case event.TypeError:
			t.Errorf("unexpected error event: %s", e.Payload)
		}
	}
	if got, want := strings.Join(tools, " "), "read edit edit sh skill write"; got != want {
		t.Errorf("tool order = %q, want %q", got, want)
	}
	if !shPassed {
		t.Error("go test did not pass in the stream")
	}
	last := evs[len(evs)-1]
	var fin event.TurnFinished
	mustDecode(t, last, &fin)
	if fin.Reason != "completed" {
		t.Errorf("turn.finished reason = %q, want completed", fin.Reason)
	}
}

// openM1Colony wires the ant runner, the scripted provider, and the injected
// language-server seam into a colony rooted at root, the same assembly the TUI
// and headless clients use. The workspace is trusted so the hook trust gate is
// satisfied deliberately, not bypassed (plan 02 section 5).
func openM1Colony(t *testing.T, root string, p provider.Provider, fake lsp.LSPClient) *core.Colony {
	t.Helper()
	home := t.TempDir()
	t.Setenv("ARI_HOME", home)

	// Trust the workspace before Bind reads the trust file, so a repo that
	// carried hooks would have them enabled by an explicit decision.
	n, err := nest.Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	ts := hook.LoadTrust(n.TrustFile())
	if err := ts.Trust(n.Root, fixedTime); err != nil {
		t.Fatal(err)
	}

	reg := provider.NewRegistry()
	reg.AddProvider(p)
	if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
		t.Fatal(err)
	}

	r := ant.NewRunner()
	r.GitStatus = func(string) string { return "## main" }
	r.LSPClient = fake

	ctx := context.Background()
	c, err := core.Open(ctx, root,
		core.WithRunner(r),
		core.WithRegistry(reg),
		core.WithConfig(&config.Config{Mode: "ask", LSP: config.LSP{Enabled: true}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	r.Bind(c)
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

// copyFixtureNamed copies the named fixture module into a fresh temp dir so
// the replay mutates a throwaway copy, never the checked-in fixture.
func copyFixtureNamed(t *testing.T, name string) string {
	t.Helper()
	src := filepath.Join("fixtures", name)
	dst := t.TempDir()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
	real, err := filepath.EvalSymlinks(dst)
	if err != nil {
		t.Fatal(err)
	}
	return real
}
