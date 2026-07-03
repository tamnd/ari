package anthropic

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
	think strings.Builder
	calls []provider.ToolCall
	usage provider.Usage
}

func (c *collector) OnText(d string) { c.order = append(c.order, "text"); c.text.WriteString(d) }
func (c *collector) OnThinking(d string) {
	c.order = append(c.order, "thinking")
	c.think.WriteString(d)
}
func (c *collector) OnToolCall(t provider.ToolCall) {
	c.order = append(c.order, "call")
	c.calls = append(c.calls, t)
}
func (c *collector) OnUsage(u provider.Usage) { c.order = append(c.order, "usage"); c.usage = u }

// scriptedSSE is the recorded stream: text in two deltas, then one tool
// call assembled from json fragments, then the final usage.
const scriptedSSE = `event: message_start
data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":12,"output_tokens":1,"cache_read_input_tokens":0,"cache_creation_input_tokens":100}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"/a\"}"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_delta","delta":{"type":"message_delta","stop_reason":"tool_use"},"usage":{"output_tokens":9}}

data: {"type":"message_stop"}

`

// serve returns a test server that captures the request and replies with
// the scripted stream.
func serve(t *testing.T, body *string, headers *http.Header) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path = %s, want /v1/messages", r.URL.Path)
		}
		raw, _ := io.ReadAll(r.Body)
		*body = string(raw)
		*headers = r.Header.Clone()
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprint(w, scriptedSSE)
	}))
}

func request() provider.Request {
	return provider.Request{
		Model:  "claude-sonnet-5",
		System: []provider.Block{{Text: "be brief", Cache: true}},
		Tools: []provider.ToolDef{{
			Name: "read", Description: "read a file",
			Schema: map[string]any{"type": "object"},
			Cache:  true,
		}},
		Messages: []provider.Message{{Role: "user", Blocks: []provider.MsgBlock{{Kind: "text", Text: "hi"}}}},
		MaxOut:   64,
		Effort:   "high",
		Think:    provider.ThinkAdaptive,
	}
}

func TestStreamDecodesTextToolAndUsageInOrder(t *testing.T) {
	var body string
	var headers http.Header
	srv := serve(t, &body, &headers)
	defer srv.Close()

	p := New("anthropic", srv.URL, "sk-test")
	var c collector
	res, err := p.Stream(context.Background(), request(), &c)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	if got := c.text.String(); got != "Hello" {
		t.Errorf("text = %q, want Hello", got)
	}
	if len(c.calls) != 1 || c.calls[0].ID != "tu_1" || c.calls[0].Name != "read" || c.calls[0].Input != `{"path":"/a"}` {
		t.Fatalf("calls = %+v", c.calls)
	}
	// Text before the call, usage last: the order the loop renders in.
	want := []string{"text", "text", "call", "usage"}
	if fmt.Sprint(c.order) != fmt.Sprint(want) {
		t.Errorf("sink order = %v, want %v", c.order, want)
	}

	u := provider.Usage{Input: 12, Output: 9, CacheWrite: 100}
	if res.Usage != u {
		t.Errorf("usage = %+v, want %+v", res.Usage, u)
	}
	if c.usage != u {
		t.Errorf("sink usage = %+v, want %+v", c.usage, u)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("stop = %q, want tool_use", res.StopReason)
	}
	if res.Model != "claude-sonnet-5" {
		t.Errorf("model = %q", res.Model)
	}
}

func TestWireBodyKeepsCacheOrderAndNeverBuildsBudgetTokens(t *testing.T) {
	var body string
	var headers http.Header
	srv := serve(t, &body, &headers)
	defer srv.Close()

	p := New("anthropic", srv.URL, "sk-test")
	var c collector
	if _, err := p.Stream(context.Background(), request(), &c); err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// Render order is tools, then system, then messages; that ordering is
	// the caching game (doc 10 section 3.1).
	ti, si, mi := strings.Index(body, `"tools"`), strings.Index(body, `"system"`), strings.Index(body, `"messages"`)
	if ti < 0 || si < 0 || mi < 0 || ti > si || si > mi {
		t.Errorf("wire order tools=%d system=%d messages=%d, want tools < system < messages", ti, si, mi)
	}
	if !strings.Contains(body, `"cache_control":{"type":"ephemeral"}`) {
		t.Error("cache breakpoints did not reach the wire")
	}
	if strings.Contains(body, "budget_tokens") {
		t.Error("budget_tokens must never be constructed (doc 10 section 3.1)")
	}
	if !strings.Contains(body, `"thinking":{"type":"adaptive"}`) {
		t.Error("adaptive thinking missing")
	}
	if !strings.Contains(body, `"output_config":{"effort":"high"}`) {
		t.Error("effort missing from output_config")
	}
	if headers.Get("x-api-key") != "sk-test" {
		t.Error("x-api-key header missing")
	}
	if headers.Get("anthropic-version") != apiVersion {
		t.Errorf("anthropic-version = %q", headers.Get("anthropic-version"))
	}
}

func TestServerErrorSurfacesStatusAndBodyNeverTheKey(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"type":"overloaded_error","message":"busy"}}`, http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	p := New("anthropic", srv.URL, "sk-secret-value")
	var c collector
	_, err := p.Stream(context.Background(), request(), &c)
	if err == nil {
		t.Fatal("want an error on 503")
	}
	if !strings.Contains(err.Error(), "503") || !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("error should carry status and body, got: %v", err)
	}
	if strings.Contains(err.Error(), "sk-secret-value") {
		t.Error("the api key leaked into an error (D16)")
	}
}

func TestStreamErrorEventFailsTheTurn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, "data: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"try later\"}}\n\n")
	}))
	defer srv.Close()

	p := New("anthropic", srv.URL, "k")
	var c collector
	_, err := p.Stream(context.Background(), request(), &c)
	if err == nil || !strings.Contains(err.Error(), "overloaded_error") {
		t.Errorf("want the stream error surfaced, got: %v", err)
	}
}
