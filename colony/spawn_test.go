package colony

import (
	"context"
	"fmt"
	"testing"
)

// fakeMemory is a seed source with one canned cluster. It records the
// embedding and k it was queried with and every namespace it was asked to
// seed, so a spawn test can prove the seed pointed at the task and copied the
// cluster into the newborn's namespace.
type fakeMemory struct {
	cluster    Cluster
	gotEmbed   []float32
	gotK       int
	seededNS   string
	seededPins int
}

func (m *fakeMemory) NearestCluster(_ context.Context, embed []float32, k int) (Cluster, error) {
	m.gotEmbed = embed
	m.gotK = k
	return m.cluster, nil
}
func (m *fakeMemory) SeedNamespace(_ context.Context, ns string, _ Cluster, pins int) error {
	m.seededNS = ns
	m.seededPins = pins
	return nil
}

// memCards is a map-backed CardStore that actually records upserts, so a test
// can read a spawned card back and watch a promotion flip its status.
type memCards struct{ cards map[string]Card }

func newMemCards() *memCards { return &memCards{cards: map[string]Card{}} }

func (m *memCards) Upsert(_ context.Context, c Card) error { m.cards[c.ID] = c; return nil }
func (m *memCards) Load(_ context.Context, id string) (Card, error) {
	c, ok := m.cards[id]
	if !ok {
		return Card{}, fmt.Errorf("no card %s", id)
	}
	return c, nil
}
func (m *memCards) List(_ context.Context) ([]CardRow, error) {
	var rows []CardRow
	for _, c := range m.cards {
		rows = append(rows, CardRow{
			ID: c.ID, Name: c.Name, Status: c.Status, Tier: c.Tier,
			Classes: c.Discovery.Classes, Tools: c.Tools, Signals: c.Discovery.Signals,
			Prefers: c.Discovery.Prefers,
		})
	}
	return rows, nil
}
func (m *memCards) SetStatus(_ context.Context, id string, status CardStatus) error {
	c := m.cards[id]
	c.Status = status
	m.cards[id] = c
	return nil
}

func seedCluster() Cluster {
	return Cluster{
		ID:       "c1",
		Digest:   "prior work touched the parser and its tests.",
		Classes:  []TaskClass{ClassEdit},
		Signals:  []string{"*.go"},
		Check:    "go test ./...",
		Fixtures: []string{"fixtures/c1"},
	}
}

func spawnBrief(class TaskClass, embed []float32, deliver Kind) TaskBrief {
	return TaskBrief{
		Header:      Header{ID: "b1", Kind: KindTaskBrief, From: "queen", TaskID: "t1"},
		Goal:        "handle a genuinely new kind of work",
		Class:       class,
		Deliverable: deliver,
		Embed:       embed,
	}
}

// TestSpawnFiresOnEmptyColony is the DoD that an empty candidate set, the case
// where the colony has no ant for a class at all, breeds one seeded from the
// nearest cluster and assigns it with Spawned true.
func TestSpawnFiresOnEmptyColony(t *testing.T) {
	store := newMemCards()
	mem := &fakeMemory{cluster: seedCluster()}
	q := NewQueen(store).WithMemory(mem)

	brief := spawnBrief(ClassEdit, []float32{1, 0}, KindPatch)
	a, err := q.Assign(context.Background(), brief)
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !a.Spawned {
		t.Fatal("assignment did not record a spawn on an empty colony")
	}
	if a.Ant != "ant-c1-edit" {
		t.Errorf("spawned ant id = %q, want ant-c1-edit", a.Ant)
	}
	born, err := store.Load(context.Background(), a.Ant)
	if err != nil {
		t.Fatalf("spawned ant was not registered: %v", err)
	}
	if born.Status != StatusProvisional {
		t.Errorf("newborn status = %q, want provisional", born.Status)
	}
	if mem.seededNS != born.State.Namespace {
		t.Errorf("seeded namespace = %q, want the newborn's %q", mem.seededNS, born.State.Namespace)
	}
}

// TestSeedPointsAtNearestCluster is the DoD that the seed points at the nearest
// cluster by the task embedding, with the configured breadth and pin count.
func TestSeedPointsAtNearestCluster(t *testing.T) {
	store := newMemCards()
	mem := &fakeMemory{cluster: seedCluster()}
	q := NewQueen(store).WithMemory(mem)
	embed := []float32{0.2, 0.9}

	if _, err := q.Assign(context.Background(), spawnBrief(ClassEdit, embed, KindPatch)); err != nil {
		t.Fatalf("assign: %v", err)
	}
	if len(mem.gotEmbed) != 2 || mem.gotEmbed[0] != 0.2 || mem.gotEmbed[1] != 0.9 {
		t.Errorf("cluster queried with %v, want the task embedding %v", mem.gotEmbed, embed)
	}
	if mem.gotK != DefaultRouteConfig().SeedK {
		t.Errorf("seed k = %d, want %d", mem.gotK, DefaultRouteConfig().SeedK)
	}
	if mem.seededPins != DefaultRouteConfig().SeedPins {
		t.Errorf("seed pins = %d, want %d", mem.seededPins, DefaultRouteConfig().SeedPins)
	}
}

// TestSpawnFiresOnWeakMatch is the DoD's judgment case: a candidate exists and
// passes the tool and class filter, but its match is below the threshold, so
// the gap is real and the queen breeds rather than route to a poor fit.
func TestSpawnFiresOnWeakMatch(t *testing.T) {
	// An existing edit ant whose skill vector is orthogonal to the task.
	weak := row("stale", []TaskClass{ClassEdit}, []string{"edit"}, []float32{0, 1})
	mem := &fakeMemory{cluster: seedCluster()}
	q := NewQueen(stubCards{rows: []CardRow{weak}}).WithMemory(mem)

	a, err := q.Assign(context.Background(), spawnBrief(ClassEdit, []float32{1, 0}, KindPatch))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if !a.Spawned {
		t.Errorf("winner = %s, want a spawn: the only candidate's match is below the floor", a.Ant)
	}
}

// TestNoSpawnWhenAnAntFits is the other side: an ant whose vector matches the
// task clears the floor, so no gap exists and the queen routes to it, never
// breeding a near-duplicate.
func TestNoSpawnWhenAnAntFits(t *testing.T) {
	good := row("fit", []TaskClass{ClassEdit}, []string{"edit"}, []float32{1, 0})
	mem := &fakeMemory{cluster: seedCluster()}
	q := NewQueen(stubCards{rows: []CardRow{good}}).WithMemory(mem)

	a, err := q.Assign(context.Background(), spawnBrief(ClassEdit, []float32{1, 0}, KindPatch))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}
	if a.Spawned {
		t.Error("queen bred a near-duplicate when an ant already fit the task")
	}
	if a.Ant != "fit" {
		t.Errorf("winner = %s, want fit", a.Ant)
	}
}

// TestNoMemoryNoSpawn is the DoD that spawning needs a seed source: without a
// memory wired, a gap does not silently birth a blank ant, it surfaces as no
// eligible candidate so a caller can stretch a generalist.
func TestNoMemoryNoSpawn(t *testing.T) {
	q := NewQueen(stubCards{})
	_, err := q.Assign(context.Background(), spawnBrief(ClassEdit, []float32{1, 0}, KindPatch))
	if err == nil {
		t.Fatal("expected no-eligible-ant error when there is nothing to route and no seed source")
	}
}

// TestPromotionIsEvalGated is the DoD that a spawned ant that completes its
// first task verified is promoted to active, and a failing verdict leaves it
// provisional for another trial.
func TestPromotionIsEvalGated(t *testing.T) {
	store := newMemCards()
	mem := &fakeMemory{cluster: seedCluster()}
	q := NewQueen(store).WithMemory(mem)

	a, err := q.Assign(context.Background(), spawnBrief(ClassEdit, []float32{1, 0}, KindPatch))
	if err != nil {
		t.Fatalf("assign: %v", err)
	}

	// A failing verdict does not promote.
	if err := q.Promote(context.Background(), a.Ant, Verdict{Pass: false}); err != nil {
		t.Fatalf("promote (fail): %v", err)
	}
	c, _ := store.Load(context.Background(), a.Ant)
	if c.Status != StatusProvisional {
		t.Errorf("status after a failed verdict = %q, want still provisional", c.Status)
	}

	// A passing verdict promotes to active.
	if err := q.Promote(context.Background(), a.Ant, Verdict{Pass: true}); err != nil {
		t.Fatalf("promote (pass): %v", err)
	}
	c, _ = store.Load(context.Background(), a.Ant)
	if c.Status != StatusActive {
		t.Errorf("status after a passed verdict = %q, want active", c.Status)
	}

	// Promotion is idempotent: a second passing verdict does not error or change
	// an already-active ant.
	if err := q.Promote(context.Background(), a.Ant, Verdict{Pass: true}); err != nil {
		t.Fatalf("promote (repeat): %v", err)
	}
}

// TestSynthesizeCardIsRegistrable proves the synthesized card clears the
// registration gate for both deliverable kinds: a finding ant and a patch ant,
// the latter carrying the read-only probe a mutator owes.
func TestSynthesizeCardIsRegistrable(t *testing.T) {
	cl := seedCluster()

	finder := SynthesizeCard(spawnBrief(ClassSurvey, []float32{1, 0}, KindFinding), cl)
	if err := finder.Validate(); err != nil {
		t.Errorf("finding ant does not validate: %v", err)
	}

	mutator := SynthesizeCard(spawnBrief(ClassEdit, []float32{1, 0}, KindPatch), cl)
	if err := mutator.Validate(); err != nil {
		t.Errorf("patch ant does not validate: %v", err)
	}
	if mutator.MutatesWithoutProbe() {
		t.Error("a spawned mutator was born without the read-only probe it owes (D4)")
	}
	if mutator.Verify.Check != cl.Check {
		t.Errorf("verify check = %q, want the cluster's %q", mutator.Verify.Check, cl.Check)
	}
}
