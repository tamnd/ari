// Package demo turns the M0 walkthrough (spec 2085 doc plan/01 section 4)
// into a runnable replay fixture. The hello-ari module under fixtures/ is
// the demo repo, demo.json is the recorded provider script, and the test
// below drives the real core, loop, tools, permission pipeline, and ledger
// over it exactly as the headless `ari -p --json` job does in CI. If the
// walkthrough stops working, this test goes red and the release does not
// ship (D22, D23).
package demo

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// demoScript is the seven-step refactor as scripted provider turns: find
// the symbol, read the three files that reference it, fumble the first edit
// on a non-unique match, correct it, rename the test and the caller, then
// run go test. The prompt and the responses are the source of truth;
// demo.json is their checked-in serialization (D23).
func demoScript() eval.Script {
	responses := []scripted.Response{
		{
			Text: "Finding every place Greeting is used before I touch anything.",
			Calls: []provider.ToolCall{
				{ID: "c-find", Name: "find", Input: `{"content":"Greeting"}`},
			},
			Usage: provider.Usage{Input: 60, Output: 14},
			Stop:  "tool_use",
		},
		{
			Text: "Reading the three files so I can edit them safely.",
			Calls: []provider.ToolCall{
				{ID: "c-read-greet", Name: "read", Input: `{"file_path":"%ROOT%/greet/greet.go"}`},
				{ID: "c-read-test", Name: "read", Input: `{"file_path":"%ROOT%/greet/greet_test.go"}`},
				{ID: "c-read-main", Name: "read", Input: `{"file_path":"%ROOT%/main.go"}`},
			},
			Usage: provider.Usage{Input: 90, Output: 22},
			Stop:  "tool_use",
		},
		{
			Text: "Renaming the function.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-bad", Name: "edit", Input: `{"file_path":"%ROOT%/greet/greet.go","old_string":"Greeting","new_string":"Greet"}`},
			},
			Usage: provider.Usage{Input: 110, Output: 18},
			Stop:  "tool_use",
		},
		{
			Text: "Greeting appears twice, so I will match the comment and the signature together to make it unique.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-greet", Name: "edit", Input: `{"file_path":"%ROOT%/greet/greet.go","old_string":"// Greeting returns a friendly hello for name.\nfunc Greeting(name string) string","new_string":"// Greet returns a friendly hello for name.\nfunc Greet(name string) string"}`},
			},
			Usage: provider.Usage{Input: 130, Output: 30},
			Stop:  "tool_use",
		},
		{
			Text: "Updating the test to call the renamed function.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-test", Name: "edit", Input: `{"file_path":"%ROOT%/greet/greet_test.go","old_string":"Greeting(\"ari\")","new_string":"Greet(\"ari\")"}`},
			},
			Usage: provider.Usage{Input: 145, Output: 20},
			Stop:  "tool_use",
		},
		{
			Text: "Updating the caller in main.",
			Calls: []provider.ToolCall{
				{ID: "c-edit-main", Name: "edit", Input: `{"file_path":"%ROOT%/main.go","old_string":"greet.Greeting(\"world\")","new_string":"greet.Greet(\"world\")"}`},
			},
			Usage: provider.Usage{Input: 155, Output: 20},
			Stop:  "tool_use",
		},
		{
			Text: "Running the tests to confirm the rename compiles and passes.",
			Calls: []provider.ToolCall{
				{ID: "c-sh", Name: "sh", Input: `{"command":"go test ./...","description":"Run the package tests"}`},
			},
			Usage: provider.Usage{Input: 170, Output: 16},
			Stop:  "tool_use",
		},
		{
			Text:  "Done. Greeting is now Greet across the package and go test passes.",
			Usage: provider.Usage{Input: 190, Output: 24},
			Stop:  "end_turn",
		},
	}

	raw := make([]json.RawMessage, len(responses))
	for i, r := range responses {
		b, err := json.Marshal(r)
		if err != nil {
			panic(err)
		}
		raw[i] = b
	}
	return eval.Script{
		Name:      "hello-ari-rename",
		Prompt:    "Rename Greeting to Greet across the package and update the test, then run go test.",
		Responses: raw,
	}
}

// TestDemoReplay is the walkthrough as a gate. It regenerates demo.json
// under -update and otherwise asserts the checked-in file still matches the
// in-code script, then drives the whole spine over a copy of the fixture.
func TestDemoReplay(t *testing.T) {
	script := demoScript()
	path := filepath.Join("fixtures", "demo.json")

	if eval.Updating() {
		if err := eval.SaveScript(path, script); err != nil {
			t.Fatal(err)
		}
	}
	assertFixtureMatches(t, path, script)

	root := copyFixture(t)
	raw := make([]json.RawMessage, len(script.Responses))
	for i, r := range script.Responses {
		raw[i] = json.RawMessage(strings.ReplaceAll(string(r), "%ROOT%", root))
	}
	p, err := scripted.FromRaw(raw)
	if err != nil {
		t.Fatal(err)
	}

	c := openColony(t, root, p)
	ctx := context.Background()
	sub, err := c.Events(ctx, core.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	sid, err := c.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	// Full-auto is the flag the CI job passes; the safety floor still holds
	// under it, which the doctor and permission tests cover.
	if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: script.Prompt, Mode: core.ModeFullAuto}); err != nil {
		t.Fatal(err)
	}
	evs := collect(t, sub, event.TypeTurnFinished)

	assertRenamed(t, root)
	assertStream(t, evs)
}

// assertRenamed checks the refactor landed on disk: the function is
// declared as Greet, and both callers reference it. The passing go test in
// the stream is the compile-level proof; these checks pin the exact edits.
func assertRenamed(t *testing.T, root string) {
	t.Helper()
	cases := []struct{ file, gone, want string }{
		{"greet/greet.go", "func Greeting(", "func Greet("},
		{"greet/greet_test.go", "Greeting(\"ari\")", "Greet(\"ari\")"},
		{"main.go", "greet.Greeting(", "greet.Greet("},
	}
	for _, c := range cases {
		data, err := os.ReadFile(filepath.Join(root, c.file))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), c.gone) {
			t.Errorf("%s still has %q after the rename:\n%s", c.file, c.gone, data)
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("%s is missing %q after the rename:\n%s", c.file, c.want, data)
		}
	}
}

// assertStream reconstructs the turn from the event stream alone, the way a
// downstream -p --json consumer does, and pins the walkthrough's shape: the
// tool order, the rejected-then-corrected edit, a passing go test, and a
// clean finish.
func assertStream(t *testing.T, evs []event.Event) {
	t.Helper()
	var tools []string
	var editOK []bool
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
			if te.Tool == "edit" {
				editOK = append(editOK, te.OK)
			}
			if te.Tool == "sh" && te.OK {
				shPassed = true
			}
		case event.TypeError:
			t.Errorf("unexpected error event: %s", e.Payload)
		}
	}

	if got, want := strings.Join(tools, " "), "find read read read edit edit edit edit sh"; got != want {
		t.Errorf("tool order = %q, want %q", got, want)
	}
	// The first edit is rejected (not unique), the next three land.
	want := []bool{false, true, true, true}
	if len(editOK) != len(want) {
		t.Fatalf("edit outcomes = %v, want %v", editOK, want)
	}
	for i := range want {
		if editOK[i] != want[i] {
			t.Errorf("edit %d ok = %v, want %v (the demo shows one rejection that self-corrects)", i, editOK[i], want[i])
		}
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

// openColony wires the ant runner and the scripted provider into a colony
// rooted at root, the same assembly the TUI and headless clients use.
func openColony(t *testing.T, root string, p provider.Provider) *core.Colony {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())

	reg := provider.NewRegistry()
	reg.AddProvider(p)
	if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
		t.Fatal(err)
	}

	r := ant.NewRunner()
	r.GitStatus = func(string) string { return "## main" }

	ctx := context.Background()
	c, err := core.Open(ctx, root,
		core.WithRunner(r),
		core.WithRegistry(reg),
		core.WithConfig(&config.Config{Mode: "ask"}),
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

// copyFixture copies the hello-ari module into a fresh temp dir so the
// replay mutates a throwaway copy, never the checked-in fixture.
func copyFixture(t *testing.T) string {
	t.Helper()
	src := filepath.Join("fixtures", "hello-ari")
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

func collect(t *testing.T, sub *core.Subscription, until event.Type) []event.Event {
	t.Helper()
	var out []event.Event
	deadline := time.After(30 * time.Second)
	for {
		select {
		case e := <-sub.C:
			out = append(out, e)
			if e.Type == until {
				return out
			}
		case <-sub.Done():
			return out
		case <-deadline:
			t.Fatalf("timed out waiting for %s; saw %d events", until, len(out))
		}
	}
}

func mustDecode(t *testing.T, e event.Event, dst any) {
	t.Helper()
	if err := json.Unmarshal(e.Payload, dst); err != nil {
		t.Fatalf("decode %s: %v", e.Type, err)
	}
}

func assertFixtureMatches(t *testing.T, path string, script eval.Script) {
	t.Helper()
	on, err := eval.LoadScript(path)
	if err != nil {
		t.Fatalf("load %s (run with -update to create it): %v", path, err)
	}
	got, err := json.Marshal(on)
	if err != nil {
		t.Fatal(err)
	}
	want, err := json.Marshal(script)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("%s is stale; rerun with -update", path)
	}
}
