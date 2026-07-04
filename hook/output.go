package hook

import (
	"bytes"
	"encoding/json"
)

// Output is the structured stdout a hook may return on exit 0. Every field is
// optional; a hook that only wants to block uses exit 2 and no JSON at all
// (doc 05 section 13.3).
type Output struct {
	// Continue false stops the turn with StopReason, across every event.
	Continue   *bool  `json:"continue,omitempty"`
	StopReason string `json:"stopReason,omitempty"`

	// AdditionalContext is injected for user-prompt, session-start, and
	// post-tool events.
	AdditionalContext string `json:"additionalContext,omitempty"`

	// Permission is honored only for pre-tool and permission-request, where it
	// feeds the pipeline. The dispatcher parses it now; the pipeline wiring
	// lands in the next slice.
	Permission *Permission `json:"permission,omitempty"`
}

// Permission is how a pre-tool or permission-request hook steers the pipeline.
// The Behavior is a plain string here so the package stays free of ari
// dependencies; the runner maps it onto the permission pipeline's types.
type Permission struct {
	Behavior     string          `json:"behavior"` // "allow" or "deny"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	AddRules     []string        `json:"addRules,omitempty"`
	Message      string          `json:"message,omitempty"`
	Interrupt    bool            `json:"interrupt,omitempty"` // deny and abort the turn
}

// parseOutput reads a hook's stdout as structured control. It returns nil,
// false when the stdout is not a JSON object, which is the common case: a
// hook that prints plain text on exit 0 is shown, not parsed. Only a
// well-formed JSON object with at least one known field is treated as
// control, so a hook that happens to print a JSON array or a bare string is
// still shown verbatim.
func parseOutput(stdout []byte) (*Output, bool) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	var out Output
	if err := dec.Decode(&out); err != nil {
		// Unknown fields or malformed control: treat the stdout as plain text
		// rather than guessing at a schema the hook did not mean.
		return nil, false
	}
	if out == (Output{}) {
		return nil, false
	}
	return &out, true
}
