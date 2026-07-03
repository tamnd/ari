// Package ant is the population side of the colony: the card contract,
// the prompt assembly, and the runner that puts the agent loop behind
// the core's TurnRunner seam. M0 ships exactly one ant, the pi-shaped
// worker; everything here is written so a second ant is a row of data,
// never a new type (D3, D4).
package ant

import (
	"fmt"
	"time"
)

// ModelTier names a failover chain in the provider registry. A card
// carries a tier, never a model id, so a model swap is a config edit
// the card never sees (D17).
type ModelTier string

const (
	TierFrontier ModelTier = "frontier"
	TierMid      ModelTier = "mid"
	TierCheap    ModelTier = "cheap"
	TierLocal    ModelTier = "local"
)

// TaskClass is one coarse task class in the small controlled vocabulary
// the queen prefilters on before spending an embedding call (doc 06).
type TaskClass string

// CardStatus is the card's lifecycle position.
type CardStatus string

const (
	StatusProvisional CardStatus = "provisional"
	StatusActive      CardStatus = "active"
	StatusArchived    CardStatus = "archived"
)

// StateSpec is the S section: the state this ant owns. Not global soup;
// a named memory namespace plus any on-disk working state.
type StateSpec struct {
	Namespace string   `json:"namespace"`      // memory namespace, doc 07
	Disk      []string `json:"disk,omitempty"` // worklog, sidechain paths
}

// CommandSpec is the C section: the typed handoff kinds this ant takes
// in and produces.
type CommandSpec struct {
	Accepts  []string `json:"accepts"`
	Produces []string `json:"produces"`
}

// InspectSpec is the I section: read-only probes. Probe-before-mutate
// (D4) is enforced here: an ant that mutates must list an inspection
// that shows what it would touch before it touches it.
type InspectSpec struct {
	Probes []string `json:"probes"`
}

// RenderSpec is the R section: how this ant presents results, for the
// TUI and the --json stream.
type RenderSpec struct {
	Style string `json:"style"` // "markdown" for the worker
}

// VerifySpec is the V section: the verification story. No ant is
// registered without one (D4). Fixtures are replay sets the eval
// harness runs; Check names what gates the ant's output.
type VerifySpec struct {
	Fixtures []string `json:"fixtures"`
	Check    string   `json:"check"`
}

// DiscoverySpec is the D section, the routing-facing description.
// Summary is what gets embedded to SkillVec; Classes are the coarse
// prefilter; Signals are cheap string cues (globs, languages, symbols)
// the queen can match without an embedding call (doc 06 section 2.1).
type DiscoverySpec struct {
	Summary string      `json:"summary"`
	Classes []TaskClass `json:"classes"`
	Signals []string    `json:"signals"`
	Prefers []TaskClass `json:"prefers,omitempty"`
}

// CardStats is a denormalized fitness snapshot, refreshed at folding
// boundaries. The trail table (doc 06 section 8) is the source of
// truth from M4 on; this is a render cache.
type CardStats struct {
	Assigned         int       `json:"assigned"`
	Succeeded        int       `json:"succeeded"`
	Failed           int       `json:"failed"`
	TokensTotal      int64     `json:"tokens_total"`
	SuccessPerKToken float64   `json:"success_per_ktoken"`
	LastActive       time.Time `json:"last_active,omitzero"`
}

// Card is an ant's S/C/I/R/V/D contract (D4). It is data the queen
// routes on, a document a human reads, and a fixture the eval harness
// runs (D23). At rest it is a row; awake it parameterizes one goroutine
// holding one context window (doc 01 section 2.2).
type Card struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Glyph  string `json:"glyph"`  // one rune for the TUI accent, D18
	Accent string `json:"accent"` // semantic palette key, D18

	State     StateSpec     `json:"state"`
	Commands  CommandSpec   `json:"commands"`
	Inspect   InspectSpec   `json:"inspect"`
	Render    RenderSpec    `json:"render"`
	Verify    VerifySpec    `json:"verify"`
	Discovery DiscoverySpec `json:"discovery"`

	Tier  ModelTier `json:"tier"`
	Tools []string  `json:"tools"` // allowlist, subset of the D7 six

	Status  CardStatus `json:"status"`
	Born    time.Time  `json:"born,omitzero"`
	Revised time.Time  `json:"revised,omitzero"`
}

// Validate enforces the D4 floor: every letter of S/C/I/R/V/D must say
// something, and an ant with no verification story does not register.
func (c Card) Validate() error {
	switch {
	case c.ID == "" || c.Name == "":
		return fmt.Errorf("card: id and name are required")
	case c.State.Namespace == "":
		return fmt.Errorf("card %s: the S section needs a memory namespace", c.Name)
	case len(c.Commands.Accepts) == 0 || len(c.Commands.Produces) == 0:
		return fmt.Errorf("card %s: the C section needs accepted and produced kinds", c.Name)
	case len(c.Inspect.Probes) == 0:
		return fmt.Errorf("card %s: the I section needs at least one read-only probe", c.Name)
	case c.Render.Style == "":
		return fmt.Errorf("card %s: the R section needs a render style", c.Name)
	case len(c.Verify.Fixtures) == 0 || c.Verify.Check == "":
		return fmt.Errorf("card %s: no ant registers without a verification story (D4)", c.Name)
	case c.Discovery.Summary == "" || len(c.Discovery.Classes) == 0:
		return fmt.Errorf("card %s: the D section needs a summary and task classes", c.Name)
	case c.Tier == "":
		return fmt.Errorf("card %s: a model tier is required (D17)", c.Name)
	case len(c.Tools) == 0:
		return fmt.Errorf("card %s: an empty tool allowlist can do nothing", c.Name)
	}
	return nil
}

// WorkerCard is the one built-in ant M0 ships: the pi-shaped generalist
// that reads, edits, runs, and verifies inside one repository.
func WorkerCard() Card {
	return Card{
		ID:     "worker",
		Name:   "worker",
		Glyph:  "π",
		Accent: "worker",
		State: StateSpec{
			Namespace: "worker/main",
			Disk:      []string{".ari/sessions"},
		},
		Commands: CommandSpec{
			Accepts:  []string{"prompt"},
			Produces: []string{"reply", "patch"},
		},
		Inspect: InspectSpec{
			Probes: []string{
				"read the file before editing it; the edit gate refuses blind writes",
				"find the affected code before mutating it",
				"git status before and after a change",
			},
		},
		Render: RenderSpec{Style: "markdown"},
		Verify: VerifySpec{
			Fixtures: []string{"testdata/read_edit_verify.json"},
			Check:    "the replayed turn reads the file, applies the edit on disk, verifies it with sh, and finishes completed with every turn metered",
		},
		Discovery: DiscoverySpec{
			Summary: "A general-purpose coding ant. It takes a plain prompt about the " +
				"current repository, inspects with read and find, changes files with " +
				"edit and write, runs builds and tests with sh, and pulls references " +
				"with fetch. It verifies what it changes before calling anything done.",
			Classes: []TaskClass{"edit", "fix", "survey", "explain"},
			Signals: []string{"*", "*.go", "test", "build"},
		},
		Tier:   TierFrontier,
		Tools:  []string{"read", "find", "write", "edit", "sh", "fetch"},
		Status: StatusActive,
	}
}
