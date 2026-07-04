package ant

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/tamnd/ari/agent"
	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
)

// workerMaxTurns bounds a colony worker's loop. A background worker gets a
// tighter ceiling than the foreground's hundred because its job is one briefed
// subtask, not an open-ended conversation (doc 09 section 5).
const workerMaxTurns = 32

// Detachment is one colony worker: the M0 agent loop run under a restricted
// contract (doc 09 section 5). The restriction has three structural parts. Its
// tools are its card's allowlist and nothing more, enforced by never
// registering the rest, so a surveyor without write in its card cannot mutate a
// file even if its model decides to. Its context is seeded from its TaskBrief,
// not the foreground conversation, so it never inherits chat history it has no
// business seeing. Its output channel is the blackboard: a worker's final act
// is a typed handoff, never free text to the user, so colony output stays
// scannable instead of a wall of sub-agent chatter.
type Detachment struct {
	card    colony.Card
	brief   colony.TaskBrief
	claimID string
	parent  session.ID
	board   colony.Blackboard
	store   session.Store
	loop    *agent.Loop
	file    string
	cwd     string // where the worker ran; the worktree for a patch task
	baseRef string // the ref a patch is diffed against, "" defaults to HEAD

	lastText string // the most recent assistant text, harvested into the handoff
	tokens   int64  // input+output tokens summed across the worker's turns
}

// DetachConfig is what building a colony worker needs: the card it runs under,
// the claimed brief, the goal row id to complete or fail, the spawning session
// its sidechain nests beneath, the board it posts to, the store it writes its
// transcript to, and the provider and model its loop drives.
type DetachConfig struct {
	Card     colony.Card
	Brief    colony.TaskBrief
	ClaimID  string
	Parent   session.ID
	Board    colony.Blackboard
	Store    session.Store
	Provider provider.Provider
	Model    string
	// Ledger meters the worker's model turns. A background worker is metered
	// the same as the foreground (D22): nil skips the meter, and the runner
	// wires the colony's one ledger so a fan-out's cost lands on the same books
	// as everything else.
	Ledger *ledger.Ledger
	Cwd    string
	// BaseRef is the ref a patch deliverable is diffed against, the commit the
	// worker's worktree branched from. Empty defaults to HEAD, which is the
	// worktree's own base after Create. It is unused for a finding.
	BaseRef string
}

// NewDetachment builds a colony worker over the six core tools narrowed to the
// card's allowlist. A tool absent from the allowlist is never registered, so it
// is invisible to the model and physically uncallable: the restriction is
// structural, applied before a tool could run, not trusted to the prompt (doc
// 09 section 5, doc 06 the card's Tools as a hard filter).
func NewDetachment(cfg DetachConfig) (*Detachment, error) {
	reg := tool.NewRegistry()
	for _, tl := range []tool.Tool{
		tool.NewRead(), tool.NewFind(), tool.NewWrite(),
		tool.NewEdit(), tool.NewSh(), tool.NewFetch(),
	} {
		if err := reg.Register(tl); err != nil {
			return nil, fmt.Errorf("registering core tools: %w", err)
		}
	}
	reg = reg.ForAllowlist(cfg.Card.Tools)

	d := &Detachment{
		card:    cfg.Card,
		brief:   cfg.Brief,
		claimID: cfg.ClaimID,
		parent:  cfg.Parent,
		board:   cfg.Board,
		store:   cfg.Store,
		file:    cfg.Card.ID + "." + cfg.Brief.TaskID,
		cwd:     cfg.Cwd,
		baseRef: cfg.BaseRef,
	}
	d.loop = &agent.Loop{
		Provider: cfg.Provider,
		Model:    cfg.Model,
		System: SystemPrompt(Env{
			Cwd:      cfg.Cwd,
			Platform: runtime.GOOS + "/" + runtime.GOARCH,
			Model:    cfg.Model,
		}),
		Tools: reg,
		TC: &tool.ToolContext{
			Cwd:       cfg.Cwd,
			Files:     tool.NewFileState(),
			Ant:       tool.AntID(cfg.Card.ID),
			Namespace: cfg.Card.State.Namespace,
		},
		Session: string(cfg.Parent),
		Tier:    string(cfg.Card.Tier),
		Limits:  agent.Limits{MaxTurns: workerMaxTurns},
	}
	// Metering rides the loop's per-turn Record seam. The wrapper both forwards
	// the row to the colony ledger and sums the turn's tokens onto the worker,
	// so the runner can fold the total into the ant's trail when the task ends.
	// The loop drives one detachment on one goroutine, so the running sum needs
	// no lock; Tokens is read only after Run returns.
	if cfg.Ledger != nil {
		d.loop.Record = func(row ledger.Row) {
			d.tokens += int64(row.Usage.Input) + int64(row.Usage.Output)
			cfg.Ledger.Record(row)
		}
	}
	return d, nil
}

// Tokens reports the input-plus-output tokens the worker spent across its
// turns, the cost the runner folds into the ant's trail fitness. It is valid
// after Run returns.
func (d *Detachment) Tokens() int64 { return d.tokens }

// Run drives the worker to a terminal reason and reduces its result to a typed
// handoff. A clean finish posts the handoff and marks the claim done; anything
// else (a run error, a cancellation, a non-completed terminal reason, or a
// panic) marks the claim incomplete and leaves the partial sidechain intact.
// The board writes use a cancel-immune context so a cancelled worker can still
// record that it failed rather than vanishing (doc 09 sections 5.1 and 11.1).
func (d *Detachment) Run(ctx context.Context) (err error) {
	defer func() {
		if r := recover(); r != nil {
			_ = d.board.Fail(context.WithoutCancel(ctx), d.claimID)
			err = fmt.Errorf("worker %s panicked on task %s: %v", d.card.ID, d.brief.TaskID, r)
		}
	}()

	if oerr := d.openSidechain(ctx); oerr != nil {
		return oerr
	}

	// Every transcript line goes to the sidechain, never the foreground
	// session, so a user resuming the main session pays nothing for worker
	// chatter (D9). The assistant text is remembered for the handoff.
	d.loop.Append = func(e session.Entry) error {
		if e.Type == session.EntryAnt {
			var b agent.AntBody
			if json.Unmarshal(e.Body, &b) == nil && b.Text != "" {
				d.lastText = b.Text
			}
		}
		return d.store.AppendSidechain(ctx, d.parent, d.file, e)
	}

	out, runErr := d.loop.Run(ctx, d.prompt())
	if runErr != nil || ctx.Err() != nil || out.Reason != agent.TermCompleted {
		if ferr := d.board.Fail(context.WithoutCancel(ctx), d.claimID); ferr != nil {
			return fmt.Errorf("worker %s stopped and could not mark its claim incomplete: %w", d.card.ID, ferr)
		}
		return runErr
	}

	result, herr := d.harvest()
	if herr != nil {
		_ = d.board.Fail(context.WithoutCancel(ctx), d.claimID)
		return fmt.Errorf("worker %s could not build its result handoff: %w", d.card.ID, herr)
	}
	if _, cerr := d.board.Complete(context.WithoutCancel(ctx), d.claimID, result); cerr != nil {
		return fmt.Errorf("worker %s could not post its result: %w", d.card.ID, cerr)
	}
	return nil
}

// openSidechain writes the first line of the worker's transcript: a meta entry
// pointing at the spawning session, so the file is self-describing and slice
// 16 can drill into it from the foreground without guessing its parent.
func (d *Detachment) openSidechain(ctx context.Context) error {
	body, err := json.Marshal(session.Meta{
		Title:   fmt.Sprintf("worker %s on task %s", d.card.ID, d.brief.TaskID),
		Parent:  d.parent,
		Created: time.Now().UTC(),
	})
	if err != nil {
		return err
	}
	return d.store.AppendSidechain(ctx, d.parent, d.file, session.Entry{
		ID:   session.NewID(),
		Type: session.EntryMeta,
		Time: time.Now().UTC(),
		Body: body,
	})
}

// prompt seeds the worker from its brief and nothing else: the goal, then its
// constraints and the context refs it should read on its own with its own
// tools. The refs are named, not inlined, so the brief stays small and the
// worker reads lazily (doc 09 section 3.2).
func (d *Detachment) prompt() string {
	var b strings.Builder
	b.WriteString(d.brief.Goal)
	if len(d.brief.Constraints) > 0 {
		b.WriteString("\n\nConstraints:")
		for _, c := range d.brief.Constraints {
			b.WriteString("\n- ")
			b.WriteString(c)
		}
	}
	if len(d.brief.Context) > 0 {
		b.WriteString("\n\nContext to read:")
		for _, r := range d.brief.Context {
			if r.Path != "" {
				b.WriteString("\n- ")
				b.WriteString(r.Path)
			}
		}
	}
	return b.String()
}

// harvest reduces the finished run to the typed handoff the brief asked for.
// The worker's result is a Finding or a Patch, never the transcript that
// produced it, which is the reduction that keeps ants from drowning in each
// other's context (doc 09 section 3.2). A patch is the diff the worker left in
// its isolated worktree; reconcile replays it against the live tree later.
func (d *Detachment) harvest() (colony.Handoff, error) {
	hdr := colony.Header{
		ID:        "h-" + session.NewID(),
		Kind:      d.brief.Deliverable,
		From:      d.card.ID,
		TaskID:    d.brief.TaskID,
		SessionID: string(d.parent),
		Labels:    d.brief.Labels,
	}
	switch d.brief.Deliverable {
	case colony.KindFinding:
		evidence := briefCitations(d.brief)
		if len(evidence) == 0 {
			return nil, fmt.Errorf("a finding needs at least one cited anchor and the brief carried none")
		}
		summary := d.lastText
		if summary == "" {
			summary = d.brief.Goal
		}
		return colony.Finding{
			Header:     hdr,
			Summary:    summary,
			Evidence:   evidence,
			Confidence: 0.5,
		}, nil
	case colony.KindPatch:
		return d.harvestPatch(hdr)
	default:
		return nil, fmt.Errorf("deliverable %q has no harvest path", d.brief.Deliverable)
	}
}

// harvestPatch captures the diff the worker left in its worktree. It stages
// everything first so new and deleted files land in the diff, then diffs the
// index against the base ref. An empty diff is a failure, not a no-op patch: a
// worker briefed to produce a change that touched nothing did not do its job,
// and the board should see the claim fail rather than reconcile a no-op.
func (d *Detachment) harvestPatch(hdr colony.Header) (colony.Handoff, error) {
	if d.cwd == "" {
		return nil, fmt.Errorf("a patch needs a worktree and the detachment ran with no cwd")
	}
	base := d.baseRef
	if base == "" {
		base = "HEAD"
	}
	ctx := context.Background()
	git := gitRunner{}
	if _, err := git.Run(ctx, d.cwd, "git", "add", "-A"); err != nil {
		return nil, fmt.Errorf("staging the worktree: %w", err)
	}
	diff, err := git.Run(ctx, d.cwd, "git", "diff", "--cached", base)
	if err != nil {
		return nil, fmt.Errorf("diffing the worktree against %s: %w", base, err)
	}
	if strings.TrimSpace(diff) == "" {
		return nil, fmt.Errorf("worker %s produced no changes for patch task %s", d.card.ID, d.brief.TaskID)
	}
	return colony.Patch{
		Header:   hdr,
		Worktree: d.cwd,
		BaseRef:  base,
		Diff:     diff,
		Notes:    d.lastText,
	}, nil
}

// briefCitations turns the brief's context refs and file anchors into the
// citations a Finding owes: a surveyor cites the places it was pointed at, so
// its answer is grounded in the repo rather than free-floating (D11).
func briefCitations(b colony.TaskBrief) []colony.Citation {
	var out []colony.Citation
	for _, r := range b.Context {
		if r.Path != "" {
			out = append(out, colony.Citation{Path: r.Path, Lines: r.Lines})
		}
	}
	for _, a := range b.Anchors {
		if a.Kind == colony.AnchorFile && a.Value != "" {
			out = append(out, colony.Citation{Path: a.Value})
		}
	}
	return out
}
