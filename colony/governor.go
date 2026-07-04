package colony

import "sync"

// The colony runs under three mechanical ceilings, checked before the fact at
// every wake and claim, because a limit enforced after the spend is a limit
// already blown through (doc 09 section 13.1). The Governor owns the two that
// are colony state, concurrency and the transitive task count, and gates the
// third, budget, over numbers the ledger supplies, since the kernel imports no
// ledger package. All three degrade the colony toward its single-ant default
// rather than queueing invisibly: a refused wake leaves the goal open and
// leased on the board, a refused split runs serially, a breached worker stops.

// EventThrottle is the journal event a tripped ceiling emits, so the user sees
// the colony running narrower than the queen wanted (doc 09 section 13.3). The
// kernel names it; the wiring writes it.
const EventThrottle = "colony.throttle"

// CapConfig is the three ceilings' configuration, all per-session so a user with
// a big task can raise them deliberately and a user who never touches them gets
// conservative limits that keep a runaway from draining an API budget.
type CapConfig struct {
	MaxAwake         int   // colony.max_awake: concurrent colony workers
	MaxFanoutSession int   // colony.max_fanout_session: transitive task count per session
	BudgetReserve    int64 // remaining-token floor below which no new claim is allowed
}

// DefaultCapConfig is the shipping default: four concurrent workers, sixteen
// tasks per session, and no artificial budget reserve (the wiring sets one from
// the session budget to hold the tail back for the foreground). The defaults are
// conservative because the safe failure of a misjudged fan-out is a task that
// stays single-ant, never a session that blew three times its token budget.
func DefaultCapConfig() CapConfig {
	return CapConfig{MaxAwake: 4, MaxFanoutSession: 16, BudgetReserve: 0}
}

// Governor enforces the concurrency and fan-out ceilings and gates the budget
// floor. It is safe for concurrent use because colony workers wake, sleep, and
// post subtasks from their own goroutines.
type Governor struct {
	cfg     CapConfig
	journal JournalFunc

	mu     sync.Mutex
	awake  int
	fanout map[string]int // session -> transitive task count
}

// NewGovernor wires the governor with its ceilings and the journal seam a
// throttle is emitted through. A nil journal drops throttle events.
func NewGovernor(cfg CapConfig, journal JournalFunc) *Governor {
	return &Governor{cfg: cfg, journal: journal, fanout: map[string]int{}}
}

// Wake admits one colony worker to the awake set, or refuses when max_awake is
// already met and journals the deferral. The foreground ant never goes through
// here, so the cap counts only colony workers (and the consolidator when it is
// awake, whose tokens are real); a refused wake leaves the goal open on the
// board for the next wave (doc 09 sections 13.1 and 13.3).
func (g *Governor) Wake(session string) bool {
	g.mu.Lock()
	if g.awake >= g.cfg.MaxAwake {
		g.mu.Unlock()
		g.throttle(session)
		return false
	}
	g.awake++
	g.mu.Unlock()
	return true
}

// Sleep releases one awake slot when a worker finishes. It never drops below
// zero, so an extra Sleep from a double-finish is harmless.
func (g *Governor) Sleep() {
	g.mu.Lock()
	if g.awake > 0 {
		g.awake--
	}
	g.mu.Unlock()
}

// Awake reports how many colony workers hold a slot right now.
func (g *Governor) Awake() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.awake
}

// Admit adds n new tasks to a session's transitive count, or refuses the whole
// batch when it would cross max_fanout_session and journals the throttle. A
// worker decomposing its own task draws from the same session counter, so a
// recursive decomposition cannot amplify past the ceiling no matter how
// enthusiastic the model gets (doc 09 section 13.1). The batch is all-or-nothing
// so a split is never half-admitted.
func (g *Governor) Admit(session string, n int) bool {
	g.mu.Lock()
	if g.fanout[session]+n > g.cfg.MaxFanoutSession {
		g.mu.Unlock()
		g.throttle(session)
		return false
	}
	g.fanout[session] += n
	g.mu.Unlock()
	return true
}

// Fanout reports a session's current transitive task count.
func (g *Governor) Fanout(session string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.fanout[session]
}

// Close forgets a session's counter when its task graph reaches a terminal
// state, so a long-lived process does not accumulate dead sessions.
func (g *Governor) Close(session string) {
	g.mu.Lock()
	delete(g.fanout, session)
	g.mu.Unlock()
}

// ClaimAllowed reports whether a new claim may proceed given the session's
// remaining token budget. It refuses at or below the reserve floor, holding the
// tail back so the foreground can finish its conversation: finishing the user's
// conversation outranks finishing the colony's side quests, always (doc 09
// section 13.1). remaining is budget minus spend, which the ledger owns and the
// caller computes; the governor only compares it to the floor.
func (g *Governor) ClaimAllowed(remaining int64) bool {
	return remaining > g.cfg.BudgetReserve
}

// Breached reports whether a task has spent past its own budget, the mid-flight
// signal that terminates the worker through the loop. A zero or negative budget
// means unbudgeted, which never breaches, so the foreground's open-ended budget
// is never mistaken for an exhausted one.
func Breached(spent, budget int64) bool {
	return budget > 0 && spent > budget
}

// throttle emits the deferral event with the affected session, so the journal
// and the TUI ticker show the colony is narrower than the queen wanted.
func (g *Governor) throttle(session string) {
	if g.journal != nil {
		g.journal(EventThrottle, []string{session})
	}
}
