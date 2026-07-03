// Package anthropic speaks the Messages API natively: fine-grained
// streaming usage, cache_control breakpoints, parallel tool use. Per D17
// this is the first-class path (doc 10 section 3).
package anthropic

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

const apiVersion = "2023-06-01"

// Provider talks to one Anthropic-shaped endpoint.
type Provider struct {
	name    string
	baseURL string
	apiKey  string
	client  *http.Client
}

// New builds the provider. name is the config key ("anthropic" usually),
// baseURL the endpoint root, apiKey the interpolated secret; the key lives
// on this struct and nowhere else (D16).
func New(name, baseURL, apiKey string) *Provider {
	return &Provider{
		name:    name,
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		client:  &http.Client{}, // streaming; no overall timeout, ctx cancels
	}
}

func (p *Provider) Name() string { return p.name }

func (p *Provider) Caps() provider.Capabilities {
	return provider.Capabilities{
		PromptCache:   true,
		UsageReport:   true,
		ServerContext: true,
		Thinking:      true,
		MaxContext:    200_000,
	}
}

// Wire shapes. The render order is fixed: tools, then system, then
// messages; the ordering is the whole caching game and the provider never
// reorders what the loop built (doc 10 section 3.1, D14).
type messagesBody struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	Stream    bool            `json:"stream"`
	Tools     []wireTool      `json:"tools,omitempty"`
	System    []wireBlock     `json:"system,omitempty"`
	Messages  []wireMessage   `json:"messages"`
	Thinking  *wireThinking   `json:"thinking,omitempty"`
	Output    *wireOutputConf `json:"output_config,omitempty"`
	Stops     []string        `json:"stop_sequences,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
	Cache       *cacheControl  `json:"cache_control,omitempty"`
}

type wireBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text,omitempty"`
	Cache   *cacheControl   `json:"cache_control,omitempty"`
	ID      string          `json:"id,omitempty"`          // tool_use
	Name    string          `json:"name,omitempty"`        // tool_use
	Input   json.RawMessage `json:"input,omitempty"`       // tool_use
	CallID  string          `json:"tool_use_id,omitempty"` // tool_result
	Content string          `json:"content,omitempty"`     // tool_result
	IsError bool            `json:"is_error,omitempty"`    // tool_result
}

type wireMessage struct {
	Role    string      `json:"role"`
	Content []wireBlock `json:"content"`
}

type wireThinking struct {
	Type string `json:"type"`
}

type wireOutputConf struct {
	Effort string `json:"effort,omitempty"`
}

func buildBody(req provider.Request) messagesBody {
	body := messagesBody{
		Model:     req.Model,
		MaxTokens: req.MaxOut,
		Stream:    true,
		Stops:     req.Stops,
	}
	if body.MaxTokens == 0 {
		body.MaxTokens = 32_000
	}
	for _, t := range req.Tools {
		wt := wireTool{Name: t.Name, Description: t.Description, InputSchema: t.Schema}
		if t.Cache {
			wt.Cache = &cacheControl{Type: "ephemeral"}
		}
		body.Tools = append(body.Tools, wt)
	}
	for _, b := range req.System {
		wb := wireBlock{Type: "text", Text: b.Text}
		if b.Cache {
			wb.Cache = &cacheControl{Type: "ephemeral"}
		}
		body.System = append(body.System, wb)
	}
	for _, m := range req.Messages {
		body.Messages = append(body.Messages, toWireMessage(m))
	}
	if req.Think == provider.ThinkAdaptive {
		body.Thinking = &wireThinking{Type: "adaptive"}
	}
	if req.Effort != "" {
		body.Output = &wireOutputConf{Effort: req.Effort}
	}
	// This provider never constructs budget_tokens: a fixed thinking budget
	// is rejected by the current models, so depth rides Effort only and a
	// card asking for a budget is a config error caught at load (doc 10
	// section 3.1).
	return body
}

func toWireMessage(m provider.Message) wireMessage {
	out := wireMessage{Role: m.Role}
	for _, b := range m.Blocks {
		switch b.Kind {
		case "tool_call":
			input := json.RawMessage(b.Call.Input)
			if len(input) == 0 {
				input = json.RawMessage(`{}`)
			}
			out.Content = append(out.Content, wireBlock{Type: "tool_use", ID: b.Call.ID, Name: b.Call.Name, Input: input})
		case "tool_result":
			out.Content = append(out.Content, wireBlock{Type: "tool_result", CallID: b.CallID, Content: b.Text, IsError: b.IsErr})
		default:
			out.Content = append(out.Content, wireBlock{Type: "text", Text: b.Text})
		}
		if b.Cache {
			out.Content[len(out.Content)-1].Cache = &cacheControl{Type: "ephemeral"}
		}
	}
	return out
}

// SSE event shapes, the subset ari decodes.
type sseEvent struct {
	Type  string `json:"type"`
	Index int    `json:"index"`

	ContentBlock *struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"content_block"`

	Delta *struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
		StopReason  string `json:"stop_reason"`
	} `json:"delta"`

	Message *struct {
		Model string    `json:"model"`
		Usage anthUsage `json:"usage"`
	} `json:"message"`

	Usage *anthUsage `json:"usage"`

	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

type anthUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
}

func toUsage(u anthUsage) provider.Usage {
	return provider.Usage{
		Input:      u.InputTokens,
		Output:     u.OutputTokens,
		CacheRead:  u.CacheReadInputTokens,
		CacheWrite: u.CacheCreationInputTokens,
	}
}

// pendingTool accumulates a streamed tool_use block until its stop.
type pendingTool struct {
	id, name string
	args     strings.Builder
}

// Stream runs one turn against POST /v1/messages.
func (p *Provider) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	start := time.Now()
	payload, err := json.Marshal(buildBody(req))
	if err != nil {
		return provider.Result{}, err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+"/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return provider.Result{}, err
	}
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("anthropic-version", apiVersion)
	if p.apiKey != "" {
		hreq.Header.Set("x-api-key", p.apiKey)
	}
	resp, err := p.client.Do(hreq)
	if err != nil {
		return provider.Result{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return provider.Result{}, fmt.Errorf("anthropic %s: %s: %s", req.Model, resp.Status, strings.TrimSpace(string(body)))
	}

	res := provider.Result{Model: req.Model}
	var usage anthUsage
	tools := map[int]*pendingTool{}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		var ev sseEvent
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			return res, fmt.Errorf("anthropic stream: bad event: %w", err)
		}
		switch ev.Type {
		case "message_start":
			if ev.Message != nil {
				usage = ev.Message.Usage
				if ev.Message.Model != "" {
					res.Model = ev.Message.Model
				}
			}
		case "content_block_start":
			if ev.ContentBlock != nil && ev.ContentBlock.Type == "tool_use" {
				tools[ev.Index] = &pendingTool{id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
			}
		case "content_block_delta":
			if ev.Delta == nil {
				continue
			}
			switch ev.Delta.Type {
			case "text_delta":
				sink.OnText(ev.Delta.Text)
			case "thinking_delta":
				sink.OnThinking(ev.Delta.Thinking)
			case "input_json_delta":
				if t := tools[ev.Index]; t != nil {
					t.args.WriteString(ev.Delta.PartialJSON)
				}
			}
		case "content_block_stop":
			if t := tools[ev.Index]; t != nil {
				args := t.args.String()
				if args == "" {
					args = "{}"
				}
				sink.OnToolCall(provider.ToolCall{ID: t.id, Name: t.name, Input: args})
				delete(tools, ev.Index)
			}
		case "message_delta":
			if ev.Delta != nil && ev.Delta.StopReason != "" {
				res.StopReason = ev.Delta.StopReason
			}
			if ev.Usage != nil {
				// The final message_delta carries the authoritative counts;
				// input-side numbers arrive on message_start.
				if ev.Usage.InputTokens > 0 {
					usage.InputTokens = ev.Usage.InputTokens
				}
				if ev.Usage.CacheReadInputTokens > 0 {
					usage.CacheReadInputTokens = ev.Usage.CacheReadInputTokens
				}
				if ev.Usage.CacheCreationInputTokens > 0 {
					usage.CacheCreationInputTokens = ev.Usage.CacheCreationInputTokens
				}
				usage.OutputTokens = ev.Usage.OutputTokens
				sink.OnUsage(toUsage(usage))
			}
		case "error":
			if ev.Error != nil {
				return res, fmt.Errorf("anthropic stream: %s: %s", ev.Error.Type, ev.Error.Message)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, err
	}
	res.Usage = toUsage(usage)
	res.Wall = time.Since(start)
	if res.StopReason == "" {
		return res, fmt.Errorf("anthropic stream: ended without a stop reason")
	}
	return res, nil
}
