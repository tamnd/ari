// Package parts is the render model: the UI draws exactly one data
// structure, the ordered list of typed parts that make up a session's
// messages, projected from the core's events with no second schema
// (doc 02 section 4). Each Kind, and each tool within a tool result,
// has a pure renderer here: same part, width, and theme in, same lines
// out, which is what makes the chat list memo and the golden tests
// sound.
package parts

import (
	"encoding/json"
	"time"
)

// Role is who a part belongs to.
type Role int

const (
	RoleUser Role = iota
	RoleAssistant
	RoleTool // synthetic role for a tool result attached to a call
)

// Kind drives which renderer runs.
type Kind int

const (
	KindText       Kind = iota // prose: markdown for assistant, plain for user
	KindReasoning              // model thinking, dim, carries a duration
	KindToolCall               // a tool invocation: name plus arguments
	KindToolResult             // the result of a call, rendered per tool
	KindFinish                 // end-of-turn marker: stop reason, token counts
)

// Usage is the token accounting a finish part carries for the ledger
// line.
type Usage struct {
	Input, Output, CacheRead, CacheWrite int64
}

// Part is one renderable unit. Version bumps on every update and is the
// memo key the chat list uses to notice a streaming part changed
// without diffing its content; Finished zero means still streaming.
// Ant names the producing ant, empty for the single ant of M0; in the
// colony it picks the accent color and glyph (doc 02 section 10.5).
type Part struct {
	Kind     Kind
	Role     Role
	Text     string          // KindText, KindReasoning: accumulated content
	Tool     string          // KindToolCall, KindToolResult: tool name
	Call     string          // the call id tying a result to its call
	Args     json.RawMessage // KindToolCall: the arguments, streaming in
	Result   any             // KindToolResult: typed display data or a string
	OK       bool            // KindToolResult: whether the call succeeded
	Started  time.Time
	Finished time.Time // zero until the part is done streaming
	Stop     string    // KindFinish: stop reason
	Usage    Usage     // KindFinish
	Version  uint64
	Ant      string
}

// Block is a rendered part: styled lines, each fitting the width it was
// rendered at. The chat list turns these into cells.
type Block []string
