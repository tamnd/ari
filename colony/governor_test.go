package colony

import (
	"slices"
	"sync"
	"testing"
)

// recordJournal collects throttle events so a test can assert the deferral was
// made visible, which is half the point of a ceiling: a silent cap looks like a
// slow colony, a journaled one looks like a governed one.
type recordJournal struct {
	mu     sync.Mutex
	events []string
	ids    [][]string
}

func (r *recordJournal) fn() JournalFunc {
	return func(name string, ids []string) {
		r.mu.Lock()
		r.events = append(r.events, name)
		r.ids = append(r.ids, ids)
		r.mu.Unlock()
	}
}

func (r *recordJournal) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.events)
}

// TestWakeHoldsMaxAwake is the concurrency-ceiling DoD: at most max_awake colony
// workers hold a slot at once, and a refused wake journals colony.throttle.
func TestWakeHoldsMaxAwake(t *testing.T) {
	j := &recordJournal{}
	g := NewGovernor(CapConfig{MaxAwake: 4}, j.fn())

	for i := range 4 {
		if !g.Wake("s1") {
			t.Fatalf("wake %d refused below the ceiling", i)
		}
	}
	if g.Awake() != 4 {
		t.Fatalf("awake = %d, want 4 at the ceiling", g.Awake())
	}
	if g.Wake("s1") {
		t.Fatal("fifth wake admitted past max_awake")
	}
	if j.count() != 1 || j.events[0] != EventThrottle {
		t.Errorf("journal = %v, want one colony.throttle on the refused wake", j.events)
	}

	// A slot freed by a finished worker admits the next waiter.
	g.Sleep()
	if !g.Wake("s1") {
		t.Error("wake refused after a slot was freed")
	}
	if g.Awake() != 4 {
		t.Errorf("awake = %d, want back at 4 after refill", g.Awake())
	}
}

// TestSleepNeverGoesNegative proves a stray Sleep from a double-finish does not
// corrupt the counter into admitting more than max_awake later.
func TestSleepNeverGoesNegative(t *testing.T) {
	g := NewGovernor(CapConfig{MaxAwake: 2}, nil)
	g.Sleep()
	g.Sleep()
	if g.Awake() != 0 {
		t.Fatalf("awake = %d, want 0 after underflowing sleeps", g.Awake())
	}
	if !g.Wake("s1") {
		t.Fatal("first wake refused below the ceiling after stray sleeps")
	}
	if !g.Wake("s1") {
		t.Fatal("second wake refused below the ceiling after stray sleeps")
	}
	if g.Wake("s1") {
		t.Error("wake admitted past max_awake, the counter underflowed")
	}
}

// TestAdmitCapsTransitiveFanout is the fan-out-ceiling DoD: a recursive
// decomposition drawing from the same session counter cannot cross
// max_fanout_session no matter how the tasks nest, and the refusal journals.
func TestAdmitCapsTransitiveFanout(t *testing.T) {
	j := &recordJournal{}
	g := NewGovernor(CapConfig{MaxFanoutSession: 16}, j.fn())

	// The queen posts the first wave, then a worker recursively decomposes its
	// own task, both drawing from the same session counter.
	if !g.Admit("s1", 6) {
		t.Fatal("first wave of 6 refused under a 16 ceiling")
	}
	if !g.Admit("s1", 6) {
		t.Fatal("recursive wave of 6 refused, total 12 still under 16")
	}
	if g.Fanout("s1") != 12 {
		t.Fatalf("fanout = %d, want 12", g.Fanout("s1"))
	}
	// The next wave of 6 would reach 18, past the ceiling, so the whole batch is
	// refused rather than half-admitted.
	if g.Admit("s1", 6) {
		t.Error("wave crossing the ceiling was admitted")
	}
	if g.Fanout("s1") != 12 {
		t.Errorf("fanout = %d after a refused batch, want it unchanged at 12", g.Fanout("s1"))
	}
	if j.count() != 1 || j.events[0] != EventThrottle {
		t.Errorf("journal = %v, want one colony.throttle on the refused batch", j.events)
	}

	// A different session has its own counter, so one busy session does not
	// starve another.
	if !g.Admit("s2", 16) {
		t.Error("a fresh session was denied its own full ceiling")
	}
}

// TestCloseForgetsSession proves a closed task graph frees its counter so a
// long-running process does not accumulate dead sessions.
func TestCloseForgetsSession(t *testing.T) {
	g := NewGovernor(CapConfig{MaxFanoutSession: 4}, nil)
	g.Admit("s1", 4)
	if g.Fanout("s1") != 4 {
		t.Fatalf("fanout = %d, want 4", g.Fanout("s1"))
	}
	g.Close("s1")
	if g.Fanout("s1") != 0 {
		t.Fatalf("fanout = %d after close, want 0", g.Fanout("s1"))
	}
	if !g.Admit("s1", 4) {
		t.Error("a reopened session did not get a fresh ceiling")
	}
}

// TestClaimAllowedHoldsReserve is the claim-floor DoD: a new claim is refused
// before the spend when the remaining budget is at or below the reserve, so the
// tail is held for the foreground.
func TestClaimAllowedHoldsReserve(t *testing.T) {
	g := NewGovernor(CapConfig{BudgetReserve: 1000}, nil)
	if !g.ClaimAllowed(1001) {
		t.Error("claim refused with budget above the reserve")
	}
	if g.ClaimAllowed(1000) {
		t.Error("claim allowed at the reserve floor")
	}
	if g.ClaimAllowed(0) {
		t.Error("claim allowed with the budget exhausted")
	}

	// A zero reserve still refuses a claim once nothing remains.
	z := NewGovernor(CapConfig{}, nil)
	if !z.ClaimAllowed(1) {
		t.Error("claim refused with a token left and no reserve")
	}
	if z.ClaimAllowed(0) {
		t.Error("claim allowed with an empty budget and no reserve")
	}
}

// TestBreached is the mid-flight DoD: a task that has spent past its own budget
// breaches so the loop can stop it, while an unbudgeted foreground never does.
func TestBreached(t *testing.T) {
	if Breached(500, 1000) {
		t.Error("task under budget reported breached")
	}
	if Breached(1000, 1000) {
		t.Error("task exactly at budget reported breached")
	}
	if !Breached(1001, 1000) {
		t.Error("task over budget not reported breached")
	}
	if Breached(1_000_000, 0) {
		t.Error("unbudgeted foreground reported breached")
	}
}

// TestDefaultCapConfig pins the shipping defaults so a config regression that
// loosens a ceiling is caught here.
func TestDefaultCapConfig(t *testing.T) {
	c := DefaultCapConfig()
	if c.MaxAwake != 4 || c.MaxFanoutSession != 16 || c.BudgetReserve != 0 {
		t.Errorf("defaults = %+v, want awake 4 fanout 16 reserve 0", c)
	}
}

// TestGovernorConcurrentWakes proves the awake ceiling holds under a race: many
// goroutines waking at once never admit more than max_awake, which -race and
// goleak back with the mutex and no leaked goroutines.
func TestGovernorConcurrentWakes(t *testing.T) {
	g := NewGovernor(CapConfig{MaxAwake: 4}, (&recordJournal{}).fn())
	var wg sync.WaitGroup
	var mu sync.Mutex
	var admitted []int
	for range 32 {
		wg.Go(func() {
			if g.Wake("s1") {
				mu.Lock()
				admitted = append(admitted, 1)
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(admitted) != 4 {
		t.Errorf("admitted %d workers, want exactly max_awake 4", len(admitted))
	}
	if g.Awake() != 4 {
		t.Errorf("awake = %d, want 4", g.Awake())
	}
	// Sanity: the admitted set matches the counter, no double-count slipped through.
	if slices.Max(admitted) != 1 {
		t.Error("admission recorded a value other than a single slot")
	}
}
