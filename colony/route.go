package colony

import (
	"context"
	"fmt"
	"math"
	"path"
	"slices"
	"strings"
)

// Routing turns a validated TaskBrief into an Assignment: which ant, at what
// tier, under what budget, with a journaled reason (doc 06 section 4). The
// pipeline is deliberately cheap and every stage narrows before the next: two
// hard filters build a small candidate set, then a fused score picks a winner,
// and the overwhelmingly common single-candidate case skips the scoring math
// entirely so the queen is invisible when there is no real choice (D5).

// RouteConfig is the tunable part of routing. The defaults lean on match over
// fitness because a cold colony has no fitness signal and all the routing
// information is in the match term; as trails fill in, WFit earns its weight.
type RouteConfig struct {
	WMatch         float64 // weight on task match (cosine + signal bonus)
	WFit           float64 // weight on the Thompson fitness draw
	Epsilon        float64 // forced-exploration floor, D13
	SignalBonus    float64 // per-hit signal bonus added to match
	SignalBonusCap float64 // ceiling on the total signal bonus, anti-gaming

	CoordinationOverhead int64   // per-subtask handoff tax the fan-out gate adds to its projection
	SpecialistEdge       float64 // margin above the neutral prior a specialist's fitness must clear
}

// DefaultRouteConfig is the shipping default: match-leaning fusion, a small
// exploration epsilon, and a capped signal bonus.
func DefaultRouteConfig() RouteConfig {
	return RouteConfig{
		WMatch:               0.7,
		WFit:                 0.3,
		Epsilon:              0.05,
		SignalBonus:          0.02,
		SignalBonusCap:       0.1,
		CoordinationOverhead: 2000,
		SpecialistEdge:       0.15,
	}
}

// Assignment is the routing decision: the ant that will run the task, its
// tier and budget, and the reason, which is journaled so a stochastic choice
// stays auditable.
type Assignment struct {
	Task    string
	Ant     string
	Tier    ModelTier
	Budget  Budget
	Reason  RoutingReason
	Spawned bool // true when this assignment birthed a new ant (slice 10)
}

// Candidate is one scored survivor, recorded in the reason so a human reading
// the journal can reconstruct not just who won but who else was in the running
// and by how much.
type Candidate struct {
	Ant   string    `json:"ant"`
	Tier  ModelTier `json:"tier"`
	Match float64   `json:"match"`
	Fit   float64   `json:"fit"`
	Score float64   `json:"score"`
}

// RoutingReason is the journaled record of a routing decision (doc 06 section
// 9, D2). It carries every candidate that survived filtering with its match
// and sampled fit, the winner, and whether forced exploration fired, so the
// epsilon is safe to ship: a surprised user sees a deliberate trial rather
// than a queen who made a mistake.
type RoutingReason struct {
	Class      TaskClass   `json:"class"`
	Candidates []Candidate `json:"candidates"`
	Winner     string      `json:"winner"`
	Explored   bool        `json:"explored"`
	Summary    string      `json:"summary"`
	FanOut     *FanOutArg  `json:"fanout,omitempty"` // present only when the D5 gate approved a split
}

// candidate is the internal scoring row before it is frozen into the journal.
type candidate struct {
	ant   string
	tier  ModelTier
	match float64
	fit   float64
	score float64
}

// classTools is the hard tool requirement per class: the correctness floor a
// card must clear to be a candidate at all, no matter how good its fitness. A
// migrate task needs write and sh; a survey needs only read (doc 06 4.1).
var classTools = map[TaskClass][]string{
	ClassEdit:       {"edit"},
	ClassMigrate:    {"write", "sh"},
	ClassTest:       {"sh"},
	ClassMechanical: {"edit", "sh"},
	ClassDebug:      {"read", "sh"},
	ClassSurvey:     {"read"},
	ClassResearch:   {"read"},
	ClassGeneral:    nil,
}

// Assign is the routing pipeline. It builds the candidate set with the hard
// tool filter then the class prefilter, short-circuits the single-candidate
// common case with no sampling, and otherwise scores each candidate and picks
// a winner under the exploration epsilon.
func (q *Queen) Assign(ctx context.Context, brief TaskBrief) (Assignment, error) {
	rows, err := q.cards.List(ctx)
	if err != nil {
		return Assignment{}, fmt.Errorf("routing %s: listing cards: %w", brief.ID, err)
	}
	pool := q.candidates(brief, rows)
	if len(pool) == 0 {
		return Assignment{}, fmt.Errorf("routing %s: no eligible ant for class %s", brief.ID, brief.Class)
	}
	if len(pool) == 1 {
		// The common case: one obvious ant. No Beta draw touches the hot path.
		return sole(brief, pool[0]), nil
	}
	if q.trails == nil {
		return Assignment{}, fmt.Errorf("routing %s: %d candidates but no trail store wired", brief.ID, len(pool))
	}

	cands := make([]candidate, len(pool))
	for i, c := range pool {
		fit, err := q.trails.Sample(ctx, c.ID, brief.Class)
		if err != nil {
			return Assignment{}, fmt.Errorf("routing %s: sampling trail for %s: %w", brief.ID, c.ID, err)
		}
		cands[i] = q.score(brief, c, fit)
	}
	winner, explored := q.pick(cands, pool)
	return assemble(brief, cands, winner, explored), nil
}

// candidates runs the two hard filters in order: the tool filter first because
// it is the cheapest and most absolute, then the class prefilter, which keeps
// class-matching cards plus every general card. Archived cards never route.
func (q *Queen) candidates(brief TaskBrief, rows []CardRow) []CardRow {
	required := classTools[brief.Class]
	var pool []CardRow
	for _, r := range rows {
		if r.Status == StatusArchived {
			continue
		}
		if !hasAll(r.Tools, required) {
			continue
		}
		if !classMatch(brief.Class, r) {
			continue
		}
		pool = append(pool, r)
	}
	return pool
}

// classMatch keeps a card whose classes include the brief's class, plus every
// general card, which is always a candidate. A general brief matches every
// tool-passing card, so the prefilter is a no-op there.
func classMatch(brief TaskClass, r CardRow) bool {
	if brief == ClassGeneral {
		return true
	}
	return slices.Contains(r.Classes, brief) || slices.Contains(r.Classes, ClassGeneral)
}

// hasAll reports whether have covers every required tool.
func hasAll(have, required []string) bool {
	for _, t := range required {
		if !slices.Contains(have, t) {
			return false
		}
	}
	return true
}

// score fuses task match with sampled fitness. Match is deterministic (cosine
// plus a small capped signal bonus); fit is the Thompson draw, so the same
// candidate scores slightly differently each call, which is what gives young
// ants their trials (D13).
func (q *Queen) score(brief TaskBrief, c CardRow, fit float64) candidate {
	match := cosine(brief.Embed, c.SkillVec) + q.signalBonus(brief.Anchors, c.Signals)
	return candidate{
		ant:   c.ID,
		tier:  c.Tier,
		match: match,
		fit:   fit,
		score: q.cfg.WMatch*match + q.cfg.WFit*fit,
	}
}

// signalBonus adds a small, capped bump for each card signal that matches an
// anchor the brief named. The cap is the anti-gaming rule: a handful of
// relevant cues help, a hundred irrelevant ones do nothing, so a card cannot
// buy routing by stuffing its Signals list.
func (q *Queen) signalBonus(anchors []Anchor, signals []string) float64 {
	if len(anchors) == 0 || len(signals) == 0 {
		return 0
	}
	hits := 0
	for _, sig := range signals {
		if sig == "" || sig == "*" {
			// The catch-all matches everything, so it discriminates nothing.
			continue
		}
		for _, a := range anchors {
			if signalHits(sig, a.Value) {
				hits++
				break
			}
		}
	}
	return math.Min(float64(hits)*q.cfg.SignalBonus, q.cfg.SignalBonusCap)
}

// signalHits matches a signal against an anchor value: a glob signal matches
// by path.Match on the base name or the whole value, a plain signal by
// substring.
func signalHits(sig, value string) bool {
	if strings.ContainsAny(sig, "*?[") {
		if ok, _ := path.Match(sig, path.Base(value)); ok {
			return true
		}
		ok, _ := path.Match(sig, value)
		return ok
	}
	return strings.Contains(value, sig)
}

// pick applies the exploration epsilon, then argmax. With probability epsilon
// and more than one eligible ant, the queen ignores the score and routes to a
// uniform pick, giving a confident incumbent's rivals a periodic shot; that
// draw is reported so the journal can log it as exploration.
func (q *Queen) pick(cands []candidate, pool []CardRow) (string, bool) {
	q.mu.Lock()
	roll := q.rng.Float64()
	if roll < q.cfg.Epsilon && len(pool) > 1 {
		idx := q.rng.IntN(len(pool))
		q.mu.Unlock()
		return pool[idx].ID, true
	}
	q.mu.Unlock()

	best := cands[0]
	for _, c := range cands[1:] {
		if c.score > best.score {
			best = c
		}
	}
	return best.ant, false
}

// sole assigns the single surviving candidate with no Beta draw, recording the
// reason as "sole eligible candidate". This is the hot path, the same one M0
// through M2 take before the colony has more than one ant.
func sole(brief TaskBrief, c CardRow) Assignment {
	return Assignment{
		Task:   brief.TaskID,
		Ant:    c.ID,
		Tier:   c.Tier,
		Budget: brief.Budget,
		Reason: RoutingReason{
			Class:      brief.Class,
			Candidates: []Candidate{{Ant: c.ID, Tier: c.Tier}},
			Winner:     c.ID,
			Summary:    fmt.Sprintf("sole eligible candidate %s for class %s", c.ID, brief.Class),
		},
	}
}

// assemble freezes the scored candidates into a journaled assignment, finding
// the winner's tier and writing a reason a human can reconstruct.
func assemble(brief TaskBrief, cands []candidate, winner string, explored bool) Assignment {
	journal := make([]Candidate, len(cands))
	var tier ModelTier
	for i, c := range cands {
		journal[i] = Candidate{Ant: c.ant, Tier: c.tier, Match: c.match, Fit: c.fit, Score: c.score}
		if c.ant == winner {
			tier = c.tier
		}
	}
	summary := fmt.Sprintf("%s won class %s over %d candidates", winner, brief.Class, len(cands))
	if explored {
		summary = fmt.Sprintf("explored: %s drawn uniformly over %d candidates for class %s", winner, len(cands), brief.Class)
	}
	return Assignment{
		Task:   brief.TaskID,
		Ant:    winner,
		Tier:   tier,
		Budget: brief.Budget,
		Reason: RoutingReason{
			Class:      brief.Class,
			Candidates: journal,
			Winner:     winner,
			Explored:   explored,
			Summary:    summary,
		},
	}
}

// cosine is the deterministic match term: the angle between the brief's
// embedding and a card's skill vector, in [0,1] for the non-negative bag
// vectors here. Mismatched dimensions or a zero vector score zero rather than
// erroring, the null-embedder state where routing leans on the prefilters.
func cosine(a, b []float32) float64 {
	n := min(len(a), len(b))
	var dot, na, nb float64
	for i := range n {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}
