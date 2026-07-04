package colony

import (
	"context"
	"math"
	"math/rand/v2"
	"testing"
)

// stubCards is a CardStore that serves a fixed set of rows; routing reads only
// List, so the rest are no-ops. It lets a routing test control the exact
// candidate surface without a real store.
type stubCards struct{ rows []CardRow }

func (s stubCards) List(context.Context) ([]CardRow, error)  { return s.rows, nil }
func (stubCards) Upsert(context.Context, Card) error         { return nil }
func (stubCards) Load(context.Context, string) (Card, error) { return Card{}, nil }
func (stubCards) SetStatus(context.Context, string, CardStatus) error {
	return nil
}

// spyTrails returns a fixed fitness draw and counts how many times it was
// asked, so a test can prove the sole-candidate hot path skips sampling.
type spyTrails struct {
	calls int
	fit   float64
}

func (s *spyTrails) Update(context.Context, Outcome) error { return nil }
func (s *spyTrails) Sample(context.Context, string, TaskClass) (float64, error) {
	s.calls++
	return s.fit, nil
}
func (s *spyTrails) Load(context.Context, string, TaskClass) (Trail, error) {
	return Trail{}, nil
}

func row(id string, classes []TaskClass, tools []string, vec []float32) CardRow {
	return CardRow{ID: id, Status: StatusActive, Tier: TierFrontier, Classes: classes, Tools: tools, SkillVec: vec}
}

func routeQueen(rows []CardRow, trails TrailStore, cfg RouteConfig, rng *rand.Rand) *Queen {
	return NewQueen(stubCards{rows: rows}).WithRouting(trails, cfg, rng)
}

func briefFor(class TaskClass, embed []float32) TaskBrief {
	return TaskBrief{
		Header:      Header{ID: "b1", Kind: KindTaskBrief, From: "queen", TaskID: "t1"},
		Goal:        "do the thing",
		Class:       class,
		Deliverable: KindPatch,
		Embed:       embed,
	}
}

// TestToolFilterRunsBeforeFitness is the DoD that an ant lacking a required
// tool is dropped before any fitness math: a migrate task needs write and sh,
// and a card without them never appears among the scored candidates, and the
// trail store is never asked about it.
func TestToolFilterRunsBeforeFitness(t *testing.T) {
	rows := []CardRow{
		row("mutator", []TaskClass{ClassMigrate}, []string{"write", "sh", "edit"}, []float32{1, 0}),
		row("reader", []TaskClass{ClassMigrate}, []string{"read"}, []float32{1, 0}),
	}
	spy := &spyTrails{fit: 0.5}
	q := routeQueen(rows, spy, DefaultRouteConfig(), rand.New(rand.NewPCG(1, 2)))

	a, err := q.Assign(context.Background(), briefFor(ClassMigrate, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	// Only the tool-passing mutator survived, so it is the sole candidate.
	if a.Ant != "mutator" {
		t.Errorf("winner = %s, want mutator (reader lacks write and sh)", a.Ant)
	}
	for _, c := range a.Reason.Candidates {
		if c.Ant == "reader" {
			t.Error("a tool-lacking card reached the candidate set")
		}
	}
}

// TestClassPrefilterKeepsClassAndGeneral is the DoD that the class prefilter
// keeps class-matching cards plus every general card.
func TestClassPrefilterKeepsClassAndGeneral(t *testing.T) {
	rows := []CardRow{
		row("surveyor", []TaskClass{ClassSurvey}, []string{"read"}, []float32{1, 0}),
		row("generalist", []TaskClass{ClassGeneral}, []string{"read"}, []float32{0, 1}),
		row("tester", []TaskClass{ClassTest}, []string{"read"}, []float32{1, 1}),
	}
	spy := &spyTrails{fit: 0.5}
	q := routeQueen(rows, spy, DefaultRouteConfig(), rand.New(rand.NewPCG(1, 2)))

	a, err := q.Assign(context.Background(), briefFor(ClassSurvey, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	got := map[string]bool{}
	for _, c := range a.Reason.Candidates {
		got[c.Ant] = true
	}
	if !got["surveyor"] || !got["generalist"] {
		t.Errorf("candidates = %v, want the survey card and the general card", got)
	}
	if got["tester"] {
		t.Error("a non-matching, non-general card reached the candidate set")
	}
}

// TestSoleCandidateSkipsSampling is the DoD that a single surviving candidate
// is assigned as sole eligible with no Beta draw on the hot path.
func TestSoleCandidateSkipsSampling(t *testing.T) {
	rows := []CardRow{
		row("only", []TaskClass{ClassEdit}, []string{"edit"}, []float32{1, 0}),
	}
	spy := &spyTrails{fit: 0.9}
	q := routeQueen(rows, spy, DefaultRouteConfig(), rand.New(rand.NewPCG(1, 2)))

	a, err := q.Assign(context.Background(), briefFor(ClassEdit, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if a.Ant != "only" {
		t.Errorf("winner = %s, want only", a.Ant)
	}
	if spy.calls != 0 {
		t.Errorf("trail sampled %d times on the sole-candidate path, want 0", spy.calls)
	}
	if a.Reason.Summary == "" || a.Reason.Winner != "only" {
		t.Errorf("reason = %+v, want a sole-candidate reason naming only", a.Reason)
	}
}

// TestEpsilonForcesExplorationAndJournals is the DoD that the epsilon fires at
// its configured rate and is journaled as explored when it does.
func TestEpsilonForcesExplorationAndJournals(t *testing.T) {
	rows := []CardRow{
		row("incumbent", []TaskClass{ClassEdit}, []string{"edit"}, []float32{1, 0}),
		row("rival", []TaskClass{ClassEdit}, []string{"edit"}, []float32{0, 1}),
	}

	// Epsilon 1: always explore, always journaled.
	always := DefaultRouteConfig()
	always.Epsilon = 1
	q := routeQueen(rows, &spyTrails{fit: 0.5}, always, rand.New(rand.NewPCG(7, 7)))
	a, err := q.Assign(context.Background(), briefFor(ClassEdit, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !a.Reason.Explored {
		t.Error("epsilon 1 must journal the decision as explored")
	}

	// Epsilon 0: never explore.
	never := DefaultRouteConfig()
	never.Epsilon = 0
	q2 := routeQueen(rows, &spyTrails{fit: 0.5}, never, rand.New(rand.NewPCG(7, 7)))
	a2, err := q2.Assign(context.Background(), briefFor(ClassEdit, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if a2.Reason.Explored {
		t.Error("epsilon 0 must never explore")
	}

	// A mid rate fires near its configured fraction over many runs.
	mid := DefaultRouteConfig()
	mid.Epsilon = 0.3
	q3 := routeQueen(rows, &spyTrails{fit: 0.5}, mid, rand.New(rand.NewPCG(11, 13)))
	explored := 0
	const runs = 4000
	for range runs {
		a, err := q3.Assign(context.Background(), briefFor(ClassEdit, []float32{1, 0}))
		if err != nil {
			t.Fatalf("assign: %v", err)
		}
		if a.Reason.Explored {
			explored++
		}
	}
	if rate := float64(explored) / runs; math.Abs(rate-0.3) > 0.03 {
		t.Errorf("exploration rate = %.3f, want near 0.3", rate)
	}
}

// TestReasonReconstructsDecision is the DoD that the journaled reason lets a
// human reconstruct why an ant won: every survivor with its match, fit, and
// score, and the winner.
func TestReasonReconstructsDecision(t *testing.T) {
	rows := []CardRow{
		row("aligned", []TaskClass{ClassEdit}, []string{"edit"}, []float32{1, 0}),
		row("orthogonal", []TaskClass{ClassEdit}, []string{"edit"}, []float32{0, 1}),
	}
	// No exploration, so the fused score decides and the aligned card, whose
	// vector matches the brief, must win.
	cfg := DefaultRouteConfig()
	cfg.Epsilon = 0
	q := routeQueen(rows, &spyTrails{fit: 0.5}, cfg, rand.New(rand.NewPCG(1, 2)))

	a, err := q.Assign(context.Background(), briefFor(ClassEdit, []float32{1, 0}))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if a.Ant != "aligned" {
		t.Errorf("winner = %s, want aligned (its vector matches the brief)", a.Ant)
	}
	if len(a.Reason.Candidates) != 2 {
		t.Fatalf("reason carries %d candidates, want both survivors", len(a.Reason.Candidates))
	}
	for _, c := range a.Reason.Candidates {
		if c.Score == 0 && c.Match == 0 && c.Fit == 0 {
			t.Errorf("candidate %s has no recorded scores; the decision is not reconstructable", c.Ant)
		}
		wantScore := cfg.WMatch*c.Match + cfg.WFit*c.Fit
		if math.Abs(c.Score-wantScore) > 1e-9 {
			t.Errorf("candidate %s score %.4f is not WMatch*match + WFit*fit %.4f", c.Ant, c.Score, wantScore)
		}
	}
}

// TestSignalBonusIsCapped is the anti-gaming DoD: a card cannot buy routing by
// stuffing its Signals list; a hundred matching signals add no more than the
// cap.
func TestSignalBonusIsCapped(t *testing.T) {
	cfg := DefaultRouteConfig()
	q := &Queen{cfg: cfg}
	anchors := []Anchor{{Kind: AnchorFile, Value: "colony/board.go"}}

	many := make([]string, 100)
	for i := range many {
		many[i] = "*.go"
	}
	bonus := q.signalBonus(anchors, many)
	if bonus > cfg.SignalBonusCap+1e-9 {
		t.Errorf("signal bonus = %.4f, want capped at %.4f", bonus, cfg.SignalBonusCap)
	}
	if bonus == 0 {
		t.Error("a matching signal should add some bonus")
	}
	if q.signalBonus(anchors, []string{"*"}) != 0 {
		t.Error("the catch-all signal discriminates nothing and must add no bonus")
	}
}
