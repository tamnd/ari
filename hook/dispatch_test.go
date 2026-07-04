package hook

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"
)

// recorder is a fake process: it records every command it was asked to run and
// returns a canned result per command string, so a test proves which hooks the
// dispatcher actually spawned without touching a shell.
type recorder struct {
	mu      sync.Mutex
	ran     []string
	results map[string]Result
}

func (r *recorder) run(_ context.Context, c Command, _ []byte, _ []string) Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ran = append(r.ran, c.Command)
	if res, ok := r.results[c.Command]; ok {
		res.Event = c.Event
		return res
	}
	return Result{Event: c.Event, ExitCode: 0}
}

func (r *recorder) commands() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ran...)
}

func build(t *testing.T, ev Event, layer, command, matcher string) Command {
	t.Helper()
	c, err := Spec{Command: command, Matcher: matcher}.Build(ev, layer)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return c
}

// TestUntrustedWorkspaceRunsNoRepoHook is the adversarial DoD test: a hook that
// a repo carries in its committed config must not run until the operator trusts
// the workspace. A cloned repo full of hooks is inert on first open.
func TestUntrustedWorkspaceRunsNoRepoHook(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(Options{
		Commands: []Command{
			build(t, PreToolUse, "project", "attacker-payload", "*"),
			build(t, PreToolUse, "local", "also-attacker", "*"),
		},
		Trusted: false,
		run:     rec.run,
	})
	if r.Any() {
		t.Fatal("Any should be false: every hook is repo-layer and the workspace is untrusted")
	}
	out := r.Fire(context.Background(), PreToolUse, Payload{Tool: "sh"})
	if out.Block || len(out.Results) != 0 {
		t.Fatalf("untrusted repo hook fired: %+v", out)
	}
	if got := rec.commands(); len(got) != 0 {
		t.Fatalf("a repo hook ran in an untrusted workspace: %v", got)
	}
}

// TestTrustedWorkspaceRunsRepoHook is the flip side: once trusted, the repo
// hook runs.
func TestTrustedWorkspaceRunsRepoHook(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(Options{
		Commands: []Command{build(t, PreToolUse, "project", "repo-hook", "*")},
		Trusted:  true,
		run:      rec.run,
	})
	if !r.Any() {
		t.Fatal("Any should be true in a trusted workspace")
	}
	r.Fire(context.Background(), PreToolUse, Payload{Tool: "sh"})
	if got := rec.commands(); len(got) != 1 || got[0] != "repo-hook" {
		t.Fatalf("trusted repo hook did not run: %v", got)
	}
}

// TestUserHookRunsInUntrustedWorkspace: the operator's own user-layer hook runs
// regardless of workspace trust, because the user wrote it.
func TestUserHookRunsInUntrustedWorkspace(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(Options{
		Commands: []Command{build(t, PreToolUse, "user", "user-hook", "*")},
		Trusted:  false,
		run:      rec.run,
	})
	if !r.Any() {
		t.Fatal("a user hook always counts")
	}
	r.Fire(context.Background(), PreToolUse, Payload{Tool: "sh"})
	if got := rec.commands(); len(got) != 1 || got[0] != "user-hook" {
		t.Fatalf("user hook did not run: %v", got)
	}
}

// TestUntrustedContentSessionRunsNothing: an automation session fed untrusted
// content runs no hook at all, even a user hook in a trusted workspace.
func TestUntrustedContentSessionRunsNothing(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(Options{
		Commands:  []Command{build(t, PreToolUse, "user", "user-hook", "*")},
		Trusted:   true,
		Untrusted: true,
		run:       rec.run,
	})
	if r.Enabled() {
		t.Fatal("an untrusted-content session must be disabled")
	}
	if r.Any() {
		t.Fatal("Any must be false for an untrusted-content session")
	}
	out := r.Fire(context.Background(), PreToolUse, Payload{Tool: "sh"})
	if len(out.Results) != 0 || len(rec.commands()) != 0 {
		t.Fatalf("a hook ran in an untrusted-content session: %+v %v", out, rec.commands())
	}
}

func TestFireMatcherFiltersByTool(t *testing.T) {
	rec := &recorder{}
	r := NewRunner(Options{
		Commands: []Command{build(t, PreToolUse, "user", "on-write", "write")},
		Trusted:  true,
		run:      rec.run,
	})
	r.Fire(context.Background(), PreToolUse, Payload{Tool: "read"})
	if len(rec.commands()) != 0 {
		t.Fatal("matcher should have filtered out the read call")
	}
	r.Fire(context.Background(), PreToolUse, Payload{Tool: "write"})
	if got := rec.commands(); len(got) != 1 {
		t.Fatalf("matcher should have matched the write call: %v", got)
	}
}

func TestFireAggregatesBlocksAndContext(t *testing.T) {
	rec := &recorder{results: map[string]Result{
		"blocker": {Blocking: true, Message: "first said no"},
		"ctxer":   {Output: &Output{AdditionalContext: "added"}},
	}}
	r := NewRunner(Options{
		Commands: []Command{
			build(t, PostToolUse, "user", "blocker", "*"),
			build(t, PostToolUse, "user", "ctxer", "*"),
		},
		Trusted: true,
		run:     rec.run,
	})
	out := r.Fire(context.Background(), PostToolUse, Payload{Tool: "sh"})
	if !out.Block || !strings.Contains(out.Message, "first said no") {
		t.Fatalf("block not aggregated: %+v", out)
	}
	if !strings.Contains(out.Context, "added") {
		t.Fatalf("context not aggregated: %+v", out)
	}
}

func TestFireStopContinue(t *testing.T) {
	rec := &recorder{results: map[string]Result{
		"stopper": {Output: &Output{Continue: new(false), StopReason: "enough"}},
	}}
	r := NewRunner(Options{
		Commands: []Command{build(t, Stop, "user", "stopper", "*")},
		Trusted:  true,
		run:      rec.run,
	})
	out := r.Fire(context.Background(), Stop, Payload{})
	if out.StopContinue == nil || *out.StopContinue {
		t.Fatalf("stopContinue should be false: %+v", out)
	}
	if out.StopReason != "enough" {
		t.Fatalf("stopReason = %q", out.StopReason)
	}
}

func TestFireOnceRunsOnce(t *testing.T) {
	rec := &recorder{}
	c := build(t, SessionStart, "user", "once-hook", "*")
	c.Once = true
	r := NewRunner(Options{Commands: []Command{c}, Trusted: true, run: rec.run})
	r.Fire(context.Background(), SessionStart, Payload{})
	r.Fire(context.Background(), SessionStart, Payload{})
	if got := rec.commands(); len(got) != 1 {
		t.Fatalf("once hook ran %d times, want 1", len(got))
	}
}

func TestFireAsyncDoesNotBlock(t *testing.T) {
	ran := make(chan struct{}, 1)
	async := func(_ context.Context, _ Command, _ []byte, _ []string) Result {
		ran <- struct{}{}
		return Result{}
	}
	c := build(t, PostToolUse, "user", "async-hook", "*")
	c.Async = true
	r := NewRunner(Options{Commands: []Command{c}, Trusted: true, run: async})
	out := r.Fire(context.Background(), PostToolUse, Payload{Tool: "sh"})
	if len(out.Results) != 0 {
		t.Fatal("an async hook returns no synchronous result")
	}
	select {
	case <-ran:
	case <-time.After(time.Second):
		t.Fatal("async hook never ran")
	}
}

func TestDescribe(t *testing.T) {
	c := build(t, PreToolUse, "project", "gofmt -w $ARI_TOOL_FILE", "write|edit")
	got := Describe(c)
	for _, want := range []string{"PreToolUse", "project", "write|edit", "gofmt"} {
		if !strings.Contains(got, want) {
			t.Errorf("Describe missing %q: %s", want, got)
		}
	}
}
