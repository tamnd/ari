package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
	"github.com/tamnd/ari/tool"
)

// stubHooks is a scripted Hooks: it returns a canned result for each seam and
// records what the lifecycle seams were called with, so a test can prove the
// loop fired them and folded their results in. PreToolUse is not here: it
// steers the permission decision through the DecideFunc seam, tested via the
// Verdict's Context below and exhaustively in the permission package.
type stubHooks struct {
	post         HookResult
	promptSubmit HookResult
	sessionStart HookResult

	stop       HookResult
	stopCalls  int
	startWith  []string
	endReason  string
	endCalled  bool
	promptSeen string
}

func (s *stubHooks) PostTool(context.Context, string, json.RawMessage, string, bool) HookResult {
	return s.post
}
func (s *stubHooks) PromptSubmit(_ context.Context, prompt string) HookResult {
	s.promptSeen = prompt
	return s.promptSubmit
}
func (s *stubHooks) SessionStart(_ context.Context, reason string) HookResult {
	s.startWith = append(s.startWith, reason)
	return s.sessionStart
}
func (s *stubHooks) SessionEnd(_ context.Context, reason string) {
	s.endCalled = true
	s.endReason = reason
}
func (s *stubHooks) Stop(context.Context) HookResult {
	s.stopCalls++
	return s.stop
}

func hookScript(tool string) provider.Provider {
	return scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{call("c1", tool, `{}`)},
			Usage: provider.Usage{Input: 1, Output: 1},
		},
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
}

func lastResult(t *testing.T, rec *recordingProvider) provider.MsgBlock {
	t.Helper()
	blocks := rec.requests[1].Messages[len(rec.requests[1].Messages)-1].Blocks
	if len(blocks) == 0 {
		t.Fatal("no tool result blocks")
	}
	return blocks[0]
}

// requestHasText reports whether any message in the idx-th recorded request
// carries the wanted text, so a test can prove injected context reached the
// model.
func requestHasText(reqs []provider.Request, idx int, want string) bool {
	if idx >= len(reqs) {
		return false
	}
	for _, m := range reqs[idx].Messages {
		for _, b := range m.Blocks {
			if strings.Contains(b.Text, want) {
				return true
			}
		}
	}
	return false
}

// TestPreToolContextViaVerdict proves a PreToolUse hook's context, carried on
// the permission Verdict, is surfaced with the tool result.
func TestPreToolContextViaVerdict(t *testing.T) {
	ok := &fakeTool{name: "reader", call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		return &tool.Result{Model: "read the file"}, nil
	}}
	rec := &recordingProvider{inner: hookScript("reader")}
	h := newHarness(rec, registry(t, ok))
	h.loop.Decide = func(context.Context, tool.Tool, json.RawMessage, string) Verdict {
		return Verdict{Allow: true, Context: "a pre-tool hook noted something"}
	}

	run(t, h, "read a file")
	got := lastResult(t, rec)
	if got.IsErr {
		t.Fatalf("pre-tool context must not error: %+v", got)
	}
	if !strings.Contains(got.Text, "read the file") || !strings.Contains(got.Text, "a pre-tool hook noted something") {
		t.Fatalf("pre-tool context not surfaced: %+v", got)
	}
}

// TestPostToolHookAppendsContext proves a passing post-tool hook appends its
// additional context to the successful result.
func TestPostToolHookAppendsContext(t *testing.T) {
	ok := &fakeTool{name: "fmt", call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		return &tool.Result{Model: "wrote file"}, nil
	}}
	rec := &recordingProvider{inner: hookScript("fmt")}
	h := newHarness(rec, registry(t, ok))
	h.loop.Hooks = &stubHooks{post: HookResult{Context: "gofmt reformatted it"}}

	run(t, h, "write and format")
	got := lastResult(t, rec)
	if got.IsErr {
		t.Fatalf("a context-only post hook must not error: %+v", got)
	}
	if !strings.Contains(got.Text, "wrote file") || !strings.Contains(got.Text, "gofmt reformatted it") {
		t.Fatalf("context not appended: %+v", got)
	}
}

// TestPostToolHookBlocksTurnsIntoError proves a post-tool block turns a
// successful call into a model-facing error carrying the block message.
func TestPostToolHookBlocksTurnsIntoError(t *testing.T) {
	ok := &fakeTool{name: "edit", call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		return &tool.Result{Model: "edited"}, nil
	}}
	rec := &recordingProvider{inner: hookScript("edit")}
	h := newHarness(rec, registry(t, ok))
	h.loop.Hooks = &stubHooks{post: HookResult{Block: true, Message: "tests failed after the edit"}}

	run(t, h, "edit then test")
	got := lastResult(t, rec)
	if !got.IsErr {
		t.Fatal("a post-tool block should mark the result as an error")
	}
	if !strings.Contains(got.Text, "tests failed after the edit") {
		t.Fatalf("block message missing: %+v", got)
	}
}

// TestNilHooksNoop proves the seam is inert when unset: the tool runs and the
// result is untouched.
func TestNilHooksNoop(t *testing.T) {
	ok := &fakeTool{name: "reader"}
	rec := &recordingProvider{inner: hookScript("reader")}
	h := newHarness(rec, registry(t, ok))
	// Hooks left nil.
	run(t, h, "just read")
	got := lastResult(t, rec)
	if got.IsErr || got.Text != "ok:reader" {
		t.Fatalf("nil hooks changed the result: %+v", got)
	}
}

// TestPromptSubmitContextInjected proves a UserPromptSubmit hook's context is
// injected into the turn the model sees.
func TestPromptSubmitContextInjected(t *testing.T) {
	rec := &recordingProvider{inner: scripted.New(
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)}
	h := newHarness(rec, registry(t))
	stub := &stubHooks{promptSubmit: HookResult{Context: "the repo uses tabs"}}
	h.loop.Hooks = stub

	out := run(t, h, "format the file")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if stub.promptSeen != "format the file" {
		t.Fatalf("prompt not passed to the hook: %q", stub.promptSeen)
	}
	if !requestHasText(rec.requests, 0, "the repo uses tabs") {
		t.Fatal("UserPromptSubmit context did not reach the model")
	}
}

// TestPromptSubmitBlockRejects proves a UserPromptSubmit block ends the run
// before the model is ever called.
func TestPromptSubmitBlockRejects(t *testing.T) {
	rec := &recordingProvider{inner: scripted.New(
		scripted.Response{Text: "should not run", Usage: provider.Usage{Input: 1, Output: 1}},
	)}
	h := newHarness(rec, registry(t))
	h.loop.Hooks = &stubHooks{promptSubmit: HookResult{Block: true, Message: "prompt rejected by policy"}}

	out := run(t, h, "do something forbidden")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if len(rec.requests) != 0 {
		t.Fatalf("the model ran despite a rejected prompt: %d requests", len(rec.requests))
	}
}

// TestSessionStartContextInjected proves SessionStart fires once at startup
// with the right reason and injects its context.
func TestSessionStartContextInjected(t *testing.T) {
	rec := &recordingProvider{inner: scripted.New(
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)}
	h := newHarness(rec, registry(t))
	stub := &stubHooks{sessionStart: HookResult{Context: "session began at noon"}}
	h.loop.Hooks = stub

	run(t, h, "hi")
	if len(stub.startWith) != 1 || stub.startWith[0] != "startup" {
		t.Fatalf("SessionStart reasons = %v, want one startup", stub.startWith)
	}
	if !requestHasText(rec.requests, 0, "session began at noon") {
		t.Fatal("SessionStart context did not reach the model")
	}
}

// TestSessionEndFires proves SessionEnd runs on the terminal reason.
func TestSessionEndFires(t *testing.T) {
	rec := &recordingProvider{inner: scripted.New(
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)}
	h := newHarness(rec, registry(t))
	stub := &stubHooks{}
	h.loop.Hooks = stub

	run(t, h, "hi")
	if !stub.endCalled || stub.endReason != string(TermCompleted) {
		t.Fatalf("SessionEnd not fired with the terminal reason: called=%v reason=%q", stub.endCalled, stub.endReason)
	}
}

// TestStopBlockIsBounded proves a Stop hook that always blocks cannot spin the
// loop: the loop honors at most maxStopBlocks re-drives, then stops anyway.
func TestStopBlockIsBounded(t *testing.T) {
	var responses []scripted.Response
	for i := 0; i < maxStopBlocks+3; i++ {
		responses = append(responses, scripted.Response{Text: "still nothing to do", Usage: provider.Usage{Input: 1, Output: 1}})
	}
	h := newHarness(scripted.New(responses...), registry(t))
	stub := &stubHooks{stop: HookResult{Block: true, Message: "keep going"}}
	h.loop.Hooks = stub

	out := run(t, h, "finish up")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s, want completed after the spiral guard", out.Reason)
	}
	// The guard honors maxStopBlocks blocks, then fires Stop once more, sees
	// the guard is spent, and terminates: maxStopBlocks+1 calls in total.
	if stub.stopCalls != maxStopBlocks+1 {
		t.Fatalf("Stop fired %d times, want %d", stub.stopCalls, maxStopBlocks+1)
	}
}
