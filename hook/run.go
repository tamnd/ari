package hook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
)

// Payload is the JSON handed to a hook on stdin. It is event-shaped: a tool
// event carries the tool and its input, a prompt event carries the prompt.
// Everything a hook needs to decide is here, so a hook never has to reach
// back into ari (doc 05 section 13.2).
type Payload struct {
	Event   Event           `json:"event"`
	Tool    string          `json:"tool,omitempty"`
	Input   json.RawMessage `json:"input,omitempty"`
	Result  string          `json:"result,omitempty"`
	IsError bool            `json:"isError,omitempty"`
	Prompt  string          `json:"prompt,omitempty"`
	Reason  string          `json:"reason,omitempty"` // SessionStart/SessionEnd source
	Session string          `json:"session,omitempty"`
	Cwd     string          `json:"cwd,omitempty"`
}

// Result is what one command hook returned, interpreted per the exit-code
// contract: 0 is success (stdout may carry structured control), 2 blocks
// (stderr is the block message), any other non-zero is a non-blocking error
// (stderr warns the user, the agent is not blocked).
type Result struct {
	Event            Event
	ExitCode         int
	Blocking         bool
	NonBlockingError bool
	Message          string // the block message or the warning
	Stdout           string // raw stdout, shown when it is not structured control
	Output           *Output
}

// exitCoder lets tests stand in for the process without spawning one.
type runFunc func(ctx context.Context, c Command, payload []byte, extraEnv []string) Result

// defaultShell is the shell a command hook runs under when the hook does not
// name one. It is a login-agnostic POSIX shell so a hook string is portable.
const defaultShell = "sh"

// runCommand spawns one command hook and interprets its result. payload is
// the event JSON on stdin; the contract is exit code and stdout out. A hook
// that fails to start or is killed on timeout is a non-blocking error, never
// a silent allow, so a broken or slow hook degrades to a warning instead of
// skipping a block it was meant to impose (doc 05 section 13.4).
func runCommand(ctx context.Context, c Command, payload []byte, extraEnv []string) Result {
	ctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()

	shell := c.Shell
	if shell == "" {
		shell = defaultShell
	}
	cmd := exec.CommandContext(ctx, shell, "-c", c.Command)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Env = append(os.Environ(), extraEnv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr

	err := cmd.Run()
	// Redact secret env values from stdout before anything reads it, so a
	// hook that echoes a key cannot leak it into model context through
	// additionalContext or the raw stdout display (D16).
	out := redactSecrets(stdout.String(), cmd.Env)
	res := Result{Event: c.Event, ExitCode: exitCode(err), Stdout: out}

	// A timeout kill is a non-blocking error, not a block and not an allow.
	if ctx.Err() == context.DeadlineExceeded {
		res.NonBlockingError = true
		res.Message = "hook timed out after " + c.Timeout.String()
		return res
	}

	switch res.ExitCode {
	case 0:
		if parsed, ok := parseOutput([]byte(out)); ok {
			res.Output = parsed
		}
	case 2:
		res.Blocking = true
		res.Message = stderr.String()
	default:
		res.NonBlockingError = true
		res.Message = stderr.String()
	}
	return res
}

// exitCode extracts the process exit code from a Run error: 0 on success, the
// process code on a normal exit, and -1 for a failure to start.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return ee.ExitCode()
	}
	return -1
}
