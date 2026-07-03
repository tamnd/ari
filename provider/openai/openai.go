// Package openai speaks chat completions, the shape every non-Anthropic
// endpoint understands: hosted gateways, Ollama, LM Studio, llama.cpp, the
// tailnet box. Written to degrade gracefully rather than assume features
// (doc 10 section 4).
package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tamnd/ari/provider"
)

// Provider talks to one OpenAI-compatible endpoint.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
	caps    provider.Capabilities
	client  *http.Client
}

// New builds the provider. Local endpoints pass an empty key.
func New(name, baseURL, apiKey string) *Provider {
	return &Provider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		caps: provider.Capabilities{
			// No cache_control in this dialect; when a gateway reports
			// cached tokens through the usage extension we read them, but
			// the loop must not plan around caching here.
			PromptCache: false,
			UsageReport: true, // requested via stream_options; estimated when absent
		},
		client: &http.Client{},
	}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Caps() provider.Capabilities { return p.caps }

type chatBody struct {
	Model         string        `json:"model"`
	Stream        bool          `json:"stream"`
	Messages      []chatMessage `json:"messages"`
	Tools         []chatTool    `json:"tools,omitempty"`
	MaxTokens     int           `json:"max_tokens,omitempty"`
	Stop          []string      `json:"stop,omitempty"`
	StreamOptions *streamOpts   `json:"stream_options,omitempty"`
	Effort        string        `json:"reasoning_effort,omitempty"`
}

type streamOpts struct {
	IncludeUsage bool `json:"include_usage"`
}

type chatMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []chatToolCall `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
}

type chatTool struct {
	Type     string       `json:"type"`
	Function chatFunction `json:"function"`
}

type chatFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type chatToolCall struct {
	Index    *int   `json:"index,omitempty"`
	ID       string `json:"id,omitempty"`
	Type     string `json:"type,omitempty"`
	Function struct {
		Name      string `json:"name,omitempty"`
		Arguments string `json:"arguments,omitempty"`
	} `json:"function"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content          string         `json:"content"`
			ReasoningContent string         `json:"reasoning_content"`
			ToolCalls        []chatToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage *chatUsage `json:"usage"`
}

type chatUsage struct {
	PromptTokens        int `json:"prompt_tokens"`
	CompletionTokens    int `json:"completion_tokens"`
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

func joinSystem(blocks []provider.Block) string {
	var b strings.Builder
	for i, blk := range blocks {
		if i > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(blk.Text)
	}
	return b.String()
}

func buildBody(req provider.Request) chatBody {
	msgs := make([]chatMessage, 0, len(req.Messages)+1)
	if sys := joinSystem(req.System); sys != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: sys})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, toChatMessages(m)...)
	}
	body := chatBody{
		Model:         req.Model,
		Stream:        true,
		Messages:      msgs,
		MaxTokens:     req.MaxOut,
		Stop:          req.Stops,
		StreamOptions: &streamOpts{IncludeUsage: true},
	}
	for _, t := range req.Tools {
		body.Tools = append(body.Tools, chatTool{Type: "function", Function: chatFunction{Name: t.Name, Description: t.Description, Parameters: t.Schema}})
	}
	// Cache breakpoints in req.System and req.Tools drop here by design:
	// the dialect has no cache_control. A stable prefix still helps when
	// the endpoint caches server-side; nothing breaks when it does not
	// (doc 10 section 4.1).
	return body
}

// toChatMessages flattens one dialect-independent message. A tool_result
// block becomes its own role=tool message, which is how this dialect
// carries results.
func toChatMessages(m provider.Message) []chatMessage {
	var out []chatMessage
	cur := chatMessage{Role: m.Role}
	flush := func() {
		if cur.Content != "" || len(cur.ToolCalls) > 0 {
			out = append(out, cur)
			cur = chatMessage{Role: m.Role}
		}
	}
	for _, b := range m.Blocks {
		switch b.Kind {
		case "tool_call":
			tc := chatToolCall{ID: b.Call.ID, Type: "function"}
			tc.Function.Name = b.Call.Name
			tc.Function.Arguments = b.Call.Input
			cur.ToolCalls = append(cur.ToolCalls, tc)
		case "tool_result":
			flush()
			out = append(out, chatMessage{Role: "tool", Content: b.Text, ToolCallID: b.CallID})
		default:
			cur.Content += b.Text
		}
	}
	flush()
	return out
}

// pending reassembles tool calls streamed as indexed argument fragments.
type pending struct {
	id, name string
	args     strings.Builder
}

// Stream runs one turn against POST /v1/chat/completions.
func (p *Provider) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	start := time.Now()
	payload, err := json.Marshal(buildBody(req))
	if err != nil {
		return provider.Result{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return provider.Result{}, err
	}
	hreq.Header.Set("content-type", "application/json")
	if p.apiKey != "" {
		hreq.Header.Set("authorization", "Bearer "+p.apiKey)
	}
	resp, err := p.client.Do(hreq)
	if err != nil {
		return provider.Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return provider.Result{}, fmt.Errorf("%s %s: %s: %s", p.name, req.Model, resp.Status, strings.TrimSpace(string(body)))
	}

	res := provider.Result{Model: req.Model}
	var gotUsage bool
	var outText strings.Builder
	calls := map[int]*pending{}
	flushCalls := func() {
		for i := 0; ; i++ {
			c, ok := calls[i]
			if !ok {
				break
			}
			args := c.args.String()
			if args == "" {
				args = "{}"
			}
			sink.OnToolCall(provider.ToolCall{ID: c.id, Name: c.name, Input: args})
			delete(calls, i)
		}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" || data == "[DONE]" {
			continue
		}
		var chunk chatChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return res, fmt.Errorf("%s stream: bad chunk: %w", p.name, err)
		}
		if chunk.Usage != nil {
			u := provider.Usage{Input: chunk.Usage.PromptTokens, Output: chunk.Usage.CompletionTokens}
			if d := chunk.Usage.PromptTokensDetails; d != nil {
				u.CacheRead = d.CachedTokens
				u.Input -= d.CachedTokens // prompt_tokens includes cached here
			}
			res.Usage = u
			gotUsage = true
			sink.OnUsage(u)
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				outText.WriteString(ch.Delta.Content)
				sink.OnText(ch.Delta.Content)
			}
			if ch.Delta.ReasoningContent != "" {
				sink.OnThinking(ch.Delta.ReasoningContent)
			}
			for _, tc := range ch.Delta.ToolCalls {
				idx := 0
				if tc.Index != nil {
					idx = *tc.Index
				}
				c := calls[idx]
				if c == nil {
					c = &pending{}
					calls[idx] = c
				}
				if tc.ID != "" {
					c.id = tc.ID
				}
				if tc.Function.Name != "" {
					c.name = tc.Function.Name
				}
				c.args.WriteString(tc.Function.Arguments)
			}
			if ch.FinishReason != "" {
				flushCalls()
				res.StopReason = mapFinish(ch.FinishReason)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	if !gotUsage {
		// The endpoint reported nothing; estimate so the ledger has a
		// number, flagged honest about its provenance (doc 10 section 4.3).
		res.Usage = provider.Usage{
			Input:     estimateRequest(req),
			Output:    estimateText(outText.String()),
			Estimated: true,
		}
		sink.OnUsage(res.Usage)
	}
	res.Wall = time.Since(start)
	if res.StopReason == "" {
		return res, fmt.Errorf("%s stream: ended without a finish reason", p.name)
	}
	return res, nil
}

func mapFinish(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "tool_calls":
		return "tool_use"
	case "length":
		return "max_tokens"
	default:
		return reason
	}
}

// estimateText is the fallback tokenizer: about four bytes per token for
// code-heavy English. Rough on purpose; the row is flagged estimated.
func estimateText(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + 3) / 4
}

func estimateRequest(req provider.Request) int {
	n := 0
	for _, b := range req.System {
		n += estimateText(b.Text)
	}
	for _, t := range req.Tools {
		n += estimateText(t.Name + t.Description)
		if raw, err := json.Marshal(t.Schema); err == nil {
			n += estimateText(string(raw))
		}
	}
	for _, m := range req.Messages {
		for _, b := range m.Blocks {
			n += estimateText(b.Text)
			if b.Call != nil {
				n += estimateText(b.Call.Name + b.Call.Input)
			}
		}
	}
	return n
}
