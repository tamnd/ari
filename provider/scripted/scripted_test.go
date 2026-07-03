package scripted

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/provider"
)

func TestMain(m *testing.M) { eval.Main(m) }

// collector records sink calls in arrival order.
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

func TestPlaysResponsesInOrderAndSplitsText(t *testing.T) {
	p := New(
		Response{Text: "hello there", Usage: provider.Usage{Input: 10, Output: 3}},
		Response{Calls: []provider.ToolCall{{ID: "c1", Name: "read", Input: `{"path":"/a"}`}}, Usage: provider.Usage{Input: 20, Output: 5}},
	)

	var c1 collector
	res, err := p.Stream(context.Background(), provider.Request{Model: "m"}, &c1)
	if err != nil {
		t.Fatalf("first stream: %v", err)
	}
	if got := c1.text.String(); got != "hello there" {
		t.Errorf("text = %q", got)
	}
	if len(c1.order) < 3 || c1.order[0] != "text" {
		t.Errorf("first response should stream text in more than one delta, order = %v", c1.order)
	}
	if res.StopReason != "end_turn" {
		t.Errorf("stop = %q, want end_turn", res.StopReason)
	}

	var c2 collector
	res, err = p.Stream(context.Background(), provider.Request{Model: "m"}, &c2)
	if err != nil {
		t.Fatalf("second stream: %v", err)
	}
	if len(c2.calls) != 1 || c2.calls[0].Name != "read" {
		t.Fatalf("calls = %+v", c2.calls)
	}
	if res.StopReason != "tool_use" {
		t.Errorf("stop = %q, want tool_use when calls are present", res.StopReason)
	}
	if p.Calls() != 2 {
		t.Errorf("Calls() = %d, want 2", p.Calls())
	}
}

func TestRunningOutOfResponsesIsAnErrorNotAPanic(t *testing.T) {
	p := New()
	var c collector
	_, err := p.Stream(context.Background(), provider.Request{}, &c)
	if err == nil || !strings.Contains(err.Error(), "no response left") {
		t.Errorf("want a clean exhaustion error, got: %v", err)
	}
}

func TestFromRawDecodesAReplaySet(t *testing.T) {
	raw := []json.RawMessage{
		[]byte(`{"text":"hi","usage":{"Input":5,"Output":2},"stop":"end_turn"}`),
	}
	p, err := FromRaw(raw)
	if err != nil {
		t.Fatalf("FromRaw: %v", err)
	}
	var c collector
	res, err := p.Stream(context.Background(), provider.Request{}, &c)
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	if c.text.String() != "hi" || res.Usage.Input != 5 {
		t.Errorf("text %q usage %+v", c.text.String(), res.Usage)
	}
}

func TestCanceledContextStopsBeforePlaying(t *testing.T) {
	p := New(Response{Text: "never"})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var c collector
	if _, err := p.Stream(ctx, provider.Request{}, &c); err == nil {
		t.Error("a canceled context should refuse to stream")
	}
	if p.Calls() != 0 {
		t.Errorf("a canceled stream must not consume a response, Calls() = %d", p.Calls())
	}
}
