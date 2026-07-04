package mcp

import (
	"encoding/json"
	"testing"
)

func TestBridgeAddToolsSkipsDeniedAndSortsByName(t *testing.T) {
	b := &Bridge{}
	// A client value is needed to build adapters; it is never called here.
	c := newClient(&fakeServer{})
	descs := []ToolDesc{
		{Name: "write", Description: "mutate a row"},
		{Name: "query", Description: "read a row", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{Name: "drop", Description: "delete a table"},
	}
	cfg := Config{Deny: []string{"sqlite__drop"}}
	b.addTools(cfg, "sqlite", c, descs)

	tools := b.Tools()
	if len(tools) != 2 {
		t.Fatalf("got %d tools, want the denied one gone", len(tools))
	}
	if tools[0].Name() != "sqlite__query" || tools[1].Name() != "sqlite__write" {
		t.Fatalf("tools not sorted by name: %s, %s", tools[0].Name(), tools[1].Name())
	}
	for _, tl := range tools {
		if tl.Name() == "sqlite__drop" {
			t.Fatal("a denied tool must never reach the registry")
		}
	}
}

func TestBridgeDeniedServerGlobDropsWholeServer(t *testing.T) {
	b := &Bridge{}
	c := newClient(&fakeServer{})
	descs := []ToolDesc{{Name: "a"}, {Name: "b"}}
	cfg := Config{Deny: []string{"evil__*"}}
	b.addTools(cfg, "evil", c, descs)
	if len(b.Tools()) != 0 {
		t.Fatalf("a server-wide deny must drop every tool, got %d", len(b.Tools()))
	}
}
