package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// searchName is the built-in tool the model calls to load a deferred tool's
// schema before it can invoke it. It is the search-and-load half of the
// deferral: deferred tools ride turn one by name only, and this tool turns a
// name or a keyword into the full schemas, which stay loaded for the rest of
// the session (doc 13, D20).
const searchName = "tool_search"

// searchTool loads deferred tool schemas on demand. It holds the registry so
// it can list what is deferred, match a query, and mark the matches loaded.
type searchTool struct {
	Base
	reg *Registry
}

// NewToolSearch builds the search-and-load tool over a registry. Register it
// only when the registry actually has deferred tools, so a session with no
// MCP servers never shows the model a tool it cannot use.
func NewToolSearch(reg *Registry) Tool { return &searchTool{reg: reg} }

func (t *searchTool) Name() string { return searchName }

func (t *searchTool) Schema() Schema {
	return Schema{
		Name:        searchName,
		Description: "Load the full schema for one or more deferred tools (for example MCP tools announced by name only) so you can call them. Pass a keyword query or an explicit list of names.",
		Params: json.RawMessage(`{
  "type": "object",
  "properties": {
    "query": {"type": "string", "description": "keywords matched against deferred tool names and descriptions"},
    "names": {"type": "array", "items": {"type": "string"}, "description": "exact deferred tool names to load"}
  }
}`),
	}
}

// searchArgs is the decoded input: a free-text query, an explicit name list,
// or both.
type searchArgs struct {
	Query string   `json:"query"`
	Names []string `json:"names"`
}

func (t *searchTool) ValidateInput(_ context.Context, args json.RawMessage, _ *ToolContext) error {
	var a searchArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	if strings.TrimSpace(a.Query) == "" && len(a.Names) == 0 {
		return fmt.Errorf("give a query or a list of names to load")
	}
	return nil
}

// IsReadOnly reports true: loading a schema reveals it to the model but
// changes no state, so the tool is safe in a plan-mode session.
func (t *searchTool) IsReadOnly(json.RawMessage) bool { return true }

// IsConcurrencySafe reports true: the load is a registry mutation guarded by
// the registry's own lock and touches no files.
func (t *searchTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (t *searchTool) Call(_ context.Context, args json.RawMessage, _ *ToolContext, _ ProgressFunc) (*Result, error) {
	var a searchArgs
	_ = json.Unmarshal(args, &a)

	deferred := t.reg.DeferredTools()
	if len(deferred) == 0 {
		return &Result{Model: "No deferred tools are available to load."}, nil
	}

	matched := selectTools(deferred, a)
	if len(matched) == 0 {
		return &Result{Model: "No deferred tool matched. Available: " + strings.Join(deferredNames(deferred), ", ")}, nil
	}

	loaded := t.reg.Load(matched...)
	if len(loaded) == 0 {
		return &Result{Model: "Those tools are already loaded and callable."}, nil
	}
	return &Result{Model: renderSchemas(loaded)}, nil
}

// selectTools resolves the requested tools to names: explicit names match
// exactly, and a query matches when every whitespace-separated term is a
// substring of the tool's name or description, so a two-word query narrows
// rather than widens.
func selectTools(deferred []Tool, a searchArgs) []string {
	want := map[string]bool{}
	for _, n := range a.Names {
		want[n] = true
	}
	terms := strings.Fields(strings.ToLower(a.Query))

	var names []string
	for _, d := range deferred {
		name := d.Name()
		if want[name] {
			names = append(names, name)
			continue
		}
		if len(terms) > 0 && matchesAll(d, terms) {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names
}

// matchesAll reports whether every term appears in the tool's name or
// description.
func matchesAll(d Tool, terms []string) bool {
	hay := strings.ToLower(d.Name() + " " + d.Schema().Description)
	for _, term := range terms {
		if !strings.Contains(hay, term) {
			return false
		}
	}
	return true
}

// renderSchemas formats the loaded tools so the model can call them this
// turn: name, description, and the JSON Schema for the arguments.
func renderSchemas(loaded []Tool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Loaded %d tool(s). They are now callable:\n", len(loaded))
	for _, t := range loaded {
		s := t.Schema()
		fmt.Fprintf(&b, "\n## %s\n%s\nParameters:\n%s\n", s.Name, s.Description, string(s.Params))
	}
	return b.String()
}

func deferredNames(deferred []Tool) []string {
	names := make([]string, len(deferred))
	for i, d := range deferred {
		names[i] = d.Name()
	}
	return names
}
