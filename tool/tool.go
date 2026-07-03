// Package tool owns the fail-closed capability surface the model calls.
// Every tool implements one interface, and every method that could
// parallelize badly or mutate state defaults to the conservative answer,
// so a tool that says nothing is treated as unsafe (doc 04 section 2).
package tool

import (
	"context"
	"encoding/json"
	"time"
)

// Tool is one typed, fail-closed capability the model can invoke by name.
type Tool interface {
	// Name is the stable identifier the model calls and permission rules
	// match. It is lower_snake for core tools: read, write, edit, sh,
	// find, fetch.
	Name() string

	// Schema returns the JSON Schema for the argument object, plus the
	// one-line description the model reads. The loop renders this into
	// the tool list.
	Schema() Schema

	// ValidateInput checks the decoded arguments and returns a
	// model-facing reason when they are invalid. It runs before
	// permissions and shows no UI. A non-nil error here becomes a
	// tool_result the model can read and correct.
	ValidateInput(ctx context.Context, args json.RawMessage, tc *ToolContext) error

	// CheckPermissions is the tool's hook into the doc 05 pipeline. It
	// returns a PermissionResult the pipeline weighs together with rules
	// and mode. The default is Passthrough, which means "defer entirely
	// to the general system".
	CheckPermissions(ctx context.Context, args json.RawMessage, tc *ToolContext) PermissionResult

	// Call runs the tool. It streams progress through onProgress and
	// returns a Result. Call must respect ctx cancellation promptly.
	Call(ctx context.Context, args json.RawMessage, tc *ToolContext, onProgress ProgressFunc) (*Result, error)

	// IsReadOnly reports whether this invocation only observes state.
	// Default false. A read-only invocation may run in a plan-mode
	// session.
	IsReadOnly(args json.RawMessage) bool

	// IsConcurrencySafe reports whether this invocation may run in
	// parallel with other concurrency-safe tools in the same batch.
	// Default false. The loop (doc 03) partitions on this.
	IsConcurrencySafe(args json.RawMessage) bool

	// IsDestructive reports whether this invocation is irreversible (rm,
	// force push, DROP). Default false. The permission renderer (doc 05)
	// leans on this to escalate the consequence it shows.
	IsDestructive(args json.RawMessage) bool

	// MatchPrefix returns a matcher the permission pipeline (doc 05)
	// uses to test a rule pattern like sh(git commit:*) against this
	// specific invocation.
	MatchPrefix(args json.RawMessage) PrefixMatcher

	// MaxResultSize is the byte ceiling on the model-facing result
	// before it spills to disk. Zero means "no spill" (read uses this).
	MaxResultSize() int
}

// ProgressFunc receives incremental output while a tool runs.
type ProgressFunc func(chunk string)

// Schema is the model-facing description of a tool.
type Schema struct {
	Name        string          // matches Tool.Name
	Description string          // one line, budgeted; the model reads this
	Params      json.RawMessage // JSON Schema for the argument object
}

// Result is what Call returns before serialization. It separates the
// model payload from any structured data the UI wants for rich rendering.
type Result struct {
	// Model is the text the model will see, before size capping and
	// spillover.
	Model string

	// Display carries typed data for the UI renderer (a diff, a file
	// list, an exit code). It is never sent to the model. Nil for tools
	// with no rich view.
	Display any

	// Attachments are non-text blocks for the model, images for M7
	// read. Empty for everything else in v1.
	Attachments []Attachment

	// StateEffect describes any file-state map change (a read or a
	// write) so the loop can update the session invariant. Nil when the
	// tool touched no file.
	StateEffect *FileStateEffect
}

// Attachment is a non-text block a tool hands to the model. Unused until
// M7 gives read image passthrough; the field exists now so M7 is a
// capability flip, not a rewrite (doc 04 section 4.1).
type Attachment struct {
	MediaType string
	Data      []byte
}

// FileStateEffect records what a successful read or write did to a file,
// so the loop can fold it into the session file-state map (doc 04
// section 10). This is the arming step for the edit gate (D8).
type FileStateEffect struct {
	Path  string    // absolute, symlinks resolved
	Hash  string    // content hash after the operation
	Mtime time.Time // filesystem mtime after the operation
	Lines int       // line count, for the unchanged-read stub
}

// PermissionResult is a tool's contribution to the permission pipeline.
// The pipeline (doc 05, slice 7) weighs it together with rules and mode;
// a tool cannot allow past a safety check from here (D15).
type PermissionResult struct {
	kind string
}

// Passthrough means the tool has no opinion and the general rule and
// mode machinery decides. It is the Base default and the answer for
// every tool that has nothing early to say.
func Passthrough() PermissionResult { return PermissionResult{kind: "passthrough"} }

// IsPassthrough reports whether the tool deferred entirely.
func (p PermissionResult) IsPassthrough() bool { return p.kind == "passthrough" || p.kind == "" }

// Pattern is one permission rule pattern, kept as the user wrote it. The
// rule language proper lands with the pipeline (doc 05); tools only need
// enough shape here to answer MatchPrefix.
type Pattern struct {
	Tool    string // the tool name part, "sh" in sh(git:*)
	Content string // the argument part, "git:*" in sh(git:*); empty is tool-wide
	Source  string // the pattern verbatim, journaled as written
}

// PrefixMatcher tests a permission pattern against one invocation. Only
// the tool knows how to parse its own arguments into the units a rule
// matches, which is why this lives on the tool.
type PrefixMatcher interface {
	Matches(p Pattern) bool
}

// exactNameMatcher matches only the bare tool name, no content. It is
// the Base default: a tool-wide rule matches, a rule with content does
// not, because a tool that did not define content matching has none.
// The pipeline routes a pattern to a tool by name before asking the
// matcher, so the matcher only weighs the content part.
type exactNameMatcher struct{}

func (exactNameMatcher) Matches(p Pattern) bool {
	return p.Content == ""
}
