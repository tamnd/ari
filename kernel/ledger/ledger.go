// Package ledger is where token counts become money and budgets. Every
// provider turn produces exactly one row, cost is computed at record time
// from the price table in force, and every roll-up reads from those rows
// (doc 10 section 9). M0 keeps rows in memory behind the same API the
// SQLite backend will satisfy when colony.db lands; the ledger.turn event
// is the part clients consume and it is final from this slice.
package ledger

import (
	"strconv"
	"sync"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
)

// Row is one provider turn's accounting, sent by the loop when a turn ends.
type Row struct {
	Ant, Task, Session    string
	Turn                  string
	Provider, Model, Tier string
	Usage                 provider.Usage
	Wall                  time.Duration
	Estimated             bool
	StopReason            string
}

// Price is per-million-token pricing for one model. Cache multipliers are
// applied to the input rate: read = 0.1x, write = 1.25x for the 5-minute
// TTL (the shipped default; 1h doubles it, doc 10 section 9.4).
type Price struct {
	InPerM, OutPerM float64
}

// Prices maps model ids to prices. An unknown model costs zero, which is
// how local models stay honest in the totals without listing every tag.
type Prices struct {
	byModel   map[string]Price
	writeMult float64
}

// DefaultPrices is the compiled-in table, current as of doc 10.
func DefaultPrices() Prices {
	return Prices{
		writeMult: 1.25,
		byModel: map[string]Price{
			"claude-fable-5":    {InPerM: 10.00, OutPerM: 50.00},
			"claude-opus-4-8":   {InPerM: 5.00, OutPerM: 25.00},
			"claude-opus-4-7":   {InPerM: 5.00, OutPerM: 25.00},
			"claude-opus-4-6":   {InPerM: 5.00, OutPerM: 25.00},
			"claude-sonnet-5":   {InPerM: 3.00, OutPerM: 15.00},
			"claude-sonnet-4-6": {InPerM: 3.00, OutPerM: 15.00},
			"claude-haiku-4-5":  {InPerM: 1.00, OutPerM: 5.00},
			// local models cost nothing; listing them keeps intent visible
			"qwen3-coder:30b":  {},
			"qwen3:8b":         {},
			"nomic-embed-text": {},
		},
	}
}

// Cost prices one turn: uncached input at the input rate, output at the
// output rate, cache reads at 0.1x input, cache writes at the TTL
// multiplier. The four counts are never pre-summed (doc 10 section 9.1).
func (p Prices) Cost(model string, u provider.Usage) float64 {
	pr, ok := p.byModel[model]
	if !ok {
		return 0
	}
	const perM = 1.0 / 1_000_000
	in := float64(u.Input) * pr.InPerM * perM
	out := float64(u.Output) * pr.OutPerM * perM
	read := float64(u.CacheRead) * pr.InPerM * 0.1 * perM
	write := float64(u.CacheWrite) * pr.InPerM * p.writeMult * perM
	return in + out + read + write
}

// Totals is a roll-up over some slice of the ledger.
type Totals struct {
	InTokens, OutTokens, CacheRead, CacheWrite int64
	CostUSD                                    float64
	Requests                                   int64
	WallMS                                     int64
}

// CacheRate is the share of the prompt served from cache: reads over the
// full prompt (uncached input + reads + writes). Zero when nothing ran.
func (t Totals) CacheRate() float64 {
	prompt := t.InTokens + t.CacheRead + t.CacheWrite
	if prompt == 0 {
		return 0
	}
	return float64(t.CacheRead) / float64(prompt)
}

type stamped struct {
	Row
	cost float64
	ts   time.Time
}

// Sink receives the ledger.turn event for each recorded row. The colony
// points this at the journal so the meter reaches every subscriber.
type Sink func(event.Event)

// Ledger records rows and answers roll-ups. Safe for concurrent use.
type Ledger struct {
	mu     sync.Mutex
	rows   []stamped
	prices Prices
	sink   Sink
	now    func() time.Time
}

// Option adjusts a Ledger at construction.
type Option func(*Ledger)

// WithSink installs the event sink ledger.turn is delivered to.
func WithSink(s Sink) Option { return func(l *Ledger) { l.sink = s } }

// WithClock replaces the wall clock, for deterministic tests.
func WithClock(now func() time.Time) Option { return func(l *Ledger) { l.now = now } }

// New builds a ledger over the given price table.
func New(prices Prices, opts ...Option) *Ledger {
	l := &Ledger{prices: prices, now: time.Now}
	for _, o := range opts {
		o(l)
	}
	return l
}

// Record accounts one turn: computes cost under the prices in force,
// stores the row, and emits ledger.turn. It never blocks on storage.
func (l *Ledger) Record(r Row) {
	l.mu.Lock()
	s := stamped{Row: r, cost: l.prices.Cost(r.Model, r.Usage), ts: l.now()}
	l.rows = append(l.rows, s)
	sink := l.sink
	l.mu.Unlock()

	if sink == nil {
		return
	}
	u := r.Usage
	one := Totals{
		InTokens: int64(u.Input), CacheRead: int64(u.CacheRead), CacheWrite: int64(u.CacheWrite),
	}
	ev, err := event.New(event.TypeLedgerTurn, r.Session, r.Turn, event.LedgerTurn{
		Turn:       r.Turn,
		Model:      r.Model,
		Input:      int64(u.Input),
		Output:     int64(u.Output),
		CacheRead:  int64(u.CacheRead),
		CacheWrite: int64(u.CacheWrite),
		CostUSD:    s.cost,
		CacheRate:  one.CacheRate(),
	})
	if err != nil {
		return // a fixed payload shape cannot fail to marshal
	}
	sink(ev)
}

func (l *Ledger) sum(keep func(stamped) bool) Totals {
	l.mu.Lock()
	defer l.mu.Unlock()
	var t Totals
	for _, s := range l.rows {
		if !keep(s) {
			continue
		}
		t.InTokens += int64(s.Usage.Input)
		t.OutTokens += int64(s.Usage.Output)
		t.CacheRead += int64(s.Usage.CacheRead)
		t.CacheWrite += int64(s.Usage.CacheWrite)
		t.CostUSD += s.cost
		t.Requests++
		t.WallMS += s.Wall.Milliseconds()
	}
	return t
}

// BySession rolls up one session's turns.
func (l *Ledger) BySession(id string) Totals {
	return l.sum(func(s stamped) bool { return s.Session == id })
}

// ByTask rolls up one task's turns.
func (l *Ledger) ByTask(id string) Totals {
	return l.sum(func(s stamped) bool { return s.Task == id })
}

// ByAnt rolls up one ant's turns, the number the sidebar shows.
func (l *Ledger) ByAnt(id string) Totals {
	return l.sum(func(s stamped) bool { return s.Ant == id })
}

// ByDay rolls up one UTC day, given as unix seconds / 86400.
func (l *Ledger) ByDay(unixDay int64) Totals {
	return l.sum(func(s stamped) bool { return s.ts.Unix()/86_400 == unixDay })
}

// All rolls up everything recorded.
func (l *Ledger) All() Totals {
	return l.sum(func(stamped) bool { return true })
}

// Len reports how many rows have been recorded, for tests.
func (l *Ledger) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.rows)
}

// FormatUSD renders a cost the way the sidebar shows it: "$0.0123".
func FormatUSD(v float64) string {
	return "$" + strconv.FormatFloat(v, 'f', 4, 64)
}
