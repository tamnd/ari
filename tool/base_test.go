package tool

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// bareTool embeds Base and overrides nothing beyond the required
// identity methods. It is the shape of a half-finished tool.
type bareTool struct {
	Base
}

func (bareTool) Name() string   { return "bare" }
func (bareTool) Schema() Schema { return Schema{Name: "bare"} }
func (bareTool) ValidateInput(context.Context, json.RawMessage, *ToolContext) error {
	return nil
}
func (bareTool) Call(context.Context, json.RawMessage, *ToolContext, ProgressFunc) (*Result, error) {
	return &Result{Model: "ok"}, nil
}

// TestBaseDefaultsAreFailClosed is the table test the DoD names: a tool
// that says nothing is serial, unsafe, non-destructive, default-capped,
// and has no permission opinion, while the two read-only tools opt in
// explicitly (plan/01 slice 4, doc 04 section 2.4).
func TestBaseDefaultsAreFailClosed(t *testing.T) {
	args := json.RawMessage(`{}`)
	cases := []struct {
		tool           Tool
		readOnly, safe bool
		destructive    bool
		maxResult      int
	}{
		{bareTool{}, false, false, false, defaultMaxResultSize},
		{NewRead(), true, true, false, 0},
		{NewFind(), true, true, false, findMaxResult},
	}
	for _, c := range cases {
		name := c.tool.Name()
		if got := c.tool.IsReadOnly(args); got != c.readOnly {
			t.Errorf("%s.IsReadOnly = %v, want %v", name, got, c.readOnly)
		}
		if got := c.tool.IsConcurrencySafe(args); got != c.safe {
			t.Errorf("%s.IsConcurrencySafe = %v, want %v", name, got, c.safe)
		}
		if got := c.tool.IsDestructive(args); got != c.destructive {
			t.Errorf("%s.IsDestructive = %v, want %v", name, got, c.destructive)
		}
		if got := c.tool.MaxResultSize(); got != c.maxResult {
			t.Errorf("%s.MaxResultSize = %d, want %d", name, got, c.maxResult)
		}
	}
}

func TestBaseHasNoPermissionOpinion(t *testing.T) {
	var b bareTool
	res := b.CheckPermissions(context.Background(), json.RawMessage(`{}`), nil)
	if !res.IsPassthrough() {
		t.Error("Base must defer entirely to the general permission system")
	}
}

func TestBaseMatchesOnlyTheBareToolName(t *testing.T) {
	var b bareTool
	m := b.MatchPrefix(json.RawMessage(`{}`))
	if !m.Matches(Pattern{Tool: "bare", Source: "bare"}) {
		t.Error("a tool-wide pattern must match the default matcher")
	}
	if m.Matches(Pattern{Tool: "bare", Content: "x:*", Source: "bare(x:*)"}) {
		t.Error("a content pattern must not match a tool that defined no content matching")
	}
}
