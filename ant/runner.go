package ant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/ari/agent"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/hook"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/lsp"
	"github.com/tamnd/ari/mcp"
	"github.com/tamnd/ari/memory"
	"github.com/tamnd/ari/nest"
	"github.com/tamnd/ari/permission"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/skill"
	"github.com/tamnd/ari/tool"
)

// gitStatusMaxLines bounds what a dirty tree contributes to block two,
// so a thousand-file rebase does not eat the prompt.
const gitStatusMaxLines = 40

// Runner puts the agent loop behind the core's TurnRunner seam. One
// runner serves the colony; it wakes one worker per session and keeps
// it awake for the colony's lifetime, because the worker owns that
// session's context window (doc 01 section 2.2).
type Runner struct {
	// Memory is the store seam the ant holds and does not call in M0
	// (doc 01 section 2.1). Nil renders the pinned index placeholder.
	Memory Memory

	// GitStatus overrides the git probe for deterministic tests. Nil
	// runs git against the workspace root.
	GitStatus func(root string) string

	// LSPClient overrides the language-server seam the tools read. Nil
	// uses the real service built at Bind. It exists so the demo replay
	// can drive the self-correcting edit loop against a deterministic
	// diagnostics source instead of a live gopls whose timing would make
	// a release gate flaky; the real adapter is proven by the LSP fixture
	// suite (plan 02 slices 5 and 6).
	LSPClient lsp.LSPClient

	registry *provider.Registry
	ledger   *ledger.Ledger
	config   *coreConfig
	nest     nest.Nest
	asks     Asker
	lsp      *lsp.Service
	hooks    []hook.Command
	trust    *hook.TrustStore
	headless bool
	bound    bool

	mu      sync.Mutex
	workers map[core.SessionID]*worker
}

// coreConfig is the slice of config the runner reads, so the field list
// documents the dependency instead of hiding it behind the whole struct.
type coreConfig struct {
	mode       string
	lspEnabled bool
}

// Asker is how a blocked Ask reaches the client and the answer comes
// back. The colony's Asks registry implements it; a headless run leaves
// it nil and the Ask stands as a refusal (doc 05 section 3).
type Asker interface {
	Wait(ctx context.Context, s core.SessionID, request string) (core.RespondRequest, error)
}

// NewRunner builds an unbound runner. Pass it to core.Open via
// core.WithRunner, then Bind the opened colony:
//
//	r := ant.NewRunner()
//	c, err := core.Open(ctx, dir, core.WithRunner(r))
//	r.Bind(c)
//
// Open takes the runner as an option before the colony exists, so
// binding is the second step.
func NewRunner() *Runner {
	return &Runner{workers: map[core.SessionID]*worker{}}
}

// Bind connects the runner to the opened colony's kernel: the provider
// registry for tier resolution, the ledger for metering, the config for
// the default permission mode, and the nest for paths.
func (r *Runner) Bind(c *core.Colony) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.registry = c.Registry()
	r.ledger = c.Ledger()
	cfg := c.Config()
	r.config = &coreConfig{mode: cfg.Mode, lspEnabled: cfg.LSP.Enabled}
	r.nest = c.Nest()
	r.asks = c.Asks()
	// The language-server service is off unless config turns it on. Built
	// once here and shared by every worker, so the colony runs one gopls per
	// module, not one per session (doc 04 section 6).
	r.lsp = lsp.New(lsp.Options{Enabled: r.config.lspEnabled, Root: r.nest.Root})
	// Hooks are parsed once at bind and their workspace trust is read from the
	// global nest, so every worker shares one trust view. A missing or corrupt
	// trust file loads as untrusted, so the gate fails closed (doc 05 section
	// 12, D16).
	r.hooks = cfg.Hooks()
	r.trust = hook.LoadTrust(r.nest.TrustFile())
	r.bound = true
}

// Close tears down the runner's background resources. The colony calls it
// on shutdown through the optional io.Closer seam, so a spawned language
// server and every MCP server child process end with the session rather
// than outliving it.
func (r *Runner) Close() error {
	r.mu.Lock()
	svc := r.lsp
	workers := make([]*worker, 0, len(r.workers))
	for _, w := range r.workers {
		workers = append(workers, w)
	}
	r.mu.Unlock()
	if svc != nil {
		svc.Shutdown()
	}
	for _, w := range workers {
		if w.mcp != nil {
			_ = w.mcp.Close()
		}
		closeBackgroundShells(w)
	}
	return nil
}

// closeBackgroundShells reaps any detached shells a worker's tools left
// running, so a background build cannot outlive the session. It finds the
// owner by interface rather than naming sh, so the runner stays ignorant of
// which tool spawns processes.
func closeBackgroundShells(w *worker) {
	if w.loop == nil || w.loop.Tools == nil {
		return
	}
	for _, name := range w.loop.Tools.Names() {
		t, ok := w.loop.Tools.Resolve(name)
		if !ok {
			continue
		}
		if bc, ok := t.(tool.BackgroundCloser); ok {
			_ = bc.CloseBackground()
		}
	}
}

// warn surfaces a non-fatal setup problem to the operator's terminal. An
// MCP server that will not connect is worth a line but never a stopped
// session, so it goes to stderr and the session proceeds.
func (r *Runner) warn(msg string) {
	_, _ = fmt.Fprintln(os.Stderr, "ari: "+msg)
}

// Headless swaps the interactive resolver for the resolver of last
// resort: every Ask that reaches the pipeline is claimed with a deny
// carrying KindHeadless, so the run never blocks on a prompt nobody can
// see and never runs a call nobody reviewed (doc 05 section 11). Call
// it after Bind and before Start.
func (r *Runner) Headless() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks = nil
	r.headless = true
}

// RunTurn implements core.TurnRunner: it wakes or finds the session's
// worker and drives one turn through the loop.
func (r *Runner) RunTurn(ctx context.Context, t *core.TurnHandle) error {
	w, err := r.workerFor(ctx, t)
	if err != nil {
		return err
	}
	return w.runTurn(ctx, t)
}

func (r *Runner) workerFor(ctx context.Context, t *core.TurnHandle) (*worker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.bound {
		return nil, core.Errf(core.ErrInternal, "the ant runner is not bound; call Bind after core.Open")
	}
	if w, ok := r.workers[t.Session]; ok {
		return w, nil
	}
	w, err := r.wake(ctx, t)
	if err != nil {
		return nil, err
	}
	r.workers[t.Session] = w
	return w, nil
}

// wake builds the session's worker: tier resolved to a provider chain,
// the card's tool allowlist over the six real tools, the permission
// pipeline, and the loop with the three-block cache-aligned prompt.
// Everything computed here is session-stable by construction, which is
// what keeps blocks one and two byte-identical across turns (D14).
func (r *Runner) wake(ctx context.Context, t *core.TurnHandle) (*worker, error) {
	card := WorkerCard()
	if err := card.Validate(); err != nil {
		return nil, core.Wrap(core.ErrInternal, err, "validating the worker card")
	}

	chain, err := r.registry.Resolve(string(card.Tier))
	if err != nil {
		return nil, core.Wrap(core.ErrConfig, err, "resolving the worker's model tier")
	}
	primary := chain[0]
	fallback := ""
	if len(chain) > 1 && chain[1].Provider.Name() == primary.Provider.Name() {
		fallback = chain[1].Model
	}

	reg := tool.NewRegistry()
	for _, tl := range []tool.Tool{
		tool.NewRead(), tool.NewFind(), tool.NewWrite(),
		tool.NewEdit(), tool.NewSh(), tool.NewFetch(),
	} {
		if err := reg.Register(tl); err != nil {
			return nil, core.Wrap(core.ErrInternal, err, "registering the core tools")
		}
	}
	reg = reg.ForAllowlist(card.Tools)

	// Skills and slash commands are discovered once per session: their
	// frontmatter feeds the block two listing and the skill tool's resolver,
	// their bodies stay on disk until an invocation reads one (doc 13 section
	// 2.5). cwd is the process working directory; the walk stops at the root.
	cwd, _ := os.Getwd()
	skills, _ := skill.Discover(skill.Options{
		Root:      r.nest.Root,
		Cwd:       cwd,
		GlobalDir: r.nest.Global,
	})

	var lspClient lsp.LSPClient = r.lsp
	if r.LSPClient != nil {
		lspClient = r.LSPClient
	}
	tc := &tool.ToolContext{
		Cwd:   r.nest.Root,
		Files: tool.NewFileState(),
		Ant:   tool.AntID(card.ID),
		Spill: tool.NewDiskSpill(filepath.Join(r.nest.ProjectStateDir(), "spill")),
		LSP:   lspClient,
	}

	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil && resolved != "" {
		self = resolved
	}
	// The floor compares symlink-resolved mutation targets, so the
	// protected paths resolve the same way; otherwise a root behind a
	// symlink (macOS /var -> /private/var) hides the nest from it.
	pipe := &permission.Pipeline{
		Mode: permission.Mode(r.config.mode),
		Paths: permission.Paths{
			Root:         tool.ResolveMutationPath(r.nest.Root),
			Nest:         tool.ResolveMutationPath(r.nest.ProjectDir()),
			GlobalNest:   tool.ResolveMutationPath(r.nest.Global),
			Home:         tool.ResolveMutationPath(home),
			AriBinary:    self,
			GlobalConfig: tool.ResolveMutationPath(r.nest.GlobalConfig()),
		},
	}

	w := &worker{
		card:        card,
		tc:          tc,
		pipe:        pipe,
		session:     string(t.Session),
		defaultMode: permission.Mode(r.config.mode),
		asks:        r.asks,
		headless:    r.headless,
	}
	pipe.Resolver = permission.ResolverFunc(w.resolve)

	// The skill tool ships in the binary but is registered only when a
	// model-visible skill was discovered, so a repo with no skills does not
	// hand the model a tool it can never use (doc 13 section 2.5).
	if modelVisibleSkill(skills) {
		if err := reg.Register(tool.NewSkill(w.skillDeps(reg, skills))); err != nil {
			return nil, core.Wrap(core.ErrInternal, err, "registering the skill tool")
		}
	}

	// MCP tools attach the same way, deferred: each server's tools are
	// registered by name only and their schemas stay off turn one until the
	// model loads one through tool_search. A server that fails to connect is
	// a warning, not a session failure (doc 13, D20).
	deferredMCP := r.attachMCP(ctx, cwd, reg, w)

	w.loop = &agent.Loop{
		Provider: primary.Provider,
		Model:    primary.Model,
		Fallback: fallback,
		System: SystemPrompt(Env{
			Cwd:      r.nest.Root,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Model:    primary.Model,
		}),
		Prefix:  []provider.Message{BlockTwo(r.withDeferred(r.blockTwoContext(ctx, card, primary.Provider.Caps().MaxContext, skills), deferredMCP))},
		Tools:   reg,
		TC:      tc,
		Decide:  w.decide,
		Record:  r.ledger.Record,
		Session: string(t.Session),
		Tier:    string(card.Tier),
	}

	// Hooks reach the loop through a bridge only when a hook could actually
	// fire: the workspace trust gate decides whether the repo's own hooks
	// count, and a session with nothing to run keeps the seam nil so the loop
	// pays nothing (doc 05 section 12).
	hr := hook.NewRunner(hook.Options{
		Commands:   r.hooks,
		Trusted:    r.trust.IsTrusted(r.nest.Root),
		ProjectDir: r.nest.Root,
		Session:    string(t.Session),
	})
	if hr.Any() {
		bridge := &hookBridge{runner: hr, session: string(t.Session), cwd: r.nest.Root}
		w.loop.Hooks = bridge
		// PreToolUse steers the permission decision, so it feeds the pipeline
		// rather than the loop's post-tool seam. The pipeline weighs its verdict
		// in its own stage and the safety floor still vets a hook allow (doc 05
		// section 3, D15).
		pipe.Hook = bridge.preToolDecide
	}
	return w, nil
}

// attachMCP loads the MCP configuration, connects to every configured
// server, and registers each advertised tool deferred so it rides turn one
// by name only. It registers the tool_search built-in only when a server
// actually produced a tool, so a session with no MCP config never sees it.
// It returns the by-name announce block for block two, empty when nothing
// is configured. A connection failure is a warning, never a session error.
func (r *Runner) attachMCP(ctx context.Context, cwd string, reg *tool.Registry, w *worker) string {
	cfg, err := mcp.Discover(mcp.Options{Root: r.nest.Root, Cwd: cwd, GlobalDir: r.nest.Global})
	if err != nil {
		r.warn("mcp config: " + err.Error())
		return ""
	}
	if len(cfg.Servers) == 0 {
		return ""
	}

	bridge := mcp.Setup(context.WithoutCancel(ctx), cfg)
	for _, warn := range bridge.Warnings {
		r.warn(warn)
	}
	tools := bridge.Tools()
	if len(tools) == 0 {
		_ = bridge.Close()
		return ""
	}
	w.mcp = bridge

	var names []string
	for _, t := range tools {
		if err := reg.RegisterDeferred(t); err != nil {
			r.warn("mcp tool: " + err.Error())
			continue
		}
		names = append(names, "- "+t.Name())
	}
	if len(names) == 0 {
		return ""
	}
	if err := reg.Register(tool.NewToolSearch(reg)); err != nil {
		r.warn("registering tool_search: " + err.Error())
	}
	return strings.Join(names, "\n")
}

// withDeferred folds the MCP announce block into a block-two context.
func (r *Runner) withDeferred(c Context, deferred string) Context {
	c.DeferredTools = deferred
	return c
}

// hookBridge adapts the hook dispatcher to the loop's agent.Hooks seam, so the
// agent package never imports hook. It maps a tool call to a hook.Payload,
// fires the event, and folds the outcome back into the loop's small result.
type hookBridge struct {
	runner  *hook.Runner
	session string
	cwd     string
}

// preToolDecide is the pipeline's PreToolUse seam. It fires the pre-tool
// hooks and maps their aggregated steer onto a permission.HookVerdict: a
// block or an explicit deny becomes a deny, an allow may carry a narrowed
// updatedInput, an ask forces a prompt, and any additionalContext rides
// along. ok=false means no hook actually ran or none steered the call, so
// the pipeline proceeds as if there were no hook (doc 05 section 3).
func (b *hookBridge) preToolDecide(ctx context.Context, call permission.Call) (permission.HookVerdict, bool) {
	out := b.runner.Fire(ctx, hook.PreToolUse, hook.Payload{
		Tool:    call.Tool.Name(),
		Input:   call.Input,
		Session: b.session,
		Cwd:     b.cwd,
	})
	v := permission.HookVerdict{Context: out.Context}
	if out.Permission != nil {
		switch out.Permission.Behavior {
		case "deny":
			v.Behavior = permission.Deny
		case "allow":
			v.Behavior = permission.Allow
		case "ask":
			v.Behavior = permission.Ask
		}
		v.UpdatedInput = out.Permission.UpdatedInput
		v.Message = out.Permission.Message
	}
	if v.Behavior == "" && v.Context == "" {
		return permission.HookVerdict{}, false
	}
	return v, true
}

// PostTool fires the post-tool hooks after the tool returns. A failed call
// routes to the post-tool-failure event so a hook can distinguish the two.
func (b *hookBridge) PostTool(ctx context.Context, toolName string, input json.RawMessage, result string, isErr bool) agent.HookResult {
	ev := hook.PostToolUse
	if isErr {
		ev = hook.PostToolUseFailure
	}
	out := b.runner.Fire(ctx, ev, hook.Payload{
		Tool:    toolName,
		Input:   input,
		Result:  result,
		IsError: isErr,
		Session: b.session,
		Cwd:     b.cwd,
	})
	return agent.HookResult{Block: out.Block, Message: out.Message, Context: out.Context}
}

// PromptSubmit fires UserPromptSubmit as a new human prompt enters the run.
// A block rejects the prompt and carries the reason; otherwise its context
// is injected for the turn.
func (b *hookBridge) PromptSubmit(ctx context.Context, prompt string) agent.HookResult {
	out := b.runner.Fire(ctx, hook.UserPromptSubmit, hook.Payload{
		Prompt:  prompt,
		Session: b.session,
		Cwd:     b.cwd,
	})
	return agent.HookResult{Block: out.Block, Message: out.Message, Context: out.Context}
}

// SessionStart fires at the start of a session and after a compaction. The
// reason is "startup" or "compact"; a start hook only contributes context.
func (b *hookBridge) SessionStart(ctx context.Context, reason string) agent.HookResult {
	out := b.runner.Fire(ctx, hook.SessionStart, hook.Payload{
		Reason:  reason,
		Session: b.session,
		Cwd:     b.cwd,
	})
	return agent.HookResult{Block: out.Block, Message: out.Message, Context: out.Context}
}

// SessionEnd fires when the run reaches its terminal reason. It cannot
// change the outcome; it is for cleanup and notification side effects.
func (b *hookBridge) SessionEnd(ctx context.Context, reason string) {
	b.runner.Fire(ctx, hook.SessionEnd, hook.Payload{
		Reason:  reason,
		Session: b.session,
		Cwd:     b.cwd,
	})
}

// Stop fires when the model finishes with no tool calls. A block asks the
// loop to keep working; the loop bounds how many times it honors a block.
func (b *hookBridge) Stop(ctx context.Context) agent.HookResult {
	out := b.runner.Fire(ctx, hook.Stop, hook.Payload{
		Session: b.session,
		Cwd:     b.cwd,
	})
	return agent.HookResult{Block: out.Block, Message: out.Message, Context: out.Context}
}

// blockTwoContext gathers the session-stable inputs for block two: the
// pinned index from the memory seam when one is wired, ARI.md, and git
// status at session start (doc 03 section 8).
func (r *Runner) blockTwoContext(ctx context.Context, card Card, window int, skills []skill.Skill) Context {
	var c Context
	if r.Memory != nil {
		if idx, err := r.Memory.PinnedIndex(ctx, card.State.Namespace); err == nil {
			c.PinnedIndex = idx
		}
	}
	// Project memory is discovered by walking the tree, not read from one
	// fixed path: ARI.md plus the honored AGENTS.md and CLAUDE.md, with
	// @-imports resolved and the per-file cap applied (D21, doc 01 section
	// 7.2). The cwd is the process's working directory; the walk stops at
	// the git root.
	cwd, _ := os.Getwd()
	c.ProjectMemory = memory.Load(memory.Options{
		Cwd:       cwd,
		Root:      r.nest.Root,
		GlobalDir: r.nest.Global,
	})
	// Only the name-plus-one-line listing of the discovered skills rides
	// block two, capped to a slice of the window so a big skill directory
	// cannot crowd out the conversation (doc 13 section 2.5). The bodies stay
	// on disk until the skill tool invokes one.
	if len(skills) > 0 {
		c.Skills, _ = skill.RenderList(skills, skill.Budget(window), estimateTokens)
	}
	status := r.GitStatus
	if status == nil {
		status = gitStatus
	}
	c.GitStatus = status(r.nest.Root)
	return c
}

// gitStatus is the default git probe: branch plus porcelain entries,
// capped so a huge dirty tree stays a summary.
func gitStatus(root string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", root, "status", "--porcelain=v1", "--branch").Output()
	if err != nil {
		return ""
	}
	text := strings.TrimSpace(string(out))
	if text == "" {
		return ""
	}
	lines := strings.Split(text, "\n")
	if len(lines) > gitStatusMaxLines {
		dropped := len(lines) - gitStatusMaxLines
		lines = append(lines[:gitStatusMaxLines], fmt.Sprintf("... and %d more entries", dropped))
	}
	return strings.Join(lines, "\n")
}

// worker is one awake ant: one loop, one context window, one file-state
// map, one tool allowlist, communicating outward only through the event
// stream (doc 01 section 6.2).
type worker struct {
	card        Card
	loop        *agent.Loop
	tc          *tool.ToolContext
	pipe        *permission.Pipeline
	session     string
	defaultMode permission.Mode
	asks        Asker
	headless    bool
	turnID      string
	mcp         *mcp.Bridge // nil when no MCP server is configured
}

// resolve blocks the pipeline's Ask on the client's answer. A headless
// run has nobody to ask, so it claims the Ask with a deny carrying
// KindHeadless: the default for a headless Ask is deny, never allow
// (doc 05 section 11). When the turn is cancelled under the prompt it
// abstains and the standing Ask becomes the refusal decide maps to a
// verdict.
func (w *worker) resolve(ctx context.Context, req *permission.Request) (permission.Resolution, bool) {
	if w.headless {
		return permission.Resolution{
			Behavior: permission.Deny,
			Kind:     permission.KindHeadless,
			Message: "this is a headless run with nobody to ask, so the call was denied; " +
				"add an allow rule for it or rerun with --mode full-auto",
		}, true
	}
	if w.asks == nil {
		return permission.Resolution{}, false
	}
	ans, err := w.asks.Wait(ctx, core.SessionID(w.session), req.ID)
	if err != nil {
		return permission.Resolution{}, false
	}
	switch ans.Decision {
	case core.Allow:
		return permission.Resolution{Behavior: permission.Allow}, true
	case core.AllowSession:
		// The suggestions the pipeline built are exactly the rules the
		// dialog showed, so "allow for session" persists those and only
		// those, in memory, for this session's pipeline alone.
		if rules, perr := permission.ParseAll(req.Suggestions, permission.LayerSession); perr == nil {
			w.pipe.AddAllow(rules...)
		}
		return permission.Resolution{Behavior: permission.Allow}, true
	default:
		return permission.Resolution{Behavior: permission.Deny, Message: "the user denied this call"}, true
	}
}

// rawJournal adapts the turn handle's pre-stamped append to the
// tool.Journal seam the pipeline and the tools write through.
type rawJournal struct {
	append func(event.Event) event.Event
}

func (j rawJournal) Append(e event.Event) event.Event { return j.append(e) }

// runTurn retargets the per-turn plumbing and runs the loop. The colony
// serializes turns per session, so mutating the worker here is
// race-free by construction.
func (w *worker) runTurn(ctx context.Context, t *core.TurnHandle) error {
	w.turnID = string(t.Turn)
	w.loop.Turn = w.turnID
	w.loop.Emit = t.Emit
	w.loop.Append = func(e session.Entry) error {
		return t.Store.Append(ctx, t.Session, e)
	}
	j := rawJournal{append: t.AppendRaw}
	w.tc.Journal = j
	w.pipe.Journal = j
	w.pipe.Mode = w.defaultMode
	if t.Request.Mode != "" {
		w.pipe.Mode = permission.Mode(t.Request.Mode)
	}
	out, err := w.loop.Run(ctx, t.Request.Text)
	t.Reason = string(out.Reason)
	return err
}

// decide adapts the doc 05 pipeline to the loop's Verdict seam. An Ask
// that still stands here means the resolver abstained: there is no
// interactive client, or the turn was cancelled under the prompt. That
// is a refusal, and the message teaches the model why.
func (w *worker) decide(ctx context.Context, tl tool.Tool, input json.RawMessage, callID string) agent.Verdict {
	d := w.pipe.Decide(ctx, permission.Call{
		Tool:    tl,
		Input:   input,
		TC:      w.tc,
		CallID:  callID,
		Session: w.session,
		Turn:    w.turnID,
		Cwd:     w.tc.Cwd,
	})
	switch d.Behavior {
	case permission.Allow:
		return agent.Verdict{Allow: true, UpdatedInput: d.UpdatedInput, Context: d.HookContext}
	case permission.Ask:
		return agent.Verdict{
			Allow:  false,
			Reason: "this call needs interactive approval and none is available in this mode: " + d.Message,
		}
	default:
		return agent.Verdict{Allow: false, Reason: d.Message}
	}
}

// modelVisibleSkill reports whether any discovered skill is reachable by the
// model, so the skill tool is only handed to it when there is something to
// invoke. A disable-model-invocation skill is user-only and does not count.
func modelVisibleSkill(skills []skill.Skill) bool {
	for i := range skills {
		if !skills[i].ModelHidden {
			return true
		}
	}
	return false
}

// skillDeps wires the skill tool to this session's discovered set and its
// permission-gated seams. The matcher and the inline runner both close over
// the worker's pipeline, so a skill's own commands face the same doc 05
// decision the model's do (doc 13 section 2.7).
func (w *worker) skillDeps(reg *tool.Registry, skills []skill.Skill) tool.SkillDeps {
	byName := make(map[string]*skill.Skill, len(skills))
	names := make([]string, 0, len(skills))
	for i := range skills {
		s := &skills[i]
		byName[s.Name] = s
		names = append(names, s.Name)
	}
	sh, _ := reg.Resolve("sh")
	return tool.SkillDeps{
		Lookup:  func(name string) (*skill.Skill, bool) { s, ok := byName[name]; return s, ok },
		Names:   func() []string { return names },
		Matcher: func(s *skill.Skill) skill.Matcher { return w.allowedToolsMatcher(sh, s.AllowedTools) },
		Inline:  w.inlineRunner(sh),
		Grant:   w.grantSkillRules,
		// Untrusted-content sessions (D19) arrive with automation in M5; the
		// flag is threaded here so the rule is enforced the moment it exists.
		Trusted: true,
	}
}

// allowedToolsMatcher builds gate one of the inline-shell pass: a command is
// permitted only when one of the skill's declared sh rules covers it, using
// the same sh normalization the pipeline uses, so a skill cannot smuggle a
// second command past a narrow allowance (doc 13 section 2.7).
func (w *worker) allowedToolsMatcher(sh tool.Tool, allowed []string) skill.Matcher {
	rules, err := permission.ParseAll(allowed, permission.LayerSession)
	if err != nil || sh == nil {
		return func(string) bool { return false }
	}
	return func(cmd string) bool {
		input, err := shInput(cmd)
		if err != nil {
			return false
		}
		pm := sh.MatchPrefix(input)
		for _, r := range rules {
			if r.Pattern.Tool == "sh" && pm.Matches(r.Pattern) {
				return true
			}
		}
		return false
	}
}

// inlineRunner is gate two: it submits an inline command as a normal sh call
// so every stage of doc 05, the bypass-immune safety floor included, still
// decides before anything runs. A denied or ask decision returns an error, so
// the inline pass leaves the command as literal text.
func (w *worker) inlineRunner(sh tool.Tool) skill.InlineRunner {
	if sh == nil {
		return nil
	}
	return func(ctx context.Context, cmd string) (string, error) {
		input, err := shInput(cmd)
		if err != nil {
			return "", err
		}
		d := w.pipe.Decide(ctx, permission.Call{
			Tool:    sh,
			Input:   input,
			TC:      w.tc,
			CallID:  "skill-inline",
			Session: w.session,
			Turn:    w.turnID,
			Cwd:     w.tc.Cwd,
		})
		if d.Behavior != permission.Allow {
			return "", fmt.Errorf("the permission pipeline did not allow it: %s", d.Message)
		}
		in := input
		if d.UpdatedInput != nil {
			in = d.UpdatedInput
		}
		res, err := sh.Call(ctx, in, w.tc, nil)
		if err != nil {
			return "", err
		}
		return res.Model, nil
	}
}

// grantSkillRules applies a skill's allowed-tools as session allow rules, so
// the skill's own commands pass the pipeline without a prompt for each one.
// They narrow what the skill may ask for; they never widen the session, since
// the deny and safety stages still run ahead of the allow stage (doc 13
// section 2.5).
func (w *worker) grantSkillRules(_ string, rules []string) {
	parsed, err := permission.ParseAll(rules, permission.LayerSession)
	if err != nil {
		return
	}
	w.pipe.AddAllow(parsed...)
}

// shInput builds the sh tool's argument object for one command.
func shInput(cmd string) (json.RawMessage, error) {
	return json.Marshal(map[string]string{"command": cmd})
}
