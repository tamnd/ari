// Package scripted is the provider every loop and UI test drives the core
// with: recorded responses, no network. It satisfies the same interface a
// real provider does, so a test and a live run differ only in which
// provider is wired (D23, plan/01 slice 3).
package scripted

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/tamnd/ari/provider"
)

// Response is one scripted model reply, the shape stored in a replay
// set's responses array (kernel/eval Script). A non-nil Fail makes the
// call error with that classification instead of streaming, so loop
// tests script retries, fallbacks, and circuit breakers (doc 03).
type Response struct {
	Text     string              `json:"text,omitempty"`
	Thinking string              `json:"thinking,omitempty"`
	Calls    []provider.ToolCall `json:"calls,omitempty"`
	Usage    provider.Usage      `json:"usage"`
	Stop     string              `json:"stop"`
	Fail     *provider.Error     `json:"fail,omitempty"`
}

// Provider plays responses in order, one per Stream call.
type Provider struct {
	mu        sync.Mutex
	responses []Response
	next      int
	caps      provider.Capabilities
}

// New builds a scripted provider from decoded responses.
func New(responses ...Response) *Provider {
	return &Provider{
		responses: responses,
		caps: provider.Capabilities{
			PromptCache: true,
			UsageReport: true,
			Thinking:    true,
		},
	}
}

// FromRaw decodes a replay set's opaque responses ([]json.RawMessage).
func FromRaw(raw []json.RawMessage) (*Provider, error) {
	rs := make([]Response, len(raw))
	for i, r := range raw {
		if err := json.Unmarshal(r, &rs[i]); err != nil {
			return nil, fmt.Errorf("scripted response %d: %w", i, err)
		}
	}
	return New(rs...), nil
}

func (p *Provider) Name() string { return "scripted" }

func (p *Provider) Caps() provider.Capabilities { return p.caps }

// Calls reports how many turns have been played.
func (p *Provider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.next
}

// Stream plays the next scripted response: thinking, then text, then tool
// calls, then usage, the order a real stream produces them.
func (p *Provider) Stream(ctx context.Context, req provider.Request, sink provider.EventSink) (provider.Result, error) {
	if err := ctx.Err(); err != nil {
		return provider.Result{}, err
	}
	p.mu.Lock()
	if p.next >= len(p.responses) {
		p.mu.Unlock()
		return provider.Result{}, fmt.Errorf("scripted: no response left for call %d", p.next+1)
	}
	r := p.responses[p.next]
	p.next++
	p.mu.Unlock()

	if r.Fail != nil {
		return provider.Result{}, r.Fail
	}

	if r.Thinking != "" {
		sink.OnThinking(r.Thinking)
	}
	if r.Text != "" {
		// Two deltas, not one, so consumers prove they concatenate.
		half := len(r.Text) / 2
		sink.OnText(r.Text[:half])
		sink.OnText(r.Text[half:])
	}
	for _, c := range r.Calls {
		sink.OnToolCall(c)
	}
	sink.OnUsage(r.Usage)

	stop := r.Stop
	if stop == "" {
		stop = "end_turn"
		if len(r.Calls) > 0 {
			stop = "tool_use"
		}
	}
	return provider.Result{
		Usage:      r.Usage,
		StopReason: stop,
		Model:      req.Model,
		Wall:       time.Millisecond, // deterministic, nonzero
	}, nil
}
