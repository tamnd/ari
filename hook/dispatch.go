package hook

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
)

// Options builds a Runner for one session.
type Options struct {
	// Commands is every configured hook across all layers. The runner filters
	// them per event, per matcher, and per trust.
	Commands []Command
	// Trusted is the workspace trust decision (doc 05 section 12). When false,
	// only user-layer hooks run; the repo's own hooks stay silent until the
	// user trusts the workspace.
	Trusted bool
	// Untrusted marks an automation session fed untrusted content (D19). When
	// true, no hook runs at all, even a user hook in a trusted workspace,
	// because a prompt-injected session could otherwise trigger configured
	// commands with attacker-chosen timing.
	Untrusted bool
	// ProjectDir and Session seed the hook environment.
	ProjectDir string
	Session    string
	// run is injected by tests to stand in for spawning a process. Nil uses
	// the real process contract.
	run runFunc
}

// Runner dispatches hooks for one session. It owns the trust gate: every fire
// consults it, and there is no path around it (doc 05 section 12).
type Runner struct {
	opts Options
	run  runFunc

	mu    sync.Mutex
	fired map[string]bool // once-guard, keyed by command identity
}

// NewRunner builds a session dispatcher over the configured hooks.
func NewRunner(o Options) *Runner {
	run := o.run
	if run == nil {
		run = runCommand
	}
	return &Runner{opts: o, run: run, fired: map[string]bool{}}
}

// Enabled reports whether hooks may run for this session at all. It is the
// single gate for the untrusted-content rule: a session fed untrusted content
// runs no hook regardless of workspace trust (doc 05 sections 12 and 14).
func (r *Runner) Enabled() bool { return !r.opts.Untrusted }

// Any reports whether any hook could ever run for this session, so a caller
// can skip wiring the seam entirely when there is nothing to fire.
func (r *Runner) Any() bool {
	if !r.Enabled() {
		return false
	}
	return slices.ContainsFunc(r.opts.Commands, r.trusts)
}

// Outcome aggregates what the hooks for one event decided. Block is set when
// any hook blocked; Message carries the joined block or warning text; Context
// carries the joined additional context from exit-0 hooks; StopContinue is
// false when a hook asked the turn to stop.
type Outcome struct {
	Block        bool
	Message      string
	Context      string
	StopContinue *bool
	StopReason   string
	Results      []Result
}

// Fire runs every hook registered for an event that passes the trust gate and
// the matcher, and aggregates their results. A disabled session or an event
// with no matching hook returns the zero Outcome, so the caller treats "no
// hooks" and "hooks all passed" the same way.
func (r *Runner) Fire(ctx context.Context, ev Event, p Payload) Outcome {
	var out Outcome
	if !r.Enabled() {
		return out
	}
	p.Event = ev
	payload, err := json.Marshal(p)
	if err != nil {
		return out
	}
	env := hookEnv(ev, p)

	var blocks, contexts []string
	for i := range r.opts.Commands {
		c := r.opts.Commands[i]
		if c.Event != ev || !r.trusts(c) || !c.Applies(p.Tool) {
			continue
		}
		if c.Once && !r.claimOnce(c) {
			continue
		}
		if c.Async {
			// A detached hook cannot block and cannot return a decision; it is
			// for fire-and-forget side effects (doc 05 section 13.4).
			go r.run(context.WithoutCancel(ctx), c, payload, env)
			continue
		}
		res := r.run(ctx, c, payload, env)
		out.Results = append(out.Results, res)
		switch {
		case res.Blocking:
			out.Block = true
			if msg := strings.TrimSpace(res.Message); msg != "" {
				blocks = append(blocks, msg)
			}
		case res.NonBlockingError:
			// A non-blocking error warns the user and does not touch the model
			// context; it is collected in Results for the caller to surface.
		case res.Output != nil:
			if res.Output.AdditionalContext != "" {
				contexts = append(contexts, res.Output.AdditionalContext)
			}
			if res.Output.Continue != nil && !*res.Output.Continue {
				cont := false
				out.StopContinue = &cont
				out.StopReason = res.Output.StopReason
			}
		}
	}
	out.Message = strings.Join(blocks, "\n\n")
	out.Context = strings.Join(contexts, "\n\n")
	return out
}

// trusts applies the workspace trust gate to one command. A user-layer hook
// is always trusted because the user wrote it; a project or local hook lives
// in the repo an attacker can control, so it runs only in a trusted
// workspace (doc 05 section 12).
func (r *Runner) trusts(c Command) bool {
	if c.Layer == "user" {
		return true
	}
	return r.opts.Trusted
}

// claimOnce returns true the first time a once-hook is seen this session and
// false thereafter.
func (r *Runner) claimOnce(c Command) bool {
	key := string(c.Event) + "\x00" + c.Layer + "\x00" + c.Command
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fired[key] {
		return false
	}
	r.fired[key] = true
	return true
}

// hookEnv builds the extra environment a hook receives on top of the process
// environment: the event, the session, the project directory, and, for a tool
// event whose input names a path, the file the tool touched, so a formatter
// hook can act on ${ARI_TOOL_FILE} (doc 05 section 13, the PreToolUse
// example).
func hookEnv(ev Event, p Payload) []string {
	env := []string{
		"ARI_EVENT=" + string(ev),
		"ARI_SESSION_ID=" + p.Session,
		"ARI_PROJECT_DIR=" + p.Cwd,
	}
	if p.Tool != "" {
		env = append(env, "ARI_TOOL_NAME="+p.Tool)
	}
	if file := inputPath(p.Input); file != "" {
		env = append(env, "ARI_TOOL_FILE="+file)
	}
	return env
}

// inputPath pulls a file path out of a tool input when it has one, so the
// tool hooks that care about the touched file get it without parsing the
// whole input themselves.
func inputPath(input json.RawMessage) string {
	if len(input) == 0 {
		return ""
	}
	var fields struct {
		Path string `json:"path"`
		File string `json:"file"`
	}
	if json.Unmarshal(input, &fields) != nil {
		return ""
	}
	if fields.Path != "" {
		return fields.Path
	}
	return fields.File
}

// Describe renders a one-line summary of a command for doctor and the trust
// prompt, so a user sees what a workspace's hooks would run before trusting
// it (doc 05 section 12).
func Describe(c Command) string {
	matcher := c.matcher
	if matcher == "" {
		matcher = "*"
	}
	return fmt.Sprintf("%s [%s] %s: %s", c.Event, c.Layer, matcher, c.Command)
}
