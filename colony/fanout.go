package colony

import (
	"context"
	"slices"
)

// Fan-out is the decision to route one task to several ants, and D5 is
// unambiguous that it is the exception the queen must argue for, not the
// default she reaches for. This gate is that argument. It runs three tests in
// cheapest-first order and short-circuits on the first failure, because a
// refusal must cost almost nothing: it produces an ordinary single-ant
// assignment and writes nothing to the journal. Only an approval is loud, and
// it carries the full argument so a human auditing why the colony spent
// fifteen times the tokens here finds a recorded case, not a shrug (doc 06
// section 5, research/memory_swarm.md section 8).

// The evidence strings recorded in a FanOutArg. Keeping them named rather than
// inline keeps the journal vocabulary a closed set a replay can assert against.
const (
	independenceDisjoint = "disjoint-anchors"
	independenceDeclared = "declared-independent"
	workloadReadHeavy    = "read-heavy"
	workloadSpecialist   = "specialist-advantage"
)

// FanOutArg is the journaled justification for a split (doc 06 section 5,
// referenced by section 4's RoutingReason.FanOut). It exists only when the
// gate approved; a refusal writes nothing, per D5's cheap-and-silent rule. The
// evidence fields record which limb of tests one and two carried, and the
// budget fields record the projection the gate weighed so a later milestone
// can calibrate the crude cost model against the eventual actual.
type FanOutArg struct {
	IndependenceBy string // "disjoint-anchors" | "declared-independent"
	Workload       string // "read-heavy" | "specialist-advantage"
	Specialist     string // set when Workload is specialist-advantage
	Subtasks       int    // fan width
	Projected      int64  // estimated total tokens across subtasks
	Remaining      int64  // remaining session budget at decision time
}

// FanOutPlan is the approved split: the subtasks to run in parallel and the
// argument that let them past the gate. A routing decision points at one only
// on approval; a refusal leaves it nil and the task runs single-ant.
type FanOutPlan struct {
	Subtasks []TaskBrief
	Arg      FanOutArg
}

// FanOutGate decides whether a brief may be split across the proposed
// subtasks. All three tests must pass. It returns the approved plan on success
// and nil on refusal, and refusal is silent: the caller simply routes the
// undivided brief to one ant. remaining is the session's remaining token
// budget, which the ledger owns (slice 14); the gate only reads it.
//
// The gate never issues a model reasoning turn. Every test is a deterministic
// function of the briefs, the cards, and the trail table's running averages,
// because a gate that had to think would be a second, slower router with its
// own failure surface.
func (q *Queen) FanOutGate(ctx context.Context, sub []TaskBrief, remaining int64) *FanOutPlan {
	if len(sub) < 2 {
		// Nothing to fan out: a single subtask is a single-ant task.
		return nil
	}
	indBy, ok := independent(sub)
	if !ok {
		return nil
	}
	workload, specialist, ok := q.readHeavyOrSpecialist(ctx, sub)
	if !ok {
		return nil
	}
	projected, ok := q.project(ctx, sub)
	if !ok {
		// The cost model has no history for some class, so the estimate is
		// uncertain and D5's posture is to refuse rather than gamble the budget.
		return nil
	}
	projected += q.cfg.CoordinationOverhead * int64(len(sub))
	if projected > remaining {
		return nil
	}
	return &FanOutPlan{
		Subtasks: sub,
		Arg: FanOutArg{
			IndependenceBy: indBy,
			Workload:       workload,
			Specialist:     specialist,
			Subtasks:       len(sub),
			Projected:      projected,
			Remaining:      remaining,
		},
	}
}

// independent is test one: the subtasks do not depend on each other and no two
// of them would write the same file. Output-as-input is a hard fail, and
// write-write file overlap is the merge conflict the colony exists to avoid.
// Overlapping reads are fine (D6), so only mutating subtasks constrain each
// other, and a mutating subtask with no file anchors is too thin to prove
// disjoint, which fails conservatively because a wrong independence call is
// how parallel ants stomp each other.
func independent(sub []TaskBrief) (string, bool) {
	ids := map[string]bool{}
	for _, s := range sub {
		if s.ID != "" {
			ids[s.ID] = true
		}
		if s.TaskID != "" {
			ids[s.TaskID] = true
		}
	}
	for _, s := range sub {
		for _, dep := range s.DependsOn {
			if ids[dep] {
				// One subtask's output is another's input: not independent.
				return "", false
			}
		}
	}

	var writers []TaskBrief
	for _, s := range sub {
		if mutates(s) {
			writers = append(writers, s)
		}
	}
	for _, w := range writers {
		if len(fileAnchors(w)) == 0 {
			return "", false
		}
	}
	for i := range writers {
		for j := i + 1; j < len(writers); j++ {
			if anchorsOverlap(writers[i], writers[j]) {
				return "", false
			}
		}
	}
	if len(writers) == 0 {
		// No subtask mutates, so there is nothing to conflict: independence is
		// by declaration, not by anchor arithmetic.
		return independenceDeclared, true
	}
	return independenceDisjoint, true
}

// readHeavyOrSpecialist is test two, a disjunction: the work is read-heavy, or
// some subtask has a real specialist to route to. Either passing is enough,
// because they are two different reasons fan-out pays. What fails is the common
// bad case, an interdependent write task with no standout specialist, which is
// the chatty-committee shape that stays single-ant.
func (q *Queen) readHeavyOrSpecialist(ctx context.Context, sub []TaskBrief) (string, string, bool) {
	writers := 0
	for _, s := range sub {
		if mutates(s) {
			writers++
		}
	}
	if writers == 0 {
		// Dominated by reads: parallel readers beat one reader N times over.
		return workloadReadHeavy, "", true
	}

	rows, err := q.cards.List(ctx)
	if err != nil {
		return "", "", false
	}
	for _, s := range sub {
		for _, r := range rows {
			if r.Status == StatusArchived {
				continue
			}
			if !slices.Contains(r.Prefers, s.Class) {
				continue
			}
			if q.specialistEdge(ctx, r.ID, s.Class) {
				return workloadSpecialist, r.ID, true
			}
		}
	}
	return "", "", false
}

// specialistEdge reports whether a card that declares a class in its Prefers
// set actually carries the fitness to back the claim: a decayed success rate
// on that class that clears the neutral prior by the configured margin. A
// declared preference with no proven edge is not enough, because the point of
// the specialist limb is a real memory advantage, not a self-description. With
// no trail store wired the edge cannot be proven, so it is denied.
func (q *Queen) specialistEdge(ctx context.Context, ant string, class TaskClass) bool {
	if q.trails == nil {
		return false
	}
	t, err := q.trails.Load(ctx, ant, class)
	if err != nil {
		return false
	}
	total := t.Success + t.Failure
	if total == 0 {
		return false
	}
	return t.Success/total > 0.5+q.cfg.SpecialistEdge
}

// project is the crude cost model behind test three: the sum of each subtask
// class's historical mean token cost. It is deliberately simple, because the
// gate only needs a yes-or-no budget answer, and it errs toward refusal when
// any class has no history, the safe direction. The coordination overhead the
// caller adds is the handoff tax, because pretending handoffs are free is how
// a budget projection lies.
func (q *Queen) project(ctx context.Context, sub []TaskBrief) (int64, bool) {
	if q.trails == nil {
		return 0, false
	}
	var total int64
	for _, s := range sub {
		mean, ok, err := q.trails.MeanTokens(ctx, s.Class)
		if err != nil || !ok {
			return 0, false
		}
		total += mean
	}
	return total, true
}

// mutates reports whether a subtask produces a change to the tree, which is
// what makes it constrain other writers. The deliverable is the authoritative
// signal: a patch mutates, a finding is read-only.
func mutates(b TaskBrief) bool { return b.Deliverable == KindPatch }

// fileAnchors returns the file anchor values of a brief, the set the
// independence test intersects. Symbol and commit anchors are not file
// mutations and do not count here.
func fileAnchors(b TaskBrief) []string {
	var out []string
	for _, a := range b.Anchors {
		if a.Kind == AnchorFile {
			out = append(out, a.Value)
		}
	}
	return out
}

// anchorsOverlap reports whether two briefs name any file anchor in common,
// the structural sign that they might write the same file.
func anchorsOverlap(a, b TaskBrief) bool {
	af := fileAnchors(a)
	for _, f := range fileAnchors(b) {
		if slices.Contains(af, f) {
			return true
		}
	}
	return false
}
