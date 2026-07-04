package colony

import (
	"context"
	"fmt"
	"slices"
)

// Routing assumes a card fits. Sometimes none does, and that gap is where the
// queen breeds (doc 06 section 7). Spawning is deliberately rare and
// conservative: a colony that births an ant for every slightly novel phrasing
// becomes the generated-code sprawl the design warns against, so the queen
// spawns only when no existing ant clears a match floor, and the newborn is not
// a blank slate but a specialist seeded from the nearest memory cluster, born
// provisional until its first task verifies (D13).

// Cluster is the seed the queen breeds from: the densest knot of memories near
// a task embedding, carrying what SynthesizeCard needs to write a newborn's
// card. It is data the memory store (doc 07) hands back, kept minimal so the
// queen stays kernel-pure and holds no memory implementation.
type Cluster struct {
	ID       string      // stable cluster identity, part of the newborn's id
	Digest   string      // a short prose digest of the cluster, the summary seed
	Classes  []TaskClass // the classes the cluster's work falls under
	Signals  []string    // cheap string cues carried onto the newborn's card
	Check    string      // the dominant ant's verify check, copied so the newborn has a real V section
	Fixtures []string    // replay fixtures copied with the check
}

// MemoryCluster is the queen's only memory-facing dependency, used only when
// breeding (doc 06 section 7.2). NearestCluster finds the densest cluster near
// a task embedding; SeedNamespace copies that cluster's most-verified memories
// into the newborn's namespace as its starting pinned index.
type MemoryCluster interface {
	NearestCluster(ctx context.Context, embed []float32, k int) (Cluster, error)
	SeedNamespace(ctx context.Context, namespace string, cluster Cluster, pins int) error
}

// shouldSpawn decides whether a gap is real. An empty candidate set means the
// colony literally has no ant for this class of work, an automatic trigger. A
// non-empty set whose best match is still below the threshold is a judgment the
// threshold makes, sitting where genuinely new work lives, not where slightly
// unfamiliar phrasing lives. Without a memory to seed from the queen never
// spawns; a gap stretches an existing generalist instead.
func (q *Queen) shouldSpawn(brief TaskBrief, pool []CardRow) bool {
	if q.memory == nil {
		return false
	}
	if len(pool) == 0 {
		return true
	}
	best := 0.0
	for _, c := range pool {
		m := cosine(brief.Embed, c.SkillVec) + q.signalBonus(brief.Anchors, c.Signals)
		if m > best {
			best = m
		}
	}
	return best < q.cfg.SpawnMatchThreshold
}

// spawn births a provisional ant seeded from the memory cluster nearest the
// task. The newborn gets a synthesized card and a pinned index copied from the
// cluster's most-verified memories, does no work until Register admits it
// through the same verification gate as any card, and stays provisional until
// its first task verifies.
func (q *Queen) spawn(ctx context.Context, brief TaskBrief) (Card, error) {
	cluster, err := q.memory.NearestCluster(ctx, brief.Embed, q.cfg.SeedK)
	if err != nil {
		return Card{}, fmt.Errorf("nearest cluster: %w", err)
	}
	card := SynthesizeCard(brief, cluster)
	// Apprenticeship seeding copies, it does not move, so the parent cluster's
	// ants keep their knowledge and the newborn starts with a copy it diverges
	// from (doc 06 section 7.3).
	if err := q.memory.SeedNamespace(ctx, card.State.Namespace, cluster, q.cfg.SeedPins); err != nil {
		return Card{}, fmt.Errorf("seeding namespace %s: %w", card.State.Namespace, err)
	}
	card.Status = StatusProvisional
	if err := q.Register(ctx, card); err != nil {
		return Card{}, err
	}
	return card, nil
}

// SynthesizeCard writes the newborn's first card from the brief and the seed
// cluster. It composes the summary deterministically from the cluster digest
// and the brief goal rather than calling a model, because the queen issues no
// reasoning turns (doc 06 section 1); the Register step embeds that summary into
// the SkillVec the router will match on next time. The verify story is copied
// from the cluster's dominant ant so the newborn is born with a real V section
// and does not trip the no-registration-without-verification rule.
func SynthesizeCard(brief TaskBrief, cl Cluster) Card {
	class := brief.Class
	if class == "" {
		class = ClassGeneral
	}
	id := fmt.Sprintf("ant-%s-%s", cl.ID, class)

	tools := classTools[class]
	if len(tools) == 0 {
		tools = []string{"read"}
	}
	produces := []string{"reply", string(brief.Deliverable)}
	if brief.Deliverable == "" {
		produces = []string{"reply", "finding"}
	}

	inspect := InspectSpec{}
	if brief.Deliverable == KindPatch {
		// A mutator owes a read-only probe that shows what it would touch, or
		// Register refuses it (D4). Ensure the allowlist can read for the probe.
		if !slices.Contains(tools, "read") {
			tools = append([]string{"read"}, tools...)
		}
		inspect.Probes = []string{"read the target before editing it; the edit gate refuses blind writes"}
	}

	classes := cl.Classes
	if len(classes) == 0 {
		classes = []TaskClass{class}
	}

	return Card{
		ID:     id,
		Name:   id,
		Glyph:  "·",
		Accent: "worker",
		State: StateSpec{
			Namespace: id + "/main",
			Disk:      []string{".ari/sessions"},
		},
		Commands: CommandSpec{Accepts: []string{"prompt"}, Produces: produces},
		Inspect:  inspect,
		Render:   RenderSpec{Style: "markdown"},
		Verify:   VerifySpec{Fixtures: cl.Fixtures, Check: cl.Check},
		Discovery: DiscoverySpec{
			Summary: synthSummary(brief, cl),
			Classes: classes,
			Signals: cl.Signals,
		},
		Tier:   TierMid,
		Tools:  tools,
		Status: StatusProvisional,
	}
}

// synthSummary composes the newborn's one-paragraph description from the seed
// cluster's digest and the task that revealed the gap. It is what gets embedded,
// so it must read like a card summary a human wrote, grounded in the
// neighborhood the ant was born into.
func synthSummary(brief TaskBrief, cl Cluster) string {
	if cl.Digest != "" {
		return fmt.Sprintf("A provisional ant seeded from nearby memory to handle %s work. %s", brief.Class, cl.Digest)
	}
	return fmt.Sprintf("A provisional ant born to handle %s work like: %s", brief.Class, brief.Goal)
}

// spawnAssignment records that this routing decision birthed an ant. Spawned is
// the flag the ledger and the journal read; the reason names the newborn and
// why no existing ant sufficed, so a spawn is as auditable as any route.
func spawnAssignment(brief TaskBrief, newborn Card) Assignment {
	return Assignment{
		Task:    brief.TaskID,
		Ant:     newborn.ID,
		Tier:    newborn.Tier,
		Budget:  brief.Budget,
		Spawned: true,
		Reason: RoutingReason{
			Class:   brief.Class,
			Winner:  newborn.ID,
			Summary: fmt.Sprintf("spawned provisional ant %s for class %s: no existing ant cleared the match floor", newborn.ID, brief.Class),
		},
	}
}

// Promote moves a provisional ant to active after its first verified task, the
// one lifecycle transition M3 performs. Promotion is eval-gated: a verdict that
// did not pass leaves the ant provisional for another trial, and a card that is
// not provisional is left untouched, so promotion is idempotent and can only
// ever move a newborn forward on real evidence (D13, D23).
func (q *Queen) Promote(ctx context.Context, ant string, v Verdict) error {
	if !v.Pass {
		return nil
	}
	c, err := q.cards.Load(ctx, ant)
	if err != nil {
		return fmt.Errorf("promoting %s: %w", ant, err)
	}
	if c.Status != StatusProvisional {
		return nil
	}
	return q.cards.SetStatus(ctx, ant, StatusActive)
}
