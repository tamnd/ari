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
// records whether the tool ran, so a test can prove a pre-tool block stopped
// the call before it reached the tool.
type stubHooks struct {
	pre  HookResult
	post HookResult
}

func (s *stubHooks) PreTool(context.Context, string, json.RawMessage) HookResult { return s.pre }
func (s *stubHooks) PostTool(context.Context, string, json.RawMessage, string, bool) HookResult {
	return s.post
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

// TestPreToolHookBlocks proves a pre-tool block stops the call and feeds the
// message to the model, and that the tool never ran.
func TestPreToolHookBlocks(t *testing.T) {
	ran := false
	blocked := &fakeTool{name: "risky", call: func(context.Context, json.RawMessage, *tool.ToolContext, tool.ProgressFunc) (*tool.Result, error) {
		ran = true
		return &tool.Result{Model: "should not happen"}, nil
	}}
	rec := &recordingProvider{inner: hookScript("risky")}
	h := newHarness(rec, registry(t, blocked))
	h.loop.Hooks = &stubHooks{pre: HookResult{Block: true, Message: "policy says no"}}

	run(t, h, "do the risky thing")
	if ran {
		t.Fatal("the tool ran despite a pre-tool block")
	}
	got := lastResult(t, rec)
	if !got.IsErr || !strings.Contains(got.Text, "policy says no") {
		t.Fatalf("blocked result: %+v", got)
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
