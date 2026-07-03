package provider

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

type fakeProvider struct{ name string }

func (f fakeProvider) Name() string       { return f.name }
func (f fakeProvider) Caps() Capabilities { return Capabilities{} }
func (f fakeProvider) Stream(ctx context.Context, req Request, sink EventSink) (Result, error) {
	return Result{}, nil
}

func TestResolveReturnsTheChainInOrder(t *testing.T) {
	r := NewRegistry()
	r.AddProvider(fakeProvider{name: "anthropic"})
	r.AddProvider(fakeProvider{name: "ollama"})
	err := r.AddTier("frontier", []ChainLink{
		{Provider: "anthropic", Model: "claude-opus-4-8", TTL: "5m"},
		{Provider: "ollama", Model: "qwen3-coder:30b"},
	})
	if err != nil {
		t.Fatalf("AddTier: %v", err)
	}

	chain, err := r.Resolve("frontier")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(chain) != 2 {
		t.Fatalf("chain length = %d, want 2", len(chain))
	}
	if chain[0].Provider.Name() != "anthropic" || chain[0].Model != "claude-opus-4-8" || chain[0].TTL != "5m" {
		t.Errorf("head = %s %s %s", chain[0].Provider.Name(), chain[0].Model, chain[0].TTL)
	}
	if chain[1].Provider.Name() != "ollama" {
		t.Errorf("fallback = %s, want ollama", chain[1].Provider.Name())
	}
}

func TestUnknownTierIsACleanErrorNamingWhatExists(t *testing.T) {
	r := NewRegistry()
	r.AddProvider(fakeProvider{name: "anthropic"})
	if err := r.AddTier("mid", []ChainLink{{Provider: "anthropic", Model: "claude-sonnet-5"}}); err != nil {
		t.Fatalf("AddTier: %v", err)
	}
	_, err := r.Resolve("frontier")
	if err == nil {
		t.Fatal("Resolve(frontier) should fail, no such tier")
	}
	if !strings.Contains(err.Error(), `"frontier"`) || !strings.Contains(err.Error(), "mid") {
		t.Errorf("error should name the missing tier and list the known ones, got: %v", err)
	}
}

func TestAddTierRefusesAnUnregisteredProvider(t *testing.T) {
	r := NewRegistry()
	err := r.AddTier("cheap", []ChainLink{{Provider: "nope", Model: "m"}})
	if err == nil || !strings.Contains(err.Error(), `"nope"`) {
		t.Errorf("want an error naming the missing provider, got: %v", err)
	}
}

func TestAddTierRefusesAnEmptyModel(t *testing.T) {
	r := NewRegistry()
	r.AddProvider(fakeProvider{name: "a"})
	err := r.AddTier("cheap", []ChainLink{{Provider: "a"}})
	if err == nil || !strings.Contains(err.Error(), "empty model") {
		t.Errorf("want an empty-model error, got: %v", err)
	}
}

func TestResolveRefusesAnEmptyChain(t *testing.T) {
	r := NewRegistry()
	if err := r.AddTier("local", nil); err != nil {
		t.Fatalf("AddTier: %v", err)
	}
	if _, err := r.Resolve("local"); err == nil {
		t.Error("an empty chain should not resolve")
	}
}
