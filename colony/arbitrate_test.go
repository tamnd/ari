package colony

import (
	"slices"
	"strings"
	"testing"
)

func TestAssignStakes(t *testing.T) {
	patch := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	finding := Finding{Header: Header{ID: "f1", Kind: KindFinding}}

	cases := []struct {
		name            string
		subject         Handoff
		downstreamPatch bool
		safetyPath      bool
		deleted, limit  int
		want            Stakes
	}{
		{"patch touching a safety path", patch, false, true, 0, 50, StakesHigh},
		{"patch deleting past the limit", patch, false, false, 200, 50, StakesHigh},
		{"ordinary patch", patch, false, false, 3, 50, StakesNormal},
		{"finding with no downstream patch", finding, false, false, 0, 50, StakesLow},
		{"finding feeding a patch", finding, true, false, 0, 50, StakesNormal},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := AssignStakes(c.subject, c.downstreamPatch, c.safetyPath, c.deleted, c.limit)
			if got != c.want {
				t.Errorf("stakes = %q, want %q", got, c.want)
			}
		})
	}
}

func TestQuorumSize(t *testing.T) {
	for stakes, want := range map[Stakes]int{StakesLow: 1, StakesNormal: 3, StakesHigh: 5} {
		if got := QuorumSize(stakes); got != want {
			t.Errorf("quorum for %q = %d, want %d", stakes, got, want)
		}
	}
}

// TestPickQuorumMaximizesDistance is the picker DoD: seats go to the ants
// farthest from the disputants and from each other, and a disputant is never
// seated on the quorum judging its own disagreement.
func TestPickQuorumMaximizesDistance(t *testing.T) {
	disputants := []Arbiter{
		{Ant: "d1", Discovery: []float32{1, 0}, Fitness: 0.9},
		{Ant: "d2", Discovery: []float32{1, 0}, Fitness: 0.9},
	}
	candidates := []Arbiter{
		{Ant: "near", Discovery: []float32{1, 0.05}, Fitness: 0.9},
		{Ant: "far1", Discovery: []float32{0, 1}, Fitness: 0.9},
		{Ant: "far2", Discovery: []float32{-1, 0}, Fitness: 0.9},
	}

	one := PickQuorum(candidates, disputants, 1, 0.3)
	if len(one) != 1 || one[0].Ant != "far2" {
		t.Fatalf("size-1 quorum = %v, want the farthest ant far2", antIDs(one))
	}

	two := PickQuorum(candidates, disputants, 2, 0.3)
	if len(two) != 2 || two[0].Ant != "far2" || two[1].Ant != "far1" {
		t.Errorf("size-2 quorum = %v, want far2 then far1", antIDs(two))
	}

	// A disputant offered as a candidate is never seated on its own dispute.
	withSelf := append([]Arbiter(nil), candidates...)
	withSelf = append(withSelf, Arbiter{Ant: "d1", Discovery: []float32{-1, 0}, Fitness: 0.99})
	got := PickQuorum(withSelf, disputants, 3, 0.3)
	if slices.Contains(antIDs(got), "d1") {
		t.Errorf("quorum %v seated a disputant", antIDs(got))
	}
}

// TestPickQuorumRespectsFitnessFloor proves a distant but incompetent ant is not
// seated: the farthest candidate is below the floor, so a closer competent ant
// takes the seat instead.
func TestPickQuorumRespectsFitnessFloor(t *testing.T) {
	disputants := []Arbiter{{Ant: "d1", Discovery: []float32{1, 0}, Fitness: 0.9}}
	candidates := []Arbiter{
		{Ant: "far-unfit", Discovery: []float32{-1, 0}, Fitness: 0.1},
		{Ant: "mid-fit", Discovery: []float32{0, 1}, Fitness: 0.8},
	}
	got := PickQuorum(candidates, disputants, 1, 0.3)
	if len(got) != 1 || got[0].Ant != "mid-fit" {
		t.Errorf("quorum = %v, want mid-fit; the distant ant is below the fitness floor", antIDs(got))
	}
}

// TestOpenIssuesIndependentBriefs proves members judge independently and
// simultaneously: one directed brief per member, each carrying the subject and
// the original disagreement, none carrying another member's verdict.
func TestOpenIssuesIndependentBriefs(t *testing.T) {
	subject := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	disputants := []Verdict{
		{Header: Header{ID: "v1", From: "verifier-a"}, Pass: true, Reasons: []string{"tests pass"}, Stakes: StakesNormal},
		{Header: Header{ID: "v2", From: "verifier-b"}, Pass: false, Reasons: []string{"leaks a goroutine"}, Stakes: StakesNormal},
	}
	members := []Arbiter{{Ant: "judge-1"}, {Ant: "judge-2"}}
	evidence := []ContextRef{{Path: "server.go"}}

	rec, briefs := Open(subject, disputants, members, "s1", evidence)

	if rec.Subject != "p1" || rec.Stakes != StakesNormal || rec.QuorumSize != 3 {
		t.Errorf("opened record = %+v, want subject p1 normal quorum 3", rec)
	}
	if strings.Join(rec.Disputants, ",") != "v1,v2" {
		t.Errorf("opened disputants = %v, want the two disagreeing verdicts", rec.Disputants)
	}
	if len(briefs) != 2 {
		t.Fatalf("issued %d briefs, want one per member", len(briefs))
	}
	for i, b := range briefs {
		if b.DirectedTo != members[i].Ant {
			t.Errorf("brief %d directed to %q, want %q", i, b.DirectedTo, members[i].Ant)
		}
		if b.Deliverable != KindVerdict || b.TaskID != "p1" {
			t.Errorf("brief %d = %+v, want a verdict on p1", i, b)
		}
		if len(b.Context) != 1 || b.Context[0].Path != "server.go" {
			t.Errorf("brief %d evidence = %+v, want the subject's evidence", i, b.Context)
		}
		if !strings.Contains(b.Goal, "verifier-a") || !strings.Contains(b.Goal, "verifier-b") {
			t.Errorf("brief %d goal does not carry the disagreement: %q", i, b.Goal)
		}
		if strings.Contains(b.Goal, "judge-1") || strings.Contains(b.Goal, "judge-2") {
			t.Errorf("brief %d goal leaks a sibling member, anchoring the judge: %q", i, b.Goal)
		}
	}
}

// TestCloseMajorityStands is the tally DoD: a majority of returned verdicts
// decides, and the quorum owns the call.
func TestCloseMajorityStands(t *testing.T) {
	subject := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	members := []Verdict{
		{Header: Header{ID: "m1"}, Pass: true},
		{Header: Header{ID: "m2"}, Pass: true},
		{Header: Header{ID: "m3"}, Pass: false},
	}
	d, closed := Close(subject, members, true)
	if !d.Pass || d.DecidedBy != DecidedByQuorum || d.Escalated {
		t.Errorf("decision = %+v, want a quorum pass", d)
	}
	if closed.PassVotes != 2 || closed.FailVotes != 1 || len(closed.Verdicts) != 3 {
		t.Errorf("closed record = %+v, want 2-1 with three verdict ids", closed)
	}
}

// TestCloseTieEscalatesToUser proves a tie, only reachable when a seated member
// fails to return, goes to the user and never re-rolls.
func TestCloseTieEscalatesToUser(t *testing.T) {
	subject := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	// A size-3 quorum where one member never returned, and the two that did split.
	members := []Verdict{
		{Header: Header{ID: "m1"}, Pass: true},
		{Header: Header{ID: "m2"}, Pass: false},
	}
	d, closed := Close(subject, members, true)
	if !d.Escalated || d.DecidedBy != DecidedByUser {
		t.Errorf("decision = %+v, want escalation to the user on a tie", d)
	}
	if closed.DecidedBy != DecidedByUser {
		t.Errorf("closed decided_by = %q, want user", closed.DecidedBy)
	}
}

// TestCloseUnaffordableEscalates proves an arbitration the ledger cannot pay for
// goes to the user, who is free, even when the returned votes are lopsided.
func TestCloseUnaffordableEscalates(t *testing.T) {
	subject := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	members := []Verdict{{Header: Header{ID: "m1"}, Pass: true}}
	d, _ := Close(subject, members, false)
	if !d.Escalated || d.DecidedBy != DecidedByUser {
		t.Errorf("decision = %+v, want escalation when arbitration is unaffordable", d)
	}
}

// TestCloseNeverDropsOutput proves a quorum too thin to decide still routes the
// disputed output to the user rather than silently dropping it.
func TestCloseNeverDropsOutput(t *testing.T) {
	subject := Patch{Header: Header{ID: "p1", Kind: KindPatch}}
	d, closed := Close(subject, nil, true)
	if !d.Escalated || d.DecidedBy != DecidedByUser {
		t.Errorf("decision = %+v, want escalation when no member returned", d)
	}
	if closed.Subject != "p1" {
		t.Errorf("closed record dropped the subject: %+v", closed)
	}
}

func antIDs(as []Arbiter) []string {
	out := make([]string, len(as))
	for i, a := range as {
		out[i] = a.Ant
	}
	return out
}
