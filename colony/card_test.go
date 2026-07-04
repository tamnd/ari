package colony

import (
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

func TestWorkerCardValidates(t *testing.T) {
	card := WorkerCard()
	if err := card.Validate(); err != nil {
		t.Fatalf("the built-in worker must validate: %v", err)
	}
	if card.Verify.IsEmpty() {
		t.Error("the worker card must name a verification story")
	}
	if card.MutatesWithoutProbe() {
		t.Error("the worker mutates and must carry a probe")
	}
	for _, tl := range card.Tools {
		if !coreTools[tl] {
			t.Errorf("tool %q is not one of the D7 six", tl)
		}
	}
}

func TestCardValidateRejectsGaps(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Card)
	}{
		{"no id", func(c *Card) { c.ID = "" }},
		{"no name", func(c *Card) { c.Name = "" }},
		{"no namespace", func(c *Card) { c.State.Namespace = "" }},
		{"no accepted kinds", func(c *Card) { c.Commands.Accepts = nil }},
		{"no produced kinds", func(c *Card) { c.Commands.Produces = nil }},
		{"mutates without probe", func(c *Card) { c.Inspect.Probes = nil }},
		{"no render style", func(c *Card) { c.Render.Style = "" }},
		{"no verify fixtures", func(c *Card) { c.Verify.Fixtures = nil }},
		{"no verify check", func(c *Card) { c.Verify.Check = "" }},
		{"no summary", func(c *Card) { c.Discovery.Summary = "" }},
		{"no classes", func(c *Card) { c.Discovery.Classes = nil }},
		{"no tier", func(c *Card) { c.Tier = "" }},
		{"unknown tier", func(c *Card) { c.Tier = "gigantic" }},
		{"no tools", func(c *Card) { c.Tools = nil }},
		{"unknown tool", func(c *Card) { c.Tools = []string{"read", "teleport"} }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			card := WorkerCard()
			tc.mutate(&card)
			if err := card.Validate(); err == nil {
				t.Fatal("Validate accepted a card with a hole in its contract")
			}
		})
	}
}

// TestValidateAcceptsWellFormed pairs the rejection table with the accept
// path the slice 1 DoD names: a card that is complete and correct passes.
func TestValidateAcceptsWellFormed(t *testing.T) {
	if err := WorkerCard().Validate(); err != nil {
		t.Fatalf("a well-formed card must validate: %v", err)
	}
}

// TestMutatesWithoutProbe pins probe-before-mutate on its own: a mutator
// with no probe is a violation, the same mutator with a probe is clean, and
// a non-mutator with no probe is fine because it changes nothing.
func TestMutatesWithoutProbe(t *testing.T) {
	mutatorNoProbe := WorkerCard()
	mutatorNoProbe.Inspect.Probes = nil
	if !mutatorNoProbe.MutatesWithoutProbe() {
		t.Error("a card that produces a patch with no probe must report the violation")
	}

	mutatorWithProbe := WorkerCard()
	if mutatorWithProbe.MutatesWithoutProbe() {
		t.Error("a mutator that carries a probe is not a violation")
	}

	surveyor := WorkerCard()
	surveyor.Commands.Produces = []string{"finding"}
	surveyor.Inspect.Probes = nil
	if surveyor.MutatesWithoutProbe() {
		t.Error("a card that never produces a patch owes no probe")
	}
	if err := surveyor.Validate(); err != nil {
		t.Errorf("a non-mutating card with no probe must still validate: %v", err)
	}
}

// TestVerifyIsEmpty is the slice 1 DoD for the verify accessor slice 3
// registration reads: empty when no check is named, present otherwise.
func TestVerifyIsEmpty(t *testing.T) {
	if !(VerifySpec{}).IsEmpty() {
		t.Error("a verify spec with no check must be empty")
	}
	if (VerifySpec{Check: "the build passes"}).IsEmpty() {
		t.Error("a verify spec that names a check is not empty")
	}
}
