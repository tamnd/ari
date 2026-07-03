package openai

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/provider"
)

func TestMain(m *testing.M) { eval.Main(m) }

type collector struct {
	order []string
	text  strings.Builder
	calls []provider.ToolCall
	usage provider.Usage
}

func (c *collector) OnText(d string)     { c.order = append(c.order, "text"); c.text.WriteString(d) }
func (c *collector) OnThinking(d string) { c.order = append(c.order, "thinking") }
func (c *collector) OnToolCall(t provider.ToolCall) {
	c.order = append(c.order, "call")
	c.calls = append(c.calls, t)
}
func (c *collector) OnUsage(u provider.Usage) { c.order = append(c.order, "usage"); c.usage = u }

// scriptedChunks mirrors the anthropic test's recorded turn: the same
// text, the same tool call, arriving in this dialect's shapes.
const scriptedChunks = `data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hel"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"lo"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"path\":"}}]}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"/a\"}"}}]}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":112,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]

`

func serve(t *testing.T, stream string, body *string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		if body != nil {
			*body = string(raw)
		}
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
}

func request() provider.Request {
	return provider.Request{
		Model:  "qwen3-coder:30b",
		System: []provider.Block{{Text: "be brief", Cache: true}},
		Tools: []provider.ToolDef{{
			Name: "read", Description: "read a file",
			Schema: map[string]any{"type": "object"},
			Cache:  true,
		}},
		Messages: []provider.Message{{Role: "user", Blocks: []provider.MsgBlock{{Kind: "text", Text: "hi"}}}},
		MaxOut:   64,
	}
}

func TestStreamProducesTheSameShapesAsTheOtherDialect(t *testing.T) {
	var body string
	srv := serve(t, scriptedChunks, &body)
	defer srv.Close()

	p := New("ollama", srv.URL+"/v1", "")
	var c collector
	res, err := p.Stream(context.Background(), request(), &c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if got := c.text.String(); got != "Hello" {
		t.Errorf("text = %q, want Hello", got)
	}
	if len(c.calls) != 1 || c.calls[0].ID != "call_1" || c.calls[0].Name != "read" || c.calls[0].Input != `{"path":"/a"}` {
		t.Fatalf("calls = %+v", c.calls)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("stop = %q, want tool_use (mapped from tool_calls)", res.StopReason)
	}
	// prompt_tokens includes the cached part in this dialect; Input is the
	// uncached remainder after the split (doc 10 section 4.3).
	u := provider.Usage{Input: 12, Output: 9, CacheRead: 100}
	if res.Usage != u {
		t.Errorf("usage = %+v, want %+v", res.Usage, u)
	}
}

func TestWireBodyJoinsSystemAndDropsCacheControl(t *testing.T) {
	var body string
	srv := serve(t, scriptedChunks, &body)
	defer srv.Close()

	p := New("ollama", srv.URL+"/v1", "")
	var c collector
	if _, err := p.Stream(context.Background(), request(), &c); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !strings.Contains(body, `"role":"system"`) {
		t.Error("system message missing")
	}
	if strings.Contains(body, "cache_control") {
		t.Error("cache_control has no meaning in this dialect and must not reach the wire")
	}
	if !strings.Contains(body, `"stream_options":{"include_usage":true}`) {
		t.Error("usage must be requested via stream_options")
	}
	if !strings.Contains(body, `"type":"function"`) {
		t.Error("tools missing")
	}
}

func TestMissingUsageIsEstimatedAndFlagged(t *testing.T) {
	stream := `data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"four par"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"ts of text"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"stop"}]}

data: [DONE]

`
	srv := serve(t, stream, nil)
	defer srv.Close()

	p := New("lmstudio", srv.URL+"/v1", "")
	var c collector
	res, err := p.Stream(context.Background(), request(), &c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if !res.Usage.Estimated {
		t.Error("usage must be flagged Estimated when the endpoint reports nothing")
	}
	if res.Usage.Input <= 0 || res.Usage.Output <= 0 {
		t.Errorf("estimates must be nonzero for nonempty text, got %+v", res.Usage)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("stop = %q, want end_turn (mapped from stop)", res.StopReason)
	}
	if c.usage != res.Usage {
		t.Errorf("the estimated usage must reach the sink too, sink %+v result %+v", c.usage, res.Usage)
	}
}

func TestToolResultBecomesARoleToolMessage(t *testing.T) {
	msgs := toChatMessages(provider.Message{
		Role: "assistant",
		Blocks: []provider.MsgBlock{
			{Kind: "text", Text: "running it"},
			{Kind: "tool_call", Call: &provider.ToolCall{ID: "c1", Name: "sh", Input: `{"cmd":"ls"}`}},
			{Kind: "tool_result", CallID: "c1", Text: "file.txt"},
		},
	})
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2 (assistant, then tool)", len(msgs))
	}
	if msgs[0].Role != "assistant" || len(msgs[0].ToolCalls) != 1 {
		t.Errorf("first message = %+v", msgs[0])
	}
	if msgs[1].Role != "tool" || msgs[1].ToolCallID != "c1" || msgs[1].Content != "file.txt" {
		t.Errorf("second message = %+v", msgs[1])
	}
}
