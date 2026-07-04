package ant

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/session"
)

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
func (r *Runner) dispatch(ctx context.Context, store session.Store, sessionID core.SessionID, parent colony.TaskBrief, plan *colony.FanOutPlan) ([]colony.Finding, error) {
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
			return nil, fmt.Errorf("posting subtask %s: %w", s.TaskID, err)
		}
		childTasks = append(childTasks, s.TaskID)
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
			if err := r.forage(ctx, store, sessionID, antID); err != nil {
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
			return nil, fmt.Errorf("reading findings for %s: %w", task, err)
		}
		findings = append(findings, fs...)
	}
	return findings, errors.Join(errs...)
}

// forage is one background worker's claim loop: take an open goal, run it to a
// handoff, repeat until nothing is left. It is deliberately generic, not bound
// to a card, because the ant it embodies is chosen per claim from the brief it
// took, which is how the same idle worker can run a survey subtask on one claim
// and a different one on the next (doc 09 section 5).
func (r *Runner) forage(ctx context.Context, store session.Store, sessionID core.SessionID, antID string) error {
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
		if err := r.runSubtask(ctx, store, sessionID, entry.ID, brief); err != nil {
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
func (r *Runner) runSubtask(ctx context.Context, store session.Store, sessionID core.SessionID, claimID string, brief colony.TaskBrief) error {
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
	d, err := NewDetachment(DetachConfig{
		Card:     card,
		Brief:    brief,
		ClaimID:  claimID,
		Parent:   sessionID,
		Board:    r.board,
		Store:    store,
		Provider: chain[0].Provider,
		Model:    chain[0].Model,
		Cwd:      r.nest.Root,
	})
	if err != nil {
		return fmt.Errorf("building the worker for subtask %s: %w", brief.TaskID, err)
	}
	return d.Run(ctx)
}
