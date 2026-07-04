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
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/lsp"
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

	registry *provider.Registry
	ledger   *ledger.Ledger
	config   *coreConfig
	nest     nest.Nest
	asks     Asker
	lsp      *lsp.Service
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
	r.bound = true
}

// Close tears down the runner's background resources. The colony calls it
// on shutdown through the optional io.Closer seam, so a spawned language
// server does not outlive the session.
func (r *Runner) Close() error {
	r.mu.Lock()
	svc := r.lsp
	r.mu.Unlock()
	if svc != nil {
		svc.Shutdown()
	}
	return nil
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

	tc := &tool.ToolContext{
		Cwd:   r.nest.Root,
		Files: tool.NewFileState(),
		Ant:   tool.AntID(card.ID),
		Spill: tool.NewDiskSpill(filepath.Join(r.nest.ProjectStateDir(), "spill")),
		LSP:   r.lsp,
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

	w.loop = &agent.Loop{
		Provider: primary.Provider,
		Model:    primary.Model,
		Fallback: fallback,
		System: SystemPrompt(Env{
			Cwd:      r.nest.Root,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Model:    primary.Model,
		}),
		Prefix:  []provider.Message{BlockTwo(r.blockTwoContext(ctx, card, primary.Provider.Caps().MaxContext, skills))},
		Tools:   reg,
		TC:      tc,
		Decide:  w.decide,
		Record:  r.ledger.Record,
		Session: string(t.Session),
		Tier:    string(card.Tier),
	}
	return w, nil
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
		return agent.Verdict{Allow: true, UpdatedInput: d.UpdatedInput}
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
