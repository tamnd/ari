// Package hook runs user-supplied programs that observe and steer the agent
// loop at named events, behind the workspace trust gate from D16. A hook is
// an external process, not an ari plugin format: ari spawns it with a JSON
// payload on stdin and reads its answer from the exit code and stdout, so a
// hook is any language a user can spawn (doc 05 sections 12 and 13).
//
// The package holds no ari dependencies. The loop reaches it through a small
// seam in the agent package, and the runner builds a dispatcher over the
// discovered config and the trust decision; the permission-steering and
// session-lifecycle behaviors are wired in the next slice.
package hook

// Event names one point in the loop where hooks fire. The values match the
// Claude Code event names so a hook config written for that ecosystem
// transfers unchanged (doc 05 section 13.1, research A.6).
type Event string

const (
	// PreToolUse fires after a call is validated and before the permission
	// decision, for each call. A blocking result stops the call.
	PreToolUse Event = "PreToolUse"
	// PostToolUse fires after a tool call completes successfully, with the
	// result available. This is where a linter or formatter runs.
	PostToolUse Event = "PostToolUse"
	// PostToolUseFailure fires after a tool call returns an error, so a hook
	// can react to what failed.
	PostToolUseFailure Event = "PostToolUseFailure"
	// UserPromptSubmit fires when the user submits a prompt, before the model
	// sees it. Behavior lands in the next slice.
	UserPromptSubmit Event = "UserPromptSubmit"
	// SessionStart fires when a session begins. Behavior lands in the next
	// slice.
	SessionStart Event = "SessionStart"
	// SessionEnd fires when a session ends.
	SessionEnd Event = "SessionEnd"
	// Stop fires when the ant is about to finish a turn with no more tool
	// calls. A blocking Stop re-drives the loop; that behavior lands in the
	// next slice under the spiral guard.
	Stop Event = "Stop"
	// PreCompact fires before the loop compacts context.
	PreCompact Event = "PreCompact"
	// PostCompact fires after a compaction completes.
	PostCompact Event = "PostCompact"
)

// events is the M1 set: the coding-relevant slice of the doc 05 list.
var events = map[Event]bool{
	PreToolUse:         true,
	PostToolUse:        true,
	PostToolUseFailure: true,
	UserPromptSubmit:   true,
	SessionStart:       true,
	SessionEnd:         true,
	Stop:               true,
	PreCompact:         true,
	PostCompact:        true,
}

// Known reports whether an event name is part of the M1 set, so config can
// warn on a typo rather than silently ignoring a whole hook.
func Known(e Event) bool { return events[e] }

// toolEvent reports whether an event carries a tool, so the matcher applies.
func toolEvent(e Event) bool {
	switch e {
	case PreToolUse, PostToolUse, PostToolUseFailure:
		return true
	default:
		return false
	}
}
