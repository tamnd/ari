package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// fakeTool is a minimal Tool for exercising deferral and search. Its schema
// description feeds the query matcher.
type fakeTool struct {
	Base
	name string
	desc string
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Schema() Schema {
	return Schema{Name: f.name, Description: f.desc, Params: json.RawMessage(`{"type":"object"}`)}
}
func (f *fakeTool) ValidateInput(context.Context, json.RawMessage, *ToolContext) error { return nil }
func (f *fakeTool) Call(context.Context, json.RawMessage, *ToolContext, ProgressFunc) (*Result, error) {
	return &Result{Model: "ok"}, nil
}

func TestDeferredToolIsHiddenFromSchemaNamesUntilLoaded(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "read"})
	_ = r.RegisterDeferred(&fakeTool{name: "sqlite__query", desc: "run a read query"})

	names := r.SchemaNames()
	if contains(names, "sqlite__query") {
		t.Fatal("a deferred tool must not ride the schema listing before it is loaded")
	}
	if !contains(names, "read") {
		t.Fatal("a normal tool must be in the schema listing")
	}
	// It is still resolvable, so a rogue call would reach the tool, but the
	// model is never shown its schema.
	if _, ok := r.Resolve("sqlite__query"); !ok {
		t.Fatal("a deferred tool must still resolve by name")
	}

	// After loading, it joins the schema listing.
	newly := r.Load("sqlite__query")
	if len(newly) != 1 {
		t.Fatalf("Load returned %d, want the one newly-loaded tool", len(newly))
	}
	if !contains(r.SchemaNames(), "sqlite__query") {
		t.Fatal("a loaded tool must join the schema listing")
	}
	// Loading again is a no-op: the schema cost is paid once.
	if again := r.Load("sqlite__query"); len(again) != 0 {
		t.Fatalf("re-loading returned %d, want none", len(again))
	}
}

func TestDeferredToolsListsOnlyUnloaded(t *testing.T) {
	r := NewRegistry()
	_ = r.RegisterDeferred(&fakeTool{name: "a__one"})
	_ = r.RegisterDeferred(&fakeTool{name: "b__two"})
	if got := len(r.DeferredTools()); got != 2 {
		t.Fatalf("DeferredTools = %d, want 2", got)
	}
	r.Load("a__one")
	left := r.DeferredTools()
	if len(left) != 1 || left[0].Name() != "b__two" {
		t.Fatalf("DeferredTools after load = %v, want only b__two", names(left))
	}
}

func TestToolSearchLoadsByNameAndQuery(t *testing.T) {
	r := NewRegistry()
	_ = r.RegisterDeferred(&fakeTool{name: "sqlite__query", desc: "run a read query against sqlite"})
	_ = r.RegisterDeferred(&fakeTool{name: "docs__search", desc: "search the docs corpus"})
	search := NewToolSearch(r)

	// By exact name.
	res, err := search.Call(context.Background(), json.RawMessage(`{"names":["sqlite__query"]}`), &ToolContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Model, "sqlite__query") || !strings.Contains(res.Model, "Parameters:") {
		t.Fatalf("name load did not render the schema:\n%s", res.Model)
	}
	if contains(r.SchemaNames(), "sqlite__query") == false {
		t.Fatal("the loaded tool must now be schema-visible")
	}

	// By query term, matched against the description.
	res, err = search.Call(context.Background(), json.RawMessage(`{"query":"docs corpus"}`), &ToolContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Model, "docs__search") {
		t.Fatalf("query load missed docs__search:\n%s", res.Model)
	}
}

func TestToolSearchNoMatchListsWhatIsAvailable(t *testing.T) {
	r := NewRegistry()
	_ = r.RegisterDeferred(&fakeTool{name: "sqlite__query", desc: "read"})
	search := NewToolSearch(r)
	res, err := search.Call(context.Background(), json.RawMessage(`{"query":"nonexistent-xyzzy"}`), &ToolContext{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Model, "No deferred tool matched") || !strings.Contains(res.Model, "sqlite__query") {
		t.Fatalf("a miss should name what is available:\n%s", res.Model)
	}
}

func TestToolSearchValidateRequiresQueryOrNames(t *testing.T) {
	r := NewRegistry()
	search := NewToolSearch(r)
	if err := search.ValidateInput(context.Background(), json.RawMessage(`{}`), &ToolContext{}); err == nil {
		t.Fatal("an empty search must be rejected")
	}
}

func contains(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}

func names(ts []Tool) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Name()
	}
	return out
}
