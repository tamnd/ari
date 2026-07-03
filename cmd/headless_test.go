package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// scriptedOpts is the injectable half of a headless test: a scripted
// provider behind the frontier tier, a preloaded config carrying the
// mode, and a temp global nest so nothing touches the real one.
func scriptedOpts(t *testing.T, p provider.Provider, mode string) []core.Option {
	t.Helper()
	t.Setenv("ARI_HOME", t.TempDir())
	reg := provider.NewRegistry()
	reg.AddProvider(p)
	if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
		t.Fatal(err)
	}
	return []core.Option{core.WithRegistry(reg), core.WithConfig(&config.Config{Mode: mode})}
}

// readSummarize is the demo script: read main.go, then summarize.
func readSummarize(root string) *scripted.Provider {
	return scripted.New(
		scripted.Response{
			Text: "Reading it first.",
			Calls: []provider.ToolCall{{
				ID:    "call-read",
				Name:  "read",
				Input: fmt.Sprintf(`{"file_path":%q}`, filepath.Join(root, "main.go")),
			}},
			Usage: provider.Usage{Input: 40, Output: 10},
			Stop:  "tool_use",
		},
		scripted.Response{
			Text:  "main.go declares package main and prints hello.",
			Usage: provider.Usage{Input: 60, Output: 12},
			Stop:  "end_turn",
		},
	)
}

func writeMainGo(t *testing.T, root string) {
	t.Helper()
	src := "package main\n\nfunc main() { println(\"hello\") }\n"
	if err := os.WriteFile(filepath.Join(root, "main.go"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestHeadlessPlainSummary is the slice's first DoD line: ari -p in a
// fixture repo prints the summary and exits zero, on the same core,
// loop, and tools the TUI runs (D2).
func TestHeadlessPlainSummary(t *testing.T) {
	root := t.TempDir()
	writeMainGo(t, root)
	var out bytes.Buffer

	err := oneShot(context.Background(), shot{
		Dir:    root,
		Prompt: "read main.go and summarize it",
		Out:    &out,
		Opts:   scriptedOpts(t, readSummarize(root), "full-auto"),
	})
	if err != nil {
		t.Fatalf("oneShot: %v", err)
	}
	if code := exitCode(err); code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if got, want := out.String(), "main.go declares package main and prints hello.\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

// TestHeadlessJSONStream pins the --json contract: a hello first, then
// the raw envelopes in journal order, and the transcript reconstructs
// from the stream alone (doc 01 sections 5.1 and 5.4).
func TestHeadlessJSONStream(t *testing.T) {
	root := t.TempDir()
	writeMainGo(t, root)
	var out bytes.Buffer

	err := oneShot(context.Background(), shot{
		Dir:    root,
		Prompt: "read main.go and summarize it",
		JSON:   true,
		Out:    &out,
		Opts:   scriptedOpts(t, readSummarize(root), "full-auto"),
	})
	if err != nil {
		t.Fatalf("oneShot: %v", err)
	}

	var events []event.Event
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var e event.Event
		if uerr := json.Unmarshal([]byte(line), &e); uerr != nil {
			t.Fatalf("line %q is not an event envelope: %v", line, uerr)
		}
		events = append(events, e)
	}
	if len(events) == 0 || events[0].Type != event.TypeHello {
		t.Fatalf("first event = %v, want hello", events[0].Type)
	}

	// Journal order: seq never goes backward after the hello.
	last := uint64(0)
	for _, e := range events[1:] {
		if e.Seq < last {
			t.Errorf("seq went backward: %d after %d", e.Seq, last)
		}
		last = e.Seq
	}

	// Reconstruct the transcript from the stream alone: the prompt, the
	// tool sequence, the final text, the terminal reason.
	var prompt, finalText, reason string
	var tools []string
	for _, e := range events {
		switch e.Type {
		case event.TypeTurnStarted:
			var ts event.TurnStarted
			if derr := e.Decode(&ts); derr != nil {
				t.Fatal(derr)
			}
			prompt = ts.Prompt
		case event.TypeToolStart:
			var ts event.ToolStart
			if derr := e.Decode(&ts); derr != nil {
				t.Fatal(derr)
			}
			tools = append(tools, ts.Tool)
			finalText = ""
		case event.TypeTextDelta:
			var d event.TextDelta
			if derr := e.Decode(&d); derr != nil {
				t.Fatal(derr)
			}
			finalText += d.Text
		case event.TypeTurnFinished:
			var fin event.TurnFinished
			if derr := e.Decode(&fin); derr != nil {
				t.Fatal(derr)
			}
			reason = fin.Reason
		}
	}
	if prompt != "read main.go and summarize it" {
		t.Errorf("reconstructed prompt = %q", prompt)
	}
	if got, want := strings.Join(tools, " "), "read"; got != want {
		t.Errorf("reconstructed tools = %q, want %q", got, want)
	}
	if finalText != "main.go declares package main and prints hello." {
		t.Errorf("reconstructed final text = %q", finalText)
	}
	if reason != "completed" {
		t.Errorf("reconstructed reason = %q, want completed", reason)
	}
}

// TestHeadlessAutoDenies is the hard rule made a test: a headless turn
// that hits a permission gate auto-denies with the headless kind and
// exits non-zero instead of hanging (doc 05 section 11, D16).
func TestHeadlessAutoDenies(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "pwned.txt")
	p := scripted.New(
		scripted.Response{
			Text: "Creating the file.",
			Calls: []provider.ToolCall{{
				ID:    "call-sh",
				Name:  "sh",
				Input: fmt.Sprintf(`{"command":"touch %s","description":"Create a file"}`, target),
			}},
			Usage: provider.Usage{Input: 40, Output: 10},
			Stop:  "tool_use",
		},
		scripted.Response{
			Text:  "The command was denied, so nothing was created.",
			Usage: provider.Usage{Input: 60, Output: 12},
			Stop:  "end_turn",
		},
	)
	var out bytes.Buffer

	err := oneShot(context.Background(), shot{
		Dir:    root,
		Prompt: "create pwned.txt",
		JSON:   true,
		Out:    &out,
		Opts:   scriptedOpts(t, p, "ask"),
	})
	if core.KindOf(err) != core.ErrPermission {
		t.Fatalf("error kind = %q, want permission (err: %v)", core.KindOf(err), err)
	}
	if code := exitCode(err); code != 5 {
		t.Errorf("exit code = %d, want 5", code)
	}
	if _, serr := os.Stat(target); !os.IsNotExist(serr) {
		t.Errorf("pwned.txt exists; the deny did not hold")
	}
	if kind := deniedKind(t, out.String()); kind != "headless" {
		t.Errorf("permission.resolved kind = %q, want headless", kind)
	}
}

// TestHeadlessFullAutoSafetyFloor: full-auto is the deliberate CI flag,
// and it still cannot cross the safety floor, so a headless run in an
// untrusted repo cannot be tricked into writing the nest (D15, D16).
func TestHeadlessFullAutoSafetyFloor(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, ".ari", "hijack.toml")
	p := scripted.New(
		scripted.Response{
			Text: "Writing the config.",
			Calls: []provider.ToolCall{{
				ID:    "call-write",
				Name:  "write",
				Input: fmt.Sprintf(`{"file_path":%q,"content":"[tier.frontier]\n"}`, nested),
			}},
			Usage: provider.Usage{Input: 40, Output: 10},
			Stop:  "tool_use",
		},
		scripted.Response{
			Text:  "The write was blocked.",
			Usage: provider.Usage{Input: 60, Output: 12},
			Stop:  "end_turn",
		},
	)
	var out bytes.Buffer

	err := oneShot(context.Background(), shot{
		Dir:    root,
		Prompt: "write .ari/hijack.toml",
		Mode:   "full-auto",
		JSON:   true,
		Out:    &out,
		Opts:   scriptedOpts(t, p, "full-auto"),
	})
	if err != nil {
		t.Fatalf("oneShot: %v", err)
	}
	if _, serr := os.Stat(nested); !os.IsNotExist(serr) {
		t.Errorf(".ari/hijack.toml exists; the safety floor did not hold")
	}
	if kind := deniedKind(t, out.String()); kind != "safety" {
		t.Errorf("permission.resolved kind = %q, want safety", kind)
	}
}

// deniedKind digs the first deny out of a JSON stream and returns its
// kind, so the tests assert the reason machinery, not just the effect.
func deniedKind(t *testing.T, stream string) string {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(stream), "\n") {
		var e event.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		if e.Type != event.TypePermissionResolved {
			continue
		}
		var pr event.PermissionResolved
		if err := e.Decode(&pr); err != nil {
			t.Fatal(err)
		}
		if pr.Behavior == "deny" {
			return pr.Kind
		}
	}
	return ""
}

// TestHeadlessMatchesTUIStream runs the same demo through the direct
// colony subscription (the TUI's path) and through -p --json, and
// asserts the event streams match modulo timing (D2).
func TestHeadlessMatchesTUIStream(t *testing.T) {
	prompt := "read main.go and summarize it"

	// The TUI path: subscribe, submit, drain, exactly what cmd/run.go
	// wires the program to.
	tuiRoot := t.TempDir()
	writeMainGo(t, tuiRoot)
	tuiTypes := func() []event.Type {
		t.Setenv("ARI_HOME", t.TempDir())
		reg := provider.NewRegistry()
		p := readSummarize(tuiRoot)
		reg.AddProvider(p)
		if err := reg.AddTier("frontier", []provider.ChainLink{{Provider: p.Name(), Model: "fable-test"}}); err != nil {
			t.Fatal(err)
		}
		runner := ant.NewRunner()
		ctx := context.Background()
		c, err := core.Open(ctx, tuiRoot,
			core.WithRunner(runner),
			core.WithRegistry(reg),
			core.WithConfig(&config.Config{Mode: "full-auto"}),
		)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = c.Close() }()
		runner.Bind(c)
		sub, err := c.Events(ctx, core.EventFilter{})
		if err != nil {
			t.Fatal(err)
		}
		defer sub.Cancel()
		if err := c.Start(ctx); err != nil {
			t.Fatal(err)
		}
		sid, err := c.NewSession(ctx, core.NewSessionRequest{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := c.Submit(ctx, core.SubmitRequest{Session: sid, Text: prompt}); err != nil {
			t.Fatal(err)
		}
		var types []event.Type
		for e := range sub.C {
			types = append(types, e.Type)
			if e.Type == event.TypeTurnFinished {
				break
			}
		}
		return types
	}()

	// The headless path over a fresh, identical fixture.
	headRoot := t.TempDir()
	writeMainGo(t, headRoot)
	var out bytes.Buffer
	err := oneShot(context.Background(), shot{
		Dir:    headRoot,
		Prompt: prompt,
		JSON:   true,
		Out:    &out,
		Opts:   scriptedOpts(t, readSummarize(headRoot), "full-auto"),
	})
	if err != nil {
		t.Fatal(err)
	}
	var headTypes []event.Type
	for line := range strings.SplitSeq(strings.TrimSpace(out.String()), "\n") {
		var e event.Event
		if uerr := json.Unmarshal([]byte(line), &e); uerr != nil {
			t.Fatal(uerr)
		}
		headTypes = append(headTypes, e.Type)
	}

	if got, want := fmt.Sprint(headTypes), fmt.Sprint(tuiTypes); got != want {
		t.Errorf("streams differ modulo timing\nheadless: %v\ntui:      %v", headTypes, tuiTypes)
	}
}

// TestOutcomeExitCodes pins the terminal-reason-to-exit-code table (doc
// 01 section 10.2): a CI step branches on the code, never on prose.
func TestOutcomeExitCodes(t *testing.T) {
	cases := []struct {
		name   string
		fin    event.TurnFinished
		denied bool
		code   int
	}{
		{"completed", event.TurnFinished{Reason: "completed"}, false, 0},
		{"completed after headless deny", event.TurnFinished{Reason: "completed"}, true, 5},
		{"budget", event.TurnFinished{Reason: "budget_exhausted"}, false, 6},
		{"canceled", event.TurnFinished{Reason: "canceled"}, false, 7},
		{"tools canceled", event.TurnFinished{Reason: "tools_canceled"}, false, 7},
		{"model error", event.TurnFinished{Reason: "model_error"}, false, 4},
		{"prompt too long", event.TurnFinished{Reason: "prompt_too_long"}, false, 4},
		{"max turns", event.TurnFinished{Reason: "max_turns"}, false, 1},
		{"unknown", event.TurnFinished{Reason: "elsewise"}, false, 1},
	}
	for _, tc := range cases {
		if code := exitCode(outcome(tc.fin, tc.denied, nil)); code != tc.code {
			t.Errorf("%s: exit code = %d, want %d", tc.name, code, tc.code)
		}
	}

	// An error reason carries the journaled code through.
	err := outcome(event.TurnFinished{Reason: "error"}, false, &event.ErrorInfo{Code: "provider", Message: "auth failed"})
	if code := exitCode(err); code != 4 {
		t.Errorf("error reason with provider code: exit = %d, want 4", code)
	}
}

// TestReadPrompt covers the stdin composition rule: -p - reads the
// pipe, and an empty prompt is a config error, not a hang.
func TestReadPrompt(t *testing.T) {
	got, err := readPrompt("-", strings.NewReader("summarize main.go\n"))
	if err != nil || got != "summarize main.go" {
		t.Errorf("readPrompt(-) = %q, %v", got, err)
	}
	if _, err := readPrompt("-", strings.NewReader("  \n")); core.KindOf(err) != core.ErrConfig {
		t.Errorf("empty stdin prompt: kind = %q, want config", core.KindOf(err))
	}
	got, err = readPrompt("do the thing", nil)
	if err != nil || got != "do the thing" {
		t.Errorf("readPrompt(arg) = %q, %v", got, err)
	}
}
