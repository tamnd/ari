package colony

import (
	"context"
	"fmt"
	"math/rand/v2"
	"sync"
)

// Queen is the colony's router and spawner. She never holds a Provider, a
// Session, or a tool set, per doc 06 section 1: her only model-touching
// dependency is an Embedder, because intake and spawning need a task
// embedding, which is a local call and not a reasoning turn. Giving the
// queen a provider so she could "just decide this one with a quick model
// call" is the LLM-as-router failure the design is written against.
//
// M3 fills her out slice by slice. This slice gives her Register, the only
// path to a routable ant and the one place the no-registration-without-
// verification rule lives, because the queen is the only writer of a card
// row with a live status.
type Queen struct {
	cards  CardStore
	trails TrailStore
	memory MemoryCluster
	cfg    RouteConfig

	mu  sync.Mutex
	rng *rand.Rand
}

// NewQueen builds a queen over a card store with the default routing config.
// Registration needs only the cards; WithRouting wires the trail store and
// sampler the assignment pipeline needs. Later slices add the ledger reader,
// the journal, and the blackboard.
func NewQueen(cards CardStore) *Queen {
	return &Queen{
		cards: cards,
		cfg:   DefaultRouteConfig(),
		rng:   rand.New(rand.NewPCG(0x2085, 0xa71)),
	}
}

// WithRouting wires the routing dependencies: the trail store the Thompson
// draw reads and, optionally, a config and a seeded sampler for a deterministic
// test. It returns the queen so a caller can chain it onto NewQueen.
func (q *Queen) WithRouting(trails TrailStore, cfg RouteConfig, rng *rand.Rand) *Queen {
	q.trails = trails
	q.cfg = cfg
	if rng != nil {
		q.rng = rng
	}
	return q
}

// WithMemory wires the seed source the queen breeds from. Without it the queen
// never spawns: a gap in the colony stretches an existing generalist rather
// than birthing a blank ant, because a spawn with nothing to seed from is the
// cold, uninformed newborn D13's seeding rule exists to avoid.
func (q *Queen) WithMemory(memory MemoryCluster) *Queen {
	q.memory = memory
	return q
}

// Register admits a card into the colony. It validates the card, refuses any
// card whose Verify section is empty (D4), defaults a newborn to
// provisional, and upserts it through the card store. It is the only path to
// a routable ant.
//
// The verification rule is not ceremony: an ant whose output cannot be
// checked is exactly how one wrong belief poisons a colony (D12), so an ant
// that cannot say how its work is verified is refused before it can do any.
func (q *Queen) Register(ctx context.Context, c Card) error {
	if err := c.Validate(); err != nil {
		return fmt.Errorf("card %s invalid: %w", c.ID, err)
	}
	if c.Verify.IsEmpty() {
		return fmt.Errorf("card %s has no verification story, refusing (D4)", c.ID)
	}
	// A newborn ant enters provisional; it earns active on its first verified
	// task. M3 only ever moves provisional to active; archival is M4.
	if c.Status == "" {
		c.Status = StatusProvisional
	}
	return q.cards.Upsert(ctx, c)
}

// RegisterBuiltins registers every built-in ant, the population a fresh
// colony starts with. Each ships a real verify story, so all register clean.
func (q *Queen) RegisterBuiltins(ctx context.Context) error {
	for _, c := range Builtins() {
		if err := q.Register(ctx, c); err != nil {
			return err
		}
	}
	return nil
}
