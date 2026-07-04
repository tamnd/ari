package colony

import (
	"context"
	"testing"
)

// fakeTrails is a trail store with hand-set per-class means and per-ant success
// rates, so a fan-out test can drive the cost model and the specialist limb
// without a real database. A class absent from means has no history, which is
// how a test exercises the uncertain-estimate refusal.
type fakeTrails struct {
	means map[TaskClass]int64
	edge  map[string]Trail // keyed by ant+"/"+class
}

func (f fakeTrails) Update(context.Context, Outcome) error { return nil }
func (f fakeTrails) Sample(context.Context, string, TaskClass) (float64, error) {
	return 0.5, nil
}
func (f fakeTrails) Load(_ context.Context, ant string, class TaskClass) (Trail, error) {
	return f.edge[ant+"/"+string(class)], nil
}
func (f fakeTrails) MeanTokens(_ context.Context, class TaskClass) (int64, bool, error) {
	v, ok := f.means[class]
	return v, ok, nil
}

func sub(id string, class TaskClass, deliver Kind, files ...string) TaskBrief {
	b := TaskBrief{
		Header:      Header{ID: id, Kind: KindTaskBrief, From: "queen", TaskID: id},
		Goal:        "part of a split",
		Class:       class,
		Deliverable: deliver,
	}
	for _, f := range files {
		b.Anchors = append(b.Anchors, Anchor{Kind: AnchorFile, Value: f})
	}
	return b
}

func gateQueen(trails TrailStore, rows ...CardRow) *Queen {
	return NewQueen(stubCards{rows: rows}).WithRouting(trails, DefaultRouteConfig(), nil)
}

// TestOverlappingWritesFailIndependence is the DoD that a task with overlapping
// write anchors fails test one and gets one ant.
func TestOverlappingWritesFailIndependence(t *testing.T) {
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassEdit: 1000}})
	split := []TaskBrief{
		sub("s1", ClassEdit, KindPatch, "pkg/a.go", "pkg/shared.go"),
		sub("s2", ClassEdit, KindPatch, "pkg/b.go", "pkg/shared.go"),
	}
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan != nil {
		t.Errorf("gate approved a split whose writers share pkg/shared.go: %+v", plan.Arg)
	}
}

// TestWriteHeavyNoSpecialistFailsTestTwo is the DoD that a write-heavy task with
// no standout specialist fails test two and gets one ant, even when its writers
// are anchor-disjoint and the budget is ample.
func TestWriteHeavyNoSpecialistFailsTestTwo(t *testing.T) {
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassEdit: 1000}})
	split := []TaskBrief{
		sub("s1", ClassEdit, KindPatch, "pkg/a.go"),
		sub("s2", ClassEdit, KindPatch, "pkg/b.go"),
	}
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan != nil {
		t.Errorf("gate approved a write-heavy split with no specialist: %+v", plan.Arg)
	}
}

// TestBudgetExceededFailsTestThree is the DoD that a decomposable task whose
// projected bill exceeds the remaining session budget fails test three and gets
// one ant.
func TestBudgetExceededFailsTestThree(t *testing.T) {
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassSurvey: 5000}})
	split := []TaskBrief{
		sub("s1", ClassSurvey, KindFinding, "pkg/a.go"),
		sub("s2", ClassSurvey, KindFinding, "pkg/b.go"),
	}
	// Two survey means of 5000 plus 2x2000 overhead is 14000, over a 10000 floor.
	if plan := q.FanOutGate(context.Background(), split, 10_000); plan != nil {
		t.Errorf("gate approved a split whose projection exceeds the budget: %+v", plan.Arg)
	}
	// The same split fits a generous budget and passes read-heavy test two.
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan == nil {
		t.Error("gate refused a read-heavy split that fits the budget")
	}
}

// TestReadHeavySplitApproved is the DoD that a genuinely decomposable read-heavy
// task passes all three tests and returns a plan whose subtasks have disjoint
// anchor sets and sliced budgets, with the full argument journaled.
func TestReadHeavySplitApproved(t *testing.T) {
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassSurvey: 3000}})
	split := []TaskBrief{
		sub("s1", ClassSurvey, KindFinding, "pkg/a.go"),
		sub("s2", ClassSurvey, KindFinding, "pkg/b.go"),
	}
	plan := q.FanOutGate(context.Background(), split, 1_000_000)
	if plan == nil {
		t.Fatal("gate refused a clean read-heavy split")
	}
	if plan.Arg.IndependenceBy != independenceDeclared {
		t.Errorf("independence evidence = %q, want %q for an all-read split", plan.Arg.IndependenceBy, independenceDeclared)
	}
	if plan.Arg.Workload != workloadReadHeavy {
		t.Errorf("workload evidence = %q, want %q", plan.Arg.Workload, workloadReadHeavy)
	}
	if plan.Arg.Subtasks != 2 {
		t.Errorf("fan width = %d, want 2", plan.Arg.Subtasks)
	}
	// 2x3000 mean plus 2x2000 overhead.
	if plan.Arg.Projected != 10_000 {
		t.Errorf("projection = %d, want 10000", plan.Arg.Projected)
	}
	if plan.Arg.Remaining != 1_000_000 {
		t.Errorf("remaining = %d, want 1000000", plan.Arg.Remaining)
	}
	// The plan's writers, if any, must be anchor-disjoint; these are read-only,
	// so overlapping reads would still be fine, but each names a distinct file.
	seen := map[string]bool{}
	for _, s := range plan.Subtasks {
		for _, f := range fileAnchors(s) {
			if seen[f] && mutates(s) {
				t.Errorf("two writing subtasks share %s", f)
			}
			seen[f] = true
		}
	}
}

// TestSpecialistLimbApprovesWriteSplit is the DoD's other approval path: a
// write-heavy split can still fan out when a proven specialist backs one limb.
func TestSpecialistLimbApprovesWriteSplit(t *testing.T) {
	// A migrator card that declares migrate in Prefers and has a real edge.
	card := CardRow{
		ID:      "migrator",
		Status:  StatusActive,
		Tier:    TierFrontier,
		Classes: []TaskClass{ClassMigrate},
		Tools:   []string{"write", "sh"},
		Prefers: []TaskClass{ClassMigrate},
	}
	trails := fakeTrails{
		means: map[TaskClass]int64{ClassMigrate: 2000},
		edge:  map[string]Trail{"migrator/migrate": {Success: 9, Failure: 1}},
	}
	q := gateQueen(trails, card)
	split := []TaskBrief{
		sub("s1", ClassMigrate, KindPatch, "pkg/a.go"),
		sub("s2", ClassMigrate, KindPatch, "pkg/b.go"),
	}
	plan := q.FanOutGate(context.Background(), split, 1_000_000)
	if plan == nil {
		t.Fatal("gate refused a write split backed by a proven specialist")
	}
	if plan.Arg.Workload != workloadSpecialist || plan.Arg.Specialist != "migrator" {
		t.Errorf("workload = %q specialist = %q, want specialist-advantage/migrator", plan.Arg.Workload, plan.Arg.Specialist)
	}
	if plan.Arg.IndependenceBy != independenceDisjoint {
		t.Errorf("independence = %q, want %q for disjoint writers", plan.Arg.IndependenceBy, independenceDisjoint)
	}
}

// TestUnprovenSpecialistIsNotEnough is the guard on the specialist limb: a card
// that merely declares a preference, with no fitness to back it, does not carry
// test two.
func TestUnprovenSpecialistIsNotEnough(t *testing.T) {
	card := CardRow{
		ID:      "wannabe",
		Status:  StatusActive,
		Classes: []TaskClass{ClassMigrate},
		Tools:   []string{"write", "sh"},
		Prefers: []TaskClass{ClassMigrate},
	}
	// No trail edge for wannabe: Load returns the zero Trail (no history).
	trails := fakeTrails{means: map[TaskClass]int64{ClassMigrate: 2000}}
	q := gateQueen(trails, card)
	split := []TaskBrief{
		sub("s1", ClassMigrate, KindPatch, "pkg/a.go"),
		sub("s2", ClassMigrate, KindPatch, "pkg/b.go"),
	}
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan != nil {
		t.Errorf("gate approved a split on a preference with no proven edge: %+v", plan.Arg)
	}
}

// TestDependencyFailsIndependence is the output-as-input case: a subtask that
// depends on another in the set is not independent, no matter its anchors.
func TestDependencyFailsIndependence(t *testing.T) {
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassSurvey: 1000}})
	split := []TaskBrief{
		sub("s1", ClassSurvey, KindFinding, "pkg/a.go"),
		sub("s2", ClassSurvey, KindFinding, "pkg/b.go"),
	}
	split[1].DependsOn = []string{"s1"}
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan != nil {
		t.Errorf("gate approved a split where s2 depends on s1: %+v", plan.Arg)
	}
}

// TestUncertainEstimateRefuses is the DoD that when the cost model has no
// history for a subtask class the gate refuses rather than guess.
func TestUncertainEstimateRefuses(t *testing.T) {
	// means has ClassSurvey but not ClassResearch, so one subtask is unpriced.
	q := gateQueen(fakeTrails{means: map[TaskClass]int64{ClassSurvey: 1000}})
	split := []TaskBrief{
		sub("s1", ClassSurvey, KindFinding, "pkg/a.go"),
		sub("s2", ClassResearch, KindFinding, "pkg/b.go"),
	}
	if plan := q.FanOutGate(context.Background(), split, 1_000_000); plan != nil {
		t.Errorf("gate approved a split with an unpriced class: %+v", plan.Arg)
	}
}

// TestSingleSubtaskNeverFansOut is the trivial floor: a lone subtask is a
// single-ant task and the gate refuses without touching the cost model.
func TestSingleSubtaskNeverFansOut(t *testing.T) {
	q := gateQueen(fakeTrails{})
	one := []TaskBrief{sub("s1", ClassSurvey, KindFinding, "pkg/a.go")}
	if plan := q.FanOutGate(context.Background(), one, 1_000_000); plan != nil {
		t.Errorf("gate fanned out a single subtask: %+v", plan.Arg)
	}
}
