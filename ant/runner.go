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
	"github.com/tamnd/ari/nest"
	"github.com/tamnd/ari/permission"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
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
	bound    bool

	mu      sync.Mutex
	workers map[core.SessionID]*worker
}

// coreConfig is the slice of config the runner reads, so the field list
// documents the dependency instead of hiding it behind the whole struct.
type coreConfig struct {
	mode string
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
	r.config = &coreConfig{mode: c.Config().Mode}
	r.nest = c.Nest()
	r.asks = c.Asks()
	r.bound = true
}

// Headless drops the interactive resolver, so every Ask that reaches
// the pipeline stands and the loop refuses the call instead of blocking
// on a prompt nobody can see (doc 05 section 3).
func (r *Runner) Headless() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.asks = nil
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

	tc := &tool.ToolContext{
		Cwd:   r.nest.Root,
		Files: tool.NewFileState(),
		Ant:   tool.AntID(card.ID),
		Spill: tool.NewDiskSpill(filepath.Join(r.nest.ProjectStateDir(), "spill")),
	}

	home, _ := os.UserHomeDir()
	self, _ := os.Executable()
	if resolved, rerr := filepath.EvalSymlinks(self); rerr == nil && resolved != "" {
		self = resolved
	}
	pipe := &permission.Pipeline{
		Mode: permission.Mode(r.config.mode),
		Paths: permission.Paths{
			Root:         r.nest.Root,
			Nest:         r.nest.ProjectDir(),
			GlobalNest:   r.nest.Global,
			Home:         home,
			AriBinary:    self,
			GlobalConfig: r.nest.GlobalConfig(),
		},
	}

	w := &worker{
		card:        card,
		tc:          tc,
		pipe:        pipe,
		session:     string(t.Session),
		defaultMode: permission.Mode(r.config.mode),
		asks:        r.asks,
	}
	pipe.Resolver = permission.ResolverFunc(w.resolve)
	w.loop = &agent.Loop{
		Provider: primary.Provider,
		Model:    primary.Model,
		Fallback: fallback,
		System: SystemPrompt(Env{
			Cwd:      r.nest.Root,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Model:    primary.Model,
		}),
		Prefix:  []provider.Message{BlockTwo(r.blockTwoContext(ctx, card))},
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
func (r *Runner) blockTwoContext(ctx context.Context, card Card) Context {
	var c Context
	if r.Memory != nil {
		if idx, err := r.Memory.PinnedIndex(ctx, card.State.Namespace); err == nil {
			c.PinnedIndex = idx
		}
	}
	if data, err := os.ReadFile(r.nest.ARIMD()); err == nil {
		c.ProjectMemory = strings.TrimSpace(string(data))
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
	turnID      string
}

// resolve blocks the pipeline's Ask on the client's answer. With no
// asker, or when the turn is cancelled under the prompt, it abstains
// and the standing Ask becomes the refusal decide maps to a verdict.
func (w *worker) resolve(ctx context.Context, req *permission.Request) (permission.Resolution, bool) {
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
	_, err := w.loop.Run(ctx, t.Request.Text)
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
