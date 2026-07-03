package ant

import (
	"os"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

func TestWorkerCardValidates(t *testing.T) {
	card := WorkerCard()
	if err := card.Validate(); err != nil {
		t.Fatalf("the built-in worker must validate: %v", err)
	}
	// The V section must point at fixtures that exist; a card whose
	// verification story names a missing file is a story, not a test.
	for _, f := range card.Verify.Fixtures {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("verify fixture %s: %v", f, err)
		}
	}
	for _, tl := range card.Tools {
		switch tl {
		case "read", "find", "write", "edit", "sh", "fetch":
		default:
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
		{"no namespace", func(c *Card) { c.State.Namespace = "" }},
		{"no accepted kinds", func(c *Card) { c.Commands.Accepts = nil }},
		{"no probes", func(c *Card) { c.Inspect.Probes = nil }},
		{"no render style", func(c *Card) { c.Render.Style = "" }},
		{"no verify fixtures", func(c *Card) { c.Verify.Fixtures = nil }},
		{"no verify check", func(c *Card) { c.Verify.Check = "" }},
		{"no summary", func(c *Card) { c.Discovery.Summary = "" }},
		{"no classes", func(c *Card) { c.Discovery.Classes = nil }},
		{"no tier", func(c *Card) { c.Tier = "" }},
		{"no tools", func(c *Card) { c.Tools = nil }},
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
