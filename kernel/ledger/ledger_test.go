package ledger

import (
	"math"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/provider"
)

func TestMain(m *testing.M) { eval.Main(m) }

func TestCostPricesTheFourCountsSeparately(t *testing.T) {
	p := DefaultPrices()
	u := provider.Usage{Input: 1000, Output: 500, CacheRead: 2000, CacheWrite: 3000}
	// sonnet: in 3/M, out 15/M; reads 0.1x in, writes 1.25x in.
	want := 1000*3.0/1e6 + 500*15.0/1e6 + 2000*3.0*0.1/1e6 + 3000*3.0*1.25/1e6
	if got := p.Cost("claude-sonnet-5", u); math.Abs(got-want) > 1e-12 {
		t.Errorf("cost = %.10f, want %.10f", got, want)
	}
}

func TestUnknownAndLocalModelsCostZero(t *testing.T) {
	p := DefaultPrices()
	u := provider.Usage{Input: 1_000_000, Output: 1_000_000}
	if got := p.Cost("qwen3-coder:30b", u); got != 0 {
		t.Errorf("local model cost = %f, want 0", got)
	}
	if got := p.Cost("some-model-nobody-listed", u); got != 0 {
		t.Errorf("unknown model cost = %f, want 0", got)
	}
}

func TestRecordEmitsLedgerTurnWithCostAndCacheRate(t *testing.T) {
	var got []event.Event
	l := New(DefaultPrices(), WithSink(func(e event.Event) { got = append(got, e) }))

	// Turn one: a cold prompt, everything written to cache.
	l.Record(Row{
		Ant: "worker", Task: "t1", Session: "s1", Turn: "turn1",
		Provider: "anthropic", Model: "claude-sonnet-5", Tier: "frontier",
		Usage:      provider.Usage{Input: 50, Output: 20, CacheWrite: 1000},
		Wall:       200 * time.Millisecond,
		StopReason: "tool_use",
	})
	// Turn two: the unchanged prefix comes back as cache reads.
	l.Record(Row{
		Ant: "worker", Task: "t1", Session: "s1", Turn: "turn2",
		Provider: "anthropic", Model: "claude-sonnet-5", Tier: "frontier",
		Usage:      provider.Usage{Input: 30, Output: 40, CacheRead: 1000},
		Wall:       150 * time.Millisecond,
		StopReason: "end_turn",
	})

	if len(got) != 2 {
		t.Fatalf("events = %d, want 2", len(got))
	}
	var first, second event.LedgerTurn
	if err := got[0].Decode(&first); err != nil {
		t.Fatalf("decode first: %v", err)
	}
	if err := got[1].Decode(&second); err != nil {
		t.Fatalf("decode second: %v", err)
	}
	if got[0].Type != event.TypeLedgerTurn || got[0].Session != "s1" || got[0].Turn != "turn1" {
		t.Errorf("first envelope = %+v", got[0])
	}
	if first.CostUSD <= 0 {
		t.Error("a priced model's turn must have a nonzero cost")
	}
	if first.CacheRate != 0 {
		t.Errorf("turn one read nothing from cache, rate = %f", first.CacheRate)
	}
	// The DoD line: the hit rate is non-zero on a second turn with an
	// unchanged prefix (plan/01 slice 3).
	if second.CacheRate <= 0.9 {
		t.Errorf("turn two served its prefix from cache, rate = %f, want > 0.9", second.CacheRate)
	}
	if second.CacheRead != 1000 {
		t.Errorf("cache_read = %d, want 1000", second.CacheRead)
	}
}

func TestRollupsSumTheRightRows(t *testing.T) {
	l := New(DefaultPrices())
	l.Record(Row{Ant: "worker", Task: "t1", Session: "s1", Model: "claude-haiku-4-5",
		Usage: provider.Usage{Input: 100, Output: 10}, Wall: time.Second})
	l.Record(Row{Ant: "worker", Task: "t2", Session: "s1", Model: "claude-haiku-4-5",
		Usage: provider.Usage{Input: 200, Output: 20, CacheRead: 50}, Wall: time.Second})
	l.Record(Row{Ant: "scout", Task: "t2", Session: "s2", Model: "claude-haiku-4-5",
		Usage: provider.Usage{Input: 400, Output: 40}, Wall: time.Second})

	s1 := l.BySession("s1")
	if s1.InTokens != 300 || s1.OutTokens != 30 || s1.CacheRead != 50 || s1.Requests != 2 {
		t.Errorf("BySession(s1) = %+v", s1)
	}
	if s1.WallMS != 2000 {
		t.Errorf("WallMS = %d, want 2000", s1.WallMS)
	}
	t2 := l.ByTask("t2")
	if t2.InTokens != 600 || t2.Requests != 2 {
		t.Errorf("ByTask(t2) = %+v", t2)
	}
	scout := l.ByAnt("scout")
	if scout.Requests != 1 || scout.InTokens != 400 {
		t.Errorf("ByAnt(scout) = %+v", scout)
	}
	all := l.All()
	if all.Requests != 3 || all.InTokens != 700 {
		t.Errorf("All() = %+v", all)
	}
	if all.CostUSD <= 0 {
		t.Error("three haiku turns cost something")
	}
}

func TestByDayBucketsOnTheClock(t *testing.T) {
	day := int64(20_000) // an arbitrary unix day
	now := time.Unix(day*86_400+3600, 0)
	l := New(DefaultPrices(), WithClock(func() time.Time { return now }))
	l.Record(Row{Session: "s", Model: "claude-haiku-4-5", Usage: provider.Usage{Input: 10}})
	if got := l.ByDay(day); got.Requests != 1 {
		t.Errorf("ByDay(%d) = %+v, want the one row", day, got)
	}
	if got := l.ByDay(day + 1); got.Requests != 0 {
		t.Errorf("the next day must be empty, got %+v", got)
	}
}

// TestATurnStaysWithinItsBudget is the harness hookup the DoD names: the
// ledger sums a task's tokens and eval asserts them against a ceiling, so
// a token regression fails like any other test (D23, plan/01 section 6).
func TestATurnStaysWithinItsBudget(t *testing.T) {
	var turns []event.LedgerTurn
	l := New(DefaultPrices(), WithSink(func(e event.Event) {
		var lt event.LedgerTurn
		if err := e.Decode(&lt); err != nil {
			t.Fatalf("decode: %v", err)
		}
		turns = append(turns, lt)
	}))

	l.Record(Row{Task: "demo", Session: "s", Model: "claude-sonnet-5",
		Usage: provider.Usage{Input: 900, Output: 300, CacheWrite: 2000}})
	l.Record(Row{Task: "demo", Session: "s", Model: "claude-sonnet-5",
		Usage: provider.Usage{Input: 100, Output: 400, CacheRead: 2000}})

	var u eval.Usage
	for _, lt := range turns {
		u.Add(lt.Input, lt.Output, lt.CacheRead)
	}
	eval.WithinBudget(t, u, eval.Budget{
		Name:   "ledger-demo",
		Input:  5_000,
		Output: 1_000,
		Turns:  2,
	})
}

func TestCacheRateOnEmptyTotalsIsZeroNotNaN(t *testing.T) {
	var z Totals
	if r := z.CacheRate(); r != 0 || math.IsNaN(r) {
		t.Errorf("empty rate = %f, want 0", r)
	}
}
