// Package provider is the only kernel component that opens a socket to a
// model. A provider knows how to stream one turn and how to describe its
// own capabilities, nothing else (doc 10 section 2). Two dialects satisfy
// it: provider/anthropic native and provider/openai for everything else,
// plus provider/scripted for tests (D23).
package provider

import (
	"context"
	"time"
)

// Provider streams one model turn and reports what it cost.
type Provider interface {
	// Name is a stable identifier used in the ledger and in config, for
	// example "anthropic" or "ollama".
	Name() string

	// Stream runs one turn. It writes decode events to sink as they arrive
	// and returns a Result once the turn ends. A non-nil error means the
	// turn did not complete; the caller consults the error for retry.
	Stream(ctx context.Context, req Request, sink EventSink) (Result, error)

	// Caps describes what this provider can do so the loop can degrade
	// gracefully.
	Caps() Capabilities
}

// EventSink receives decode events in order. The agent loop implements it
// to render tokens, collect tool calls, and drive the sidebar. It must not
// block; slow consumers buffer.
type EventSink interface {
	OnText(delta string)
	OnThinking(delta string)
	OnToolCall(call ToolCall)
	OnUsage(u Usage) // may fire more than once; last value wins
}

// ThinkMode selects the reasoning channel.
type ThinkMode string

const (
	ThinkUnset    ThinkMode = ""
	ThinkOff      ThinkMode = "off"
	ThinkAdaptive ThinkMode = "adaptive"
)

// Request is one turn's worth of input, dialect-independent. The loop
// builds it in a cache-stable order; the provider serializes without
// reordering anything (doc 10 section 3.1).
type Request struct {
	Model    string      // resolved provider model id
	System   []Block     // system blocks, may carry cache breakpoints
	Tools    []ToolDef   // tool definitions, may carry a cache breakpoint
	Messages []Message   // conversation turns
	MaxOut   int         // output token ceiling
	Effort   string      // "low" | "medium" | "high" | "xhigh" | "max" | ""
	Think    ThinkMode   // adaptive, off, or unset
	Stops    []string    // stop sequences, usually empty
	Meta     RequestMeta // ant, task, session ids for the ledger
}

// Block is one piece of prompt content. Cache marks a cache breakpoint
// after this block; the Anthropic path renders it as cache_control and the
// OpenAI path ignores it (doc 10 section 5, D14).
type Block struct {
	Text  string
	Cache bool
}

// ToolDef describes one tool to the model.
type ToolDef struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema for the input
	Cache       bool           // breakpoint after this tool
}

// Message is one conversation turn. Role is "user", "assistant", or
// "system" (mid-conversation operator context, doc 10 section 3.5).
type Message struct {
	Role   string
	Blocks []MsgBlock
}

// MsgBlock is one content piece inside a message.
type MsgBlock struct {
	Kind   string    // "text" | "tool_call" | "tool_result"
	Text   string    // for text and tool_result content
	Call   *ToolCall // for tool_call
	CallID string    // for tool_result: which call this answers
	IsErr  bool      // for tool_result: the tool failed
	Cache  bool      // breakpoint after this block (D14); OpenAI ignores it
}

// ToolCall is one tool invocation the model asked for.
type ToolCall struct {
	ID    string
	Name  string
	Input string // raw JSON arguments
}

// RequestMeta rides along so the ledger can attribute the cost. None of
// it is sent to the model.
type RequestMeta struct {
	Ant     string
	Task    string
	Session string
	Tier    string // requested tier: "frontier" | "mid" | "cheap" | "local"
}

// Usage is the token accounting for one turn. Four counts kept separate:
// cache reads cost ~0.1x base input and writes ~1.25x, and collapsing them
// loses the economic story. Input is the uncached remainder only; the full
// prompt is Input + CacheRead + CacheWrite (doc 10 section 3.2).
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Estimated  bool // counts were estimated, not reported by the endpoint
}

// Result is what Stream returns when a turn ends cleanly.
type Result struct {
	Usage      Usage
	StopReason string        // "end_turn" | "tool_use" | "max_tokens" | "refusal" | ...
	Model      string        // the model that actually answered
	Wall       time.Duration // wall-clock time for the turn
}

// Capabilities is a provider's honest self-description. The loop reads it
// to degrade gracefully: no caching means no D14 breakpoints, no usage
// means the ledger marks rows estimated.
type Capabilities struct {
	PromptCache   bool
	UsageReport   bool
	ServerContext bool
	Thinking      bool
	Embeddings    bool
	MaxContext    int // context window in tokens, 0 if unknown
}
