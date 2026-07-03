package core

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/anthropic"
	"github.com/tamnd/ari/provider/openai"
)

func TestBuildRegistryResolvesTheDefaultTiers(t *testing.T) {
	c := openColony(t)
	reg := c.Registry()

	chain, err := reg.Resolve("frontier")
	if err != nil {
		t.Fatalf("Resolve(frontier): %v", err)
	}
	if chain[0].Provider.Name() != "anthropic" || chain[0].Model != "claude-opus-4-8" {
		t.Errorf("frontier head = %s %s", chain[0].Provider.Name(), chain[0].Model)
	}
	// The cheap tier exists from the first release even though M0 has no
	// consolidator, so the config shape M2 needs is already correct (D17).
	if _, err := reg.Resolve("cheap"); err != nil {
		t.Errorf("Resolve(cheap): %v", err)
	}
	if _, err := reg.Resolve("imaginary"); err == nil {
		t.Error("an unknown tier must be a clean error")
	}
}

// The two scripted responses below record the same model turn, one in each
// dialect: the text "Hello" in two deltas, then one read tool call.
const anthSSE = `data: {"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":12,"output_tokens":1,"cache_creation_input_tokens":100}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hel"}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"lo"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"tu_1","name":"read"}}

data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":\"/a\"}"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"message_delta","delta":{"type":"message_delta","stop_reason":"tool_use"},"usage":{"output_tokens":9}}

`

const oaiChunks = `data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"Hel"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"content":"lo"}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{"tool_calls":[{"index":0,"id":"tu_1","type":"function","function":{"name":"read","arguments":"{\"path\":\"/a\"}"}}]}}]}

data: {"object":"chat.completion.chunk","choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: {"object":"chat.completion.chunk","choices":[],"usage":{"prompt_tokens":112,"completion_tokens":9,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]

`

func scriptedServer(t *testing.T, stream string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("content-type", "text/event-stream")
		_, _ = fmt.Fprint(w, stream)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// emitSink is the smallest loop-shaped bridge: provider decode events in,
// part events out through the turn handle. The real loop replaces it in a
// later slice; the event shapes it emits are the contract.
type emitSink struct {
	h        *TurnHandle
	part     int
	textOpen bool
}

func (s *emitSink) OnText(d string) {
	s.textOpen = true
	_ = s.h.Emit(event.TypeTextDelta, event.TextDelta{Part: s.part, Text: d})
}

func (s *emitSink) OnThinking(string) {}

func (s *emitSink) OnToolCall(c provider.ToolCall) {
	s.closeText()
	_ = s.h.Emit(event.TypeToolStart, event.ToolStart{Part: s.part, Call: c.ID, Tool: c.Name, Input: c.Input})
	_ = s.h.Emit(event.TypeToolEnd, event.ToolEnd{Part: s.part, Call: c.ID, Tool: c.Name, OK: true, Display: "scripted"})
	s.part++
}

func (s *emitSink) OnUsage(provider.Usage) {}

func (s *emitSink) closeText() {
	if s.textOpen {
		_ = s.h.Emit(event.TypeTextEnd, event.TextEnd{Part: s.part})
		s.part++
		s.textOpen = false
	}
}

// tierRunner resolves a tier, streams the turn, and records the row, which
// is the loop slice's skeleton in twenty lines.
func tierRunner(tier string) runnerFunc {
	return func(ctx context.Context, h *TurnHandle) error {
		chain, err := h.colony.registry.Resolve(tier)
		if err != nil {
			return Wrap(ErrProvider, err, "resolving the tier")
		}
		target := chain[0]
		sink := &emitSink{h: h}
		res, err := target.Provider.Stream(ctx, provider.Request{
			Model:    target.Model,
			Messages: []provider.Message{{Role: "user", Blocks: []provider.MsgBlock{{Kind: "text", Text: h.Request.Text}}}},
			MaxOut:   64,
		}, sink)
		if err != nil {
			return Wrap(ErrProvider, err, "streaming the turn")
		}
		sink.closeText()
		h.colony.ledger.Record(ledger.Row{
			Ant: workerAnt, Task: "t", Session: string(h.Session), Turn: string(h.Turn),
			Provider: target.Provider.Name(), Model: res.Model, Tier: tier,
			Usage: res.Usage, Wall: res.Wall, Estimated: res.Usage.Estimated,
			StopReason: res.StopReason,
		})
		return nil
	}
}

// runScriptedTurn drives one full turn against a registry and returns the
// event type sequence from turn.started to turn.finished.
func runScriptedTurn(t *testing.T, reg *provider.Registry) ([]event.Type, *Colony) {
	t.Helper()
	c := openColony(t, WithRegistry(reg), WithRunner(tierRunner("frontier")))
	if err := c.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	sub, err := c.Events(context.Background(), EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()
	sid, err := c.NewSession(context.Background(), NewSessionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Submit(context.Background(), SubmitRequest{Session: sid, Text: "hi"}); err != nil {
		t.Fatal(err)
	}
	events := collect(t, sub, event.TypeTurnFinished)
	var types []event.Type
	seen := false
	for _, e := range events {
		if e.Type == event.TypeTurnStarted {
			seen = true
		}
		if seen {
			types = append(types, e.Type)
		}
	}
	return types, c
}

// TestTurnEventsAreDialectBlind is the slice DoD: a scripted Anthropic
// response and a scripted OpenAI-compatible response produce the same
// event shapes, so nothing above the provider learns which dialect
// answered (plan/01 slice 3, D17).
func TestTurnEventsAreDialectBlind(t *testing.T) {
	anth := provider.NewRegistry()
	anth.AddProvider(anthropic.New("anthropic", scriptedServer(t, anthSSE).URL, "sk-test"))
	if err := anth.AddTier("frontier", []provider.ChainLink{{Provider: "anthropic", Model: "claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}

	oai := provider.NewRegistry()
	oai.AddProvider(openai.New("ollama", scriptedServer(t, oaiChunks).URL+"/v1", ""))
	if err := oai.AddTier("frontier", []provider.ChainLink{{Provider: "ollama", Model: "claude-sonnet-5"}}); err != nil {
		t.Fatal(err)
	}

	fromAnth, colonyA := runScriptedTurn(t, anth)
	fromOAI, _ := runScriptedTurn(t, oai)

	want := []event.Type{
		event.TypeTurnStarted,
		event.TypeTextDelta, event.TypeTextDelta, event.TypeTextEnd,
		event.TypeToolStart, event.TypeToolEnd,
		event.TypeLedgerTurn,
		event.TypeTurnFinished,
	}
	if !reflect.DeepEqual(fromAnth, want) {
		t.Errorf("anthropic sequence = %v, want %v", fromAnth, want)
	}
	if !reflect.DeepEqual(fromAnth, fromOAI) {
		t.Errorf("dialects diverged:\n  anthropic: %v\n  openai:    %v", fromAnth, fromOAI)
	}

	// The meter ran: the turn is in the roll-up with a real cost.
	all := colonyA.Ledger().All()
	if all.Requests != 1 || all.CostUSD <= 0 {
		t.Errorf("ledger totals = %+v, want one priced request", all)
	}
	if all.CacheWrite != 100 {
		t.Errorf("cache_write = %d, want the scripted 100", all.CacheWrite)
	}
}
