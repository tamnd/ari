package tool

import (
	"context"
	"encoding/json"
)

// defaultMaxResultSize is the byte ceiling a tool gets when it does not
// pick its own. Matches the sh and find caps (doc 04 section 3.3).
const defaultMaxResultSize = 30_000

// Base carries the fail-closed defaults. Embed it and override only the
// methods whose answer is not the conservative one. A tool that embeds
// Base and overrides nothing is a serial, unsafe, non-destructive tool
// with a default size cap and no permission opinion, which is exactly
// the shape a half-finished tool must have (doc 04 section 2.4).
type Base struct{}

func (Base) IsReadOnly(json.RawMessage) bool        { return false }
func (Base) IsConcurrencySafe(json.RawMessage) bool { return false }
func (Base) IsDestructive(json.RawMessage) bool     { return false }
func (Base) MaxResultSize() int                     { return defaultMaxResultSize }

func (Base) CheckPermissions(context.Context, json.RawMessage, *ToolContext) PermissionResult {
	return Passthrough()
}

func (Base) MatchPrefix(json.RawMessage) PrefixMatcher {
	return exactNameMatcher{} // matches only the bare tool name, no content
}
