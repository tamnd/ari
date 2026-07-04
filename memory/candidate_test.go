package memory

import (
	"testing"

	"github.com/tamnd/ari/memory/sqlite"
)

var testSource = sqlite.Source{Ant: "worker", Task: "t1", Commit: "9c2e1a4"}

// TestRememberBuildsCandidate: a well-formed remember call produces a
// candidate carrying its body, importance, and anchor.
func TestRememberBuildsCandidate(t *testing.T) {
	c, err := Remember("ant_worker", sqlite.KindObservation,
		"run make gen after editing schema.go", 7,
		[]sqlite.Anchor{{Kind: "file", Ref: "schema.go"}}, nil, testSource)
	if err != nil {
		t.Fatalf("remember: %v", err)
	}
	if c.Body == "" || c.Importance != 7 || len(c.Anchors) != 1 {
		t.Fatalf("candidate = %+v, want body, importance 7, one anchor", c)
	}
}

// TestRememberRequiresAnchor: a memory must be tied to something in the repo,
// so a remember with no anchor is refused.
func TestRememberRequiresAnchor(t *testing.T) {
	if _, err := Remember("ant_worker", sqlite.KindObservation, "a floating belief", 5, nil, nil, testSource); err == nil {
		t.Fatal("remember with no anchor was accepted, want refusal")
	}
}

// TestRememberReflectionNeedsEvidence: the D11 rule at the tool boundary, the
// earliest of its three enforcement points.
func TestRememberReflectionNeedsEvidence(t *testing.T) {
	_, err := Remember("ant_worker", sqlite.KindReflection, "never hand-edit gen", 8,
		[]sqlite.Anchor{{Kind: "file", Ref: "gen/model.go"}}, nil, testSource)
	if err == nil {
		t.Fatal("reflection with no evidence was accepted at the tool boundary, want refusal")
	}
}

// TestRememberRejectsOutOfRangeImportance keeps the 1..10 scale honest.
func TestRememberRejectsOutOfRangeImportance(t *testing.T) {
	for _, imp := range []int{0, 11, -1} {
		if _, err := Remember("ant_worker", sqlite.KindObservation, "body", imp,
			[]sqlite.Anchor{{Kind: "file", Ref: "x.go"}}, nil, testSource); err == nil {
			t.Fatalf("importance %d was accepted, want refusal", imp)
		}
	}
}

// TestHarvesterEmitsOnFailThenSucceed is the automatic observation case: a
// command that fails and then succeeds yields one observation candidate, and a
// plain success on its own yields nothing.
func TestHarvesterEmitsOnFailThenSucceed(t *testing.T) {
	h := NewHarvester("ant_worker", testSource)

	if got := h.Observe(CommandOutcome{Command: "go test ./...", Failed: false}); got != nil {
		t.Fatalf("a first-time success emitted a candidate: %+v", got)
	}
	if got := h.Observe(CommandOutcome{Command: "go build ./...", Failed: true}); got != nil {
		t.Fatalf("a failure alone emitted a candidate: %+v", got)
	}
	got := h.Observe(CommandOutcome{Command: "go build ./...", Failed: false})
	if got == nil {
		t.Fatal("fail-then-succeed emitted no candidate, want one")
	}
	if got.Kind != sqlite.KindObservation {
		t.Fatalf("kind = %q, want observation", got.Kind)
	}
	if len(got.Anchors) != 1 || got.Anchors[0].Kind != "command" || got.Anchors[0].Ref != "go build ./..." {
		t.Fatalf("anchor = %+v, want the command anchor", got.Anchors)
	}
	// The fix is recorded once: a further success does not re-emit.
	if again := h.Observe(CommandOutcome{Command: "go build ./...", Failed: false}); again != nil {
		t.Fatalf("a second success re-emitted a candidate: %+v", again)
	}
}
