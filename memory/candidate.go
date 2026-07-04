package memory

import (
	"fmt"
	"strings"

	"github.com/tamnd/ari/memory/sqlite"
)

// observationImportance is the write-time weight the harvester gives an
// automatic observation. It sits below a deliberate remember (the model sets
// that one) because an observation the loop noticed is a weaker signal than
// one the ant chose to record.
const observationImportance = 4

// Remember builds a deliberate memory candidate from a remember tool call. It
// requires a body, an importance in 1..10, and at least one anchor so a
// memory is always tied to something in the repo, and a reflection needs the
// evidence the store guard will check again. Every violation is refused with
// a model-facing reason, the first of the three no-evidence enforcement
// points (D11): here at the tool boundary, then the candidate write, then the
// fold.
func Remember(ns string, kind sqlite.Kind, body string, importance int, anchors []sqlite.Anchor, evidence []string, src sqlite.Source) (sqlite.Candidate, error) {
	if strings.TrimSpace(body) == "" {
		return sqlite.Candidate{}, fmt.Errorf("remember needs a body: say what to remember")
	}
	if importance < 1 || importance > 10 {
		return sqlite.Candidate{}, fmt.Errorf("remember importance must be between 1 and 10, got %d", importance)
	}
	if len(anchors) == 0 {
		return sqlite.Candidate{}, fmt.Errorf("remember needs at least one anchor: name the file, symbol, or command this memory rests on")
	}
	if kind == sqlite.KindReflection && len(evidence) == 0 {
		return sqlite.Candidate{}, fmt.Errorf("a reflection needs at least one piece of evidence: name the observations it rests on before recording it")
	}
	return sqlite.Candidate{
		Namespace: ns, Kind: kind, Body: body, Importance: importance,
		Anchors: anchors, Evidence: evidence, Source: src,
	}, nil
}

// CommandOutcome is one command execution the harvester watches: the command
// string and whether it failed.
type CommandOutcome struct {
	Command string
	Failed  bool
}

// Harvester turns a stream of command outcomes into observation candidates
// without the ant being asked. It watches for a command that fails and then
// succeeds, because the fix the ant found in between is knowledge it earned
// and is worth remembering before the same command bites again (doc 08
// section 2.3). It emits candidates only; the loop generates their ids and
// writes them to the pending table, so a harvested observation is no more
// recallable than a proposed one until a fold has weighed it.
type Harvester struct {
	ns     string
	src    sqlite.Source
	failed map[string]bool // command -> its last run failed and has not yet succeeded
}

// NewHarvester builds a harvester for one ant's namespace, stamping every
// candidate it emits with the ant's provenance.
func NewHarvester(ns string, src sqlite.Source) *Harvester {
	return &Harvester{ns: ns, src: src, failed: map[string]bool{}}
}

// Observe records one command outcome and returns an observation candidate
// when it completes a fail-then-succeed of the same command, else nil. A
// success with no prior failure is unremarkable, and a repeated failure just
// keeps the command marked until it eventually succeeds.
func (h *Harvester) Observe(o CommandOutcome) *sqlite.Candidate {
	if o.Failed {
		h.failed[o.Command] = true
		return nil
	}
	if !h.failed[o.Command] {
		return nil
	}
	delete(h.failed, o.Command)
	return &sqlite.Candidate{
		Namespace:  h.ns,
		Kind:       sqlite.KindObservation,
		Body:       fmt.Sprintf("%q failed and then succeeded; the fix applied in between is worth recalling before running it again", o.Command),
		Importance: observationImportance,
		Anchors:    []sqlite.Anchor{{Kind: "command", Ref: o.Command}},
		Source:     h.src,
	}
}
