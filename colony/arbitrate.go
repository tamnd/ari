package colony

import (
	"math"
	"strings"
)

// Arbitration resolves a disagreement between judgments, not a merge conflict:
// two Verdicts split on the same Patch, or two Findings answer one question two
// ways. A failed git apply --3way goes back to reconcile (doc 09 section 6.5);
// this file is only for the case where the machinery agrees on the tree and the
// ants disagree on the call (doc 09 section 7).
//
// The cheap fixes are both wrong. A majority vote among ants seeded from the
// same memory cluster is a rigged election, because D13 siblings share beliefs
// and agree for the same wrong reason; letting the queen decide alone makes her
// a single point of bias with no verification of her own. So arbitration
// convenes a quorum of ants chosen to be distant from the disputants and from
// each other in embedding space, and the distance is the whole point: shared
// bias correlates with shared history.

// The journal events the convener emits around an arbitration. The kernel names
// them; the wiring layer writes them, because colony imports no journal package.
const (
	EventArbitrationOpened = "colony.arbitration.opened"
	EventArbitrationClosed = "colony.arbitration.closed"
)

// DecidedBy records who resolved an arbitration: the distant quorum by majority,
// or the user when the quorum could not decide.
type DecidedBy string

const (
	DecidedByQuorum DecidedBy = "quorum"
	DecidedByUser   DecidedBy = "user"
)

// Arbiter is a candidate for a quorum seat: an ant with its two embeddings and
// its trail fitness on the disputed task class. Discovery is the card's routing
// embedding (doc 06); Identity is the consolidator's per-ant embedding of what
// the ant's memory believes (doc 07). The picker reads both because a single
// axis is a thinner proxy for independent priors than the two together.
type Arbiter struct {
	Ant       string
	Discovery []float32
	Identity  []float32
	Fitness   float64
}

// AssignStakes sets a Verdict's stakes mechanically at creation, which is what
// later sizes the arbitration quorum (doc 09 section 7.2). A Patch that touches
// a D15 safety-check path or deletes more than the configured line count is
// high; a Finding with no downstream Patch is low; everything else is normal.
// The assignment is mechanical, never judged, so the stakes cannot be gamed by
// the ant whose work is under review.
func AssignStakes(subject Handoff, downstreamPatch, touchesSafetyPath bool, deletedLines, deleteLimit int) Stakes {
	switch subject.Hdr().Kind {
	case KindPatch:
		if touchesSafetyPath || (deleteLimit > 0 && deletedLines > deleteLimit) {
			return StakesHigh
		}
		return StakesNormal
	case KindFinding:
		if !downstreamPatch {
			return StakesLow
		}
		return StakesNormal
	default:
		return StakesNormal
	}
}

// QuorumSize maps stakes to seats: one distant ant re-judges an advisory
// conflict, three gate a normal merge, five gate a destructive or
// security-touching change (doc 09 section 7.2). Odd sizes so a full quorum
// cannot tie; a tie is only reachable when a seated member fails to return.
func QuorumSize(s Stakes) int {
	switch s {
	case StakesLow:
		return 1
	case StakesHigh:
		return 5
	default:
		return 3
	}
}

// PickQuorum greedily seats `size` arbiters to maximize the minimum pairwise
// distance across the members plus the disputants, subject to a trail fitness
// floor so a distant but incompetent ant is not seated (doc 09 section 7.2).
// It is farthest-first traversal seeded from the disputants: each seat goes to
// the eligible candidate farthest from everyone already in the set, which is the
// standard greedy maximizer of the minimum pairwise distance. A disputant is
// never seated on the quorum judging its own disagreement. When fewer eligible
// ants exist than seats, it seats what it can; the caller escalates a quorum
// too thin to decide rather than seating an unfit ant to pad it.
func PickQuorum(candidates, disputants []Arbiter, size int, fitnessFloor float64) []Arbiter {
	used := make(map[string]bool, len(disputants))
	for _, d := range disputants {
		used[d.Ant] = true
	}
	eligible := make([]Arbiter, 0, len(candidates))
	for _, c := range candidates {
		if used[c.Ant] || c.Fitness < fitnessFloor {
			continue
		}
		eligible = append(eligible, c)
	}

	seed := append([]Arbiter(nil), disputants...)
	var chosen []Arbiter
	for len(chosen) < size && len(eligible) > 0 {
		set := append(append([]Arbiter(nil), seed...), chosen...)
		best, bestScore := -1, math.Inf(-1)
		for i, c := range eligible {
			d := minDistanceToSet(c, set)
			if d > bestScore || (d == bestScore && best >= 0 && c.Ant < eligible[best].Ant) {
				best, bestScore = i, d
			}
		}
		chosen = append(chosen, eligible[best])
		eligible = append(eligible[:best], eligible[best+1:]...)
	}
	return chosen
}

// minDistanceToSet is the distance from one candidate to the nearest member of
// a set, the quantity farthest-first maximizes. An empty set is maximally far,
// so the first pick is driven purely by distance from the disputants.
func minDistanceToSet(c Arbiter, set []Arbiter) float64 {
	best := math.Inf(1)
	for _, m := range set {
		if d := combinedDistance(c, m); d < best {
			best = d
		}
	}
	if math.IsInf(best, 1) {
		return math.Inf(1)
	}
	return best
}

// combinedDistance averages the cosine distance over the two embedding axes
// that both ants carry. Cosine distance is one minus cosine similarity, so a
// far-apart pair scores near one and near-identical priors score near zero.
// An axis missing on either ant is skipped rather than counted as far, the
// conservative direction: we only claim distance we can actually measure.
func combinedDistance(a, b Arbiter) float64 {
	var sum float64
	var n int
	if len(a.Discovery) > 0 && len(b.Discovery) > 0 {
		sum += 1 - cosine(a.Discovery, b.Discovery)
		n++
	}
	if len(a.Identity) > 0 && len(b.Identity) > 0 {
		sum += 1 - cosine(a.Identity, b.Identity)
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

// Opened is the record the convener journals when an arbitration starts: the
// subject under dispute, the stakes that sized the quorum, the ants seated, and
// the disputing verdicts that triggered it (doc 09 section 7.3).
type Opened struct {
	Subject    string
	Stakes     Stakes
	QuorumSize int
	Members    []string
	Disputants []string
}

// Open builds the opened record and the directed briefs the members judge from.
// Every brief is issued at once and carries only the subject and the original
// disagreement, never another member's verdict, so members judge independently
// and simultaneously and no member anchors on a sibling (doc 09 section 7.3).
func Open(subject Handoff, disputants []Verdict, members []Arbiter, sessionID string, evidence []ContextRef) (Opened, []TaskBrief) {
	hdr := subject.Hdr()
	stakes := disputeStakes(disputants)

	rec := Opened{
		Subject:    hdr.ID,
		Stakes:     stakes,
		QuorumSize: QuorumSize(stakes),
		Members:    make([]string, 0, len(members)),
		Disputants: make([]string, 0, len(disputants)),
	}
	for _, d := range disputants {
		rec.Disputants = append(rec.Disputants, d.ID)
	}

	briefs := make([]TaskBrief, 0, len(members))
	for _, m := range members {
		rec.Members = append(rec.Members, m.Ant)
		briefs = append(briefs, TaskBrief{
			Header: Header{
				Kind:      KindTaskBrief,
				From:      "queen",
				TaskID:    hdr.ID,
				SessionID: sessionID,
				Labels:    hdr.Labels,
			},
			Goal:        arbitrationGoal(subject, disputants),
			Context:     evidence,
			Deliverable: KindVerdict,
			DirectedTo:  m.Ant,
		})
	}
	return rec, briefs
}

// disputeStakes is the highest stakes carried by any disputing verdict: a quorum
// is sized for the most consequential thing under dispute, never the least.
func disputeStakes(disputants []Verdict) Stakes {
	stakes := StakesLow
	for _, d := range disputants {
		if stakesRank(d.Stakes) > stakesRank(stakes) {
			stakes = d.Stakes
		}
	}
	return stakes
}

func stakesRank(s Stakes) int {
	switch s {
	case StakesHigh:
		return 2
	case StakesNormal:
		return 1
	default:
		return 0
	}
}

// arbitrationGoal is the instruction every member gets: re-judge the subject
// given the disagreement, from the evidence, not from either side's conclusion.
func arbitrationGoal(subject Handoff, disputants []Verdict) string {
	var b strings.Builder
	b.WriteString("Two verdicts disagree on this ")
	b.WriteString(string(subject.Hdr().Kind))
	b.WriteString(". Judge it yourself from the evidence and return your own verdict.")
	for _, d := range disputants {
		verdict := "failed"
		if d.Pass {
			verdict = "passed"
		}
		b.WriteString("\n- ")
		b.WriteString(d.From)
		b.WriteByte(' ')
		b.WriteString(verdict)
		b.WriteString(" it")
		for _, r := range d.Reasons {
			b.WriteString(": ")
			b.WriteString(r)
		}
	}
	return b.String()
}

// Decision is the outcome of tallying the members' verdicts. Escalated is true
// when the call goes to the user rather than the quorum: on a tie, which is only
// reachable when a seated member failed to return and the remainder split, or
// when the arbitration could not be afforded in the first place.
type Decision struct {
	Pass      bool
	PassVotes int
	FailVotes int
	DecidedBy DecidedBy
	Escalated bool
}

// Closed is the record the convener journals when an arbitration resolves: the
// vote split, who decided, and the member verdicts, so a replay reconstructs
// not just the outcome but the whole panel (doc 09 section 7.3).
type Closed struct {
	Subject   string
	Pass      bool
	PassVotes int
	FailVotes int
	DecidedBy DecidedBy
	Escalated bool
	Verdicts  []string
}

// Close tallies the returned member verdicts into a decision and its journal
// record. The majority stands. A tie escalates to the user and never re-rolls,
// because a tie means a member did not return and re-rolling would just spend
// more budget on the same split. An arbitration the ledger cannot afford
// escalates too, because a decision the colony cannot pay for goes to the human,
// who is free (doc 09 section 7.3). Either way the disputed output is decided by
// someone, never silently dropped.
func Close(subject Handoff, members []Verdict, affordable bool) (Decision, Closed) {
	pass, fail := 0, 0
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.ID)
		if m.Pass {
			pass++
		} else {
			fail++
		}
	}

	d := Decision{PassVotes: pass, FailVotes: fail}
	switch {
	case !affordable, pass == fail:
		d.DecidedBy = DecidedByUser
		d.Escalated = true
	default:
		d.DecidedBy = DecidedByQuorum
		d.Pass = pass > fail
	}

	return d, Closed{
		Subject:   subject.Hdr().ID,
		Pass:      d.Pass,
		PassVotes: pass,
		FailVotes: fail,
		DecidedBy: d.DecidedBy,
		Escalated: d.Escalated,
		Verdicts:  ids,
	}
}
