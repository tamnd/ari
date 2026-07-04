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

	// Permission is ari's flat way for a pre-tool or permission-request hook
	// to steer the pipeline. It is honored alongside the Claude Code
	// hookSpecificOutput shape below; when both are set, Permission wins.
	Permission *Permission `json:"permission,omitempty"`

	// HookSpecificOutput is the Claude Code shape a PreToolUse hook returns to
	// steer permission: permissionDecision allow, deny, or ask, an optional
	// narrowing updatedInput, and a reason. A UserPromptSubmit or SessionStart
	// hook can also carry additionalContext here (doc 05 section 3).
	HookSpecificOutput *HookSpecificOutput `json:"hookSpecificOutput,omitempty"`
}

// HookSpecificOutput is the Claude Code per-event control block. The runner
// normalizes it onto Permission and AdditionalContext so the rest of the
// package reads one shape.
type HookSpecificOutput struct {
	HookEventName            string          `json:"hookEventName,omitempty"`
	PermissionDecision       string          `json:"permissionDecision,omitempty"`
	PermissionDecisionReason string          `json:"permissionDecisionReason,omitempty"`
	UpdatedInput             json.RawMessage `json:"updatedInput,omitempty"`
	AdditionalContext        string          `json:"additionalContext,omitempty"`
}

// perm returns the effective permission steer, preferring the flat
// Permission field and falling back to the Claude Code hookSpecificOutput
// block. It returns nil when the hook did not steer permission.
func (o *Output) perm() *Permission {
	if o == nil {
		return nil
	}
	if o.Permission != nil {
		return o.Permission
	}
	h := o.HookSpecificOutput
	if h == nil || h.PermissionDecision == "" {
		return nil
	}
	return &Permission{
		Behavior:     h.PermissionDecision,
		UpdatedInput: h.UpdatedInput,
		Message:      h.PermissionDecisionReason,
	}
}

// context returns the additional context the hook wants surfaced, from
// either the top-level field or the hookSpecificOutput block.
func (o *Output) context() string {
	if o == nil {
		return ""
	}
	if o.AdditionalContext != "" {
		return o.AdditionalContext
	}
	if o.HookSpecificOutput != nil {
		return o.HookSpecificOutput.AdditionalContext
	}
	return ""
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
