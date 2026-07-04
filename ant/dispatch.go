package ant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/session"
)

// DispatchResult is what an executed fan-out plan produced: the findings its
// read-only surveys posted, and, when the plan carried writers, the reconcile
// outcome of composing their patches into one diff or the exact conflict (doc
// 09 sections 5 and 6.4). Reconcile is nil for a read-only plan.
type DispatchResult struct {
	Findings  []colony.Finding
	Reconcile *colony.ReconcileResult
}

// dispatch runs an approved fan-out plan: it posts each subtask to the
// blackboard as an open goal, spawns a forager per subtask to claim and run
// them in parallel, then gathers the findings they posted. It is the execution
// half of the D5 fan-out gate: FanOutGate decides a split is worth the tokens,
// and this turns that decision into work actually running (doc 09 section 5).
//
// Each subtask carries the parent brief's trust labels, because a subtask can
// never shed a label its parent's context held (doc 09 section 12.2). The graph
// edge back to the parent lands with the reconcile slice; here the trust rides
// on the row directly so a labeled request cannot launder its labels through a
// fan-out.
func (r *Runner) dispatch(ctx context.Context, store session.Store, sessionID core.SessionID, turn core.TurnID, parent colony.TaskBrief, plan *colony.FanOutPlan) (DispatchResult, error) {
	childTasks := make([]string, 0, len(plan.Subtasks))
	for _, s := range plan.Subtasks {
		if _, err := r.board.Post(ctx, colony.Entry{
			SessionID: string(sessionID),
			TaskID:    s.TaskID,
			Origin:    colony.OriginBlackboard,
			Goal:      s.Goal,
			Payload:   s,
			Trust:     parent.Labels,
		}); err != nil {
			return DispatchResult{}, fmt.Errorf("posting subtask %s: %w", s.TaskID, err)
		}
		childTasks = append(childTasks, s.TaskID)
	}

	// Writers branch from one shared base so their diffs compose. It is
	// resolved once, before any forager runs, so every sibling worktree checks
	// out the same commit and reconcile replays them against that same base. A
	// read-only plan skips this entirely and shares the live tree.
	var baseRef string
	if planMutates(plan) {
		ref, err := r.headRef(ctx)
		if err != nil {
			return DispatchResult{}, fmt.Errorf("resolving the base commit for a writer fan-out: %w", err)
		}
		baseRef = ref
	}

	// One forager per subtask. A forager claims and runs until the board is
	// dry, so spawning as many as there are subtasks is an upper bound: the
	// compare-and-swap claim guarantees no two run the same goal, and a forager
	// that finds nothing left simply returns. A crashed subtask fails its own
	// claim and does not abort its siblings, so the plan makes what progress it
	// can and the errors are joined for the caller.
	var wg sync.WaitGroup
	var mu sync.Mutex
	var errs []error
	for i := range plan.Subtasks {
		antID := fmt.Sprintf("forager-%d", i)
		wg.Go(func() {
			if err := r.forage(ctx, store, sessionID, turn, antID, baseRef); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	var findings []colony.Finding
	for _, task := range childTasks {
		fs, err := r.board.Findings(ctx, task)
		if err != nil {
			return DispatchResult{}, fmt.Errorf("reading findings for %s: %w", task, err)
		}
		findings = append(findings, fs...)
	}

	rec, rerr := r.reconcile(ctx, sessionID, baseRef, plan)
	if rerr != nil {
		errs = append(errs, rerr)
	}
	return DispatchResult{Findings: findings, Reconcile: rec}, errors.Join(errs...)
}

// planMutates reports whether any subtask in the plan writes, which is what
// decides whether the fan-out needs a shared base and worktrees at all. A
// read-only plan races nothing and touches nothing, so it skips the whole
// worktree machinery (doc 09 section 6.3).
func planMutates(plan *colony.FanOutPlan) bool {
	for _, s := range plan.Subtasks {
		if colony.NeedsWorktree(s.Deliverable) {
			return true
		}
	}
	return false
}

// headRef resolves the repository HEAD, the commit every writer in a fan-out
// branches from so their diffs share a base and reconcile can compose them.
func (r *Runner) headRef(ctx context.Context) (string, error) {
	out, err := (gitRunner{}).Run(ctx, r.nest.Root, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("git rev-parse HEAD: %w", err)
	}
	return strings.TrimSpace(out), nil
}

// reconcile composes the writers' patches into one diff or the exact conflict.
// It gathers the Patch each writer subtask posted, pairs each with the ordering
// keys reconcile needs from its brief, and replays them onto a throwaway
// checkout at the shared base. A read-only plan has no base and no patches, so
// it returns nil and reconcile never runs (doc 09 section 6.4).
func (r *Runner) reconcile(ctx context.Context, sessionID core.SessionID, baseRef string, plan *colony.FanOutPlan) (*colony.ReconcileResult, error) {
	if baseRef == "" {
		return nil, nil
	}
	var patches []colony.ReconcilePatch
	for _, s := range plan.Subtasks {
		if !colony.NeedsWorktree(s.Deliverable) {
			continue
		}
		ps, err := r.board.Patches(ctx, s.TaskID)
		if err != nil {
			return nil, fmt.Errorf("reading patches for %s: %w", s.TaskID, err)
		}
		for _, p := range ps {
			patches = append(patches, colony.ReconcilePatch{Patch: p, DependsOn: s.DependsOn})
		}
	}
	if len(patches) == 0 {
		return nil, nil
	}
	res, err := r.worktrees.Reconcile(ctx, string(sessionID), baseRef, patches, nil, r.journal)
	if err != nil {
		return nil, fmt.Errorf("reconciling %d patches: %w", len(patches), err)
	}
	return &res, nil
}

// forage is one background worker's claim loop: take an open goal, run it to a
// handoff, repeat until nothing is left. It is deliberately generic, not bound
// to a card, because the ant it embodies is chosen per claim from the brief it
// took, which is how the same idle worker can run a survey subtask on one claim
// and a different one on the next (doc 09 section 5).
func (r *Runner) forage(ctx context.Context, store session.Store, sessionID core.SessionID, turn core.TurnID, antID, baseRef string) error {
	for {
		entry, ok, err := r.board.Claim(ctx, antID, colony.ClaimFilter{SessionID: string(sessionID)})
		if err != nil {
			return fmt.Errorf("claiming a subtask: %w", err)
		}
		if !ok {
			return nil
		}
		brief, ok := entry.Payload.(colony.TaskBrief)
		if !ok {
			_ = r.board.Fail(ctx, entry.ID)
			return fmt.Errorf("claimed goal %s did not carry a task brief", entry.ID)
		}
		if err := r.runSubtask(ctx, store, sessionID, turn, antID, entry.ID, brief, baseRef); err != nil {
			return err
		}
	}
}

// runSubtask embodies the ant a claimed brief calls for and runs it to a
// handoff. The card is the brief's DirectedTo when the decomposer named one,
// otherwise the queen routes it like any other task, so a subtask faces the
// same routing a foreground request does. The detachment posts its own result
// and completes the claim; a run error leaves the claim failed for the board to
// see (doc 09 sections 5 and 5.1).
func (r *Runner) runSubtask(ctx context.Context, store session.Store, sessionID core.SessionID, turn core.TurnID, worker, claimID string, brief colony.TaskBrief, baseRef string) error {
	antID := brief.DirectedTo
	if antID == "" {
		a, err := r.queen.Assign(ctx, brief)
		if err != nil {
			return fmt.Errorf("routing subtask %s: %w", brief.TaskID, err)
		}
		antID = a.Ant
	}
	card, err := r.cards.Load(ctx, antID)
	if err != nil {
		return fmt.Errorf("loading card %s for subtask %s: %w", antID, brief.TaskID, err)
	}
	chain, err := r.registry.Resolve(string(card.Tier))
	if err != nil {
		return fmt.Errorf("resolving tier %s for %s: %w", card.Tier, antID, err)
	}

	// A worker announces itself alive before it runs, so the colony view is
	// never wrong about who is awake (doc 09, doc 02 section 10.5, D18). The
	// forager lane is the ant's identity here, not the card it embodies, so two
	// siblings running the same card show as two live ants rather than collapsing
	// into one row. It rides the must-deliver lane through r.emit, the same
	// stream the fan-out decision rides.
	_ = r.emit(event.TypeWorkerWoke, string(sessionID), string(turn), event.WorkerWoke{
		Ant:  worker,
		Task: brief.TaskID,
		Tier: string(card.Tier),
		File: sidechainFile(card.ID, brief.TaskID),
	})

	// A writer runs in its own detached worktree at the shared base, so two
	// siblings editing the same tree cannot corrupt each other and each one's
	// output is a clean diff against that base. A reader shares the live tree,
	// which it only ever reads (doc 09 sections 6.2 and 6.3). The worktree is
	// torn down after the run under a cancel-immune context, so a cancelled
	// fan-out still cleans up its checkouts; the diff was already captured into
	// the Patch handoff before teardown.
	cwd := r.nest.Root
	ref := ""
	if colony.NeedsWorktree(brief.Deliverable) {
		wt, werr := r.worktrees.Create(ctx, brief.TaskID, baseRef)
		if werr != nil {
			return fmt.Errorf("worktree for subtask %s: %w", brief.TaskID, werr)
		}
		defer func() { _ = r.worktrees.Remove(context.WithoutCancel(ctx), wt) }()
		cwd = wt.Dir
		ref = baseRef
	}

	d, err := NewDetachment(DetachConfig{
		Card:     card,
		Brief:    brief,
		ClaimID:  claimID,
		Parent:   sessionID,
		Board:    r.board,
		Store:    store,
		Provider: chain[0].Provider,
		Model:    chain[0].Model,
		Ledger:   r.ledger,
		Cwd:      cwd,
		BaseRef:  ref,
	})
	if err != nil {
		return fmt.Errorf("building the worker for subtask %s: %w", brief.TaskID, err)
	}
	runErr := d.Run(ctx)

	// The worker's final spend rides the lossy lane: a dropped tick is not a
	// correctness problem and the finish that follows supersedes it, but when it
	// lands the view shows the tokens to the number the ledger metered (D18). The
	// finish then settles the lane on the must-deliver lane so a done worker is
	// never left showing awake.
	_ = r.emit(event.TypeColonyProgress, string(sessionID), string(turn), event.ColonyProgress{
		Ant:    worker,
		Task:   brief.TaskID,
		Tokens: d.Tokens(),
	})
	_ = r.emit(event.TypeWorkerFinished, string(sessionID), string(turn), event.WorkerFinished{
		Ant:  worker,
		Task: brief.TaskID,
		OK:   runErr == nil,
	})

	r.recordTrail(ctx, card, brief, d.Tokens(), runErr == nil)
	return runErr
}

// recordTrail folds one finished subtask into the ant's trail: the class it
// ran, whether it succeeded, and what it cost. This is the fitness feedback the
// queen routes on and the fan-out cost model projects against, so a colony that
// runs surveys learns what a survey costs and which ant is good at them (doc 06
// sections 4 and 5.3). It is best-effort bookkeeping: a trail write failure
// never fails the task the worker already finished.
func (r *Runner) recordTrail(ctx context.Context, card colony.Card, brief colony.TaskBrief, tokens int64, success bool) {
	if r.trails == nil || brief.Class == "" {
		return
	}
	_ = r.trails.Update(ctx, colony.Outcome{
		Ant:     card.ID,
		Class:   brief.Class,
		Success: success,
		Tokens:  tokens,
		Embed:   brief.Embed,
	})
}
