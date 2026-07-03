// Package permission is the ordered decision pipeline every tool call
// passes through before it runs (doc 05). Eight stages evaluate in a
// fixed order, later stages cannot undo an earlier bypass-immune stage,
// and the mode transforms sit near the end so no early allow can skip
// them. Precedence is the stage order, not a score: deny beats allow
// because deny is stage one and allow is stage seven (D15).
package permission

import "encoding/json"

// Behavior is the terminal verdict for a tool call.
type Behavior string

const (
	Allow Behavior = "allow"
	Deny  Behavior = "deny"
	Ask   Behavior = "ask"
)

// rank orders behaviors by restrictiveness, for combining compound
// shell subcommands: Deny > Ask > Allow.
func rank(b Behavior) int {
	switch b {
	case Deny:
		return 2
	case Ask:
		return 1
	default:
		return 0
	}
}

// moreRestrictive reports whether a restricts harder than b.
func moreRestrictive(a, b Behavior) bool { return rank(a) > rank(b) }

// Decision is what the pipeline returns for one tool call. Exactly one
// behavior is set; the rest of the fields annotate it.
type Decision struct {
	Behavior Behavior

	// UpdatedInput, when non-nil, replaces the tool input the loop will
	// execute. It exists so a resolver can narrow a call as a condition
	// of allowing it. It never widens a call; the pipeline rejects a
	// resolution that tries.
	UpdatedInput json.RawMessage

	// Reason is the structured, machine-readable account of why this
	// behavior won. The journal stores it whole.
	Reason Reason

	// Message is the human-facing, model-facing sentence shown on Deny
	// and Ask. Empty on Allow.
	Message string

	// Suggestions are rule fragments the UI offers the user after an
	// Ask, e.g. "sh(go test:*)". They are proposals, never applied
	// silently; nothing in the pipeline writes a rule.
	Suggestions []string
}

// Reason explains a Decision. Kind is always set; the other fields are
// populated per kind so an audit can reconstruct the exact stage that
// fired.
type Reason struct {
	Kind    ReasonKind  `json:"kind"`
	Stage   Stage       `json:"stage"`
	Rule    string      `json:"rule,omitempty"`    // the matched rule source, verbatim
	Mode    Mode        `json:"mode,omitempty"`    // the active mode, for Kind mode
	Sub     []SubReason `json:"sub,omitempty"`     // per-subcommand, for compound sh
	Details string      `json:"details,omitempty"` // free-form, human voiced
}

// SubReason is one subcommand's own verdict inside a compound sh call,
// so the audit and the prompt can both show which piece triggered the
// stop.
type SubReason struct {
	Command  string   `json:"command"` // the normalized subcommand
	Behavior Behavior `json:"behavior"`
	Reason   Reason   `json:"reason"`
}

// ReasonKind names which machinery produced a decision.
type ReasonKind string

const (
	KindRule    ReasonKind = "rule"    // an allow, deny, or ask rule matched
	KindTool    ReasonKind = "tool"    // the tool's own CheckPermissions decided
	KindContent ReasonKind = "content" // a content-ask rule matched
	KindSafety  ReasonKind = "safety"  // a bypass-immune safety check fired
	KindMode    ReasonKind = "mode"    // a mode transform decided
	KindDefault ReasonKind = "default" // fell through to the default ask
	KindSubcmd  ReasonKind = "subcmd"  // compound sh; Sub has the breakdown

	// KindHeadless is a headless run's resolver of last resort claiming
	// an Ask with a deny. The default for a headless Ask is deny, never
	// allow: an allow-by-default here is exactly how an automated agent
	// runs a command nobody reviewed (doc 05 section 11, D16).
	KindHeadless ReasonKind = "headless"
)

// Stage names a pipeline stage; the order here is the runtime order.
type Stage string

const (
	StageDeny       Stage = "deny"
	StageToolAsk    Stage = "tool_ask"
	StageToolCheck  Stage = "tool_check"
	StageContentAsk Stage = "content_ask"
	StageSafety     Stage = "safety"
	StageMode       Stage = "mode"
	StageAllow      Stage = "allow"
	StageDefault    Stage = "default"
	StageSubcmd     Stage = "subcmd"
)
