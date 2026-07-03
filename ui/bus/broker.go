// Package bus bridges the core's event stream to the UI program (doc 02
// section 3). The core emits events on its own clock; the terminal
// renders on a much slower one; the broker decouples the two with
// bounded buffers and an explicit policy for what happens when a buffer
// fills. The UI can never wedge the agent, and the drop counter makes a
// struggling UI visible instead of hiding it.
package bus

import (
	"sync"
	"sync/atomic"
	"time"
)

// Lane selects delivery semantics. Lossy drops under pressure;
// MustDeliver blocks briefly and must not be coalesced away.
type Lane int

const (
	// Lossy is for latest-state-wins events: token deltas, reasoning
	// deltas, tool progress, cost ticks. Dropping one is safe because a
	// later event carries the full accumulated state.
	Lossy Lane = iota
	// MustDeliver is for terminal and semantically load-bearing events:
	// turn finished, tool result, permission request, error. A dropped
	// one would leave a spinner spinning or a tool blocked on a prompt
	// nobody sees.
	MustDeliver
)

// DefaultDeadline is the per-subscriber budget a must-deliver publish
// will block for before giving up on a wedged subscriber.
const DefaultDeadline = 50 * time.Millisecond

// Broker fans one stream out to N subscribers. It is generic so the same
// machinery carries every payload type; the UI subscribes with T =
// tea.Msg, and a future serve mode feeds an SSE encoder from the same
// type (doc 02 section 3.4).
type Broker[T any] struct {
	deadline time.Duration

	mu      sync.RWMutex
	subs    map[int]*Sub[T]
	nextID  int
	dropped atomic.Uint64
}

// Sub is one subscription. Read C; call Cancel when done.
type Sub[T any] struct {
	C      chan T
	cancel func()
}

// Cancel detaches the subscription from the broker.
func (s *Sub[T]) Cancel() { s.cancel() }

// New builds a broker with the default must-deliver deadline.
func New[T any]() *Broker[T] { return NewWithDeadline[T](DefaultDeadline) }

// NewWithDeadline builds a broker with an explicit must-deliver budget,
// for tests that cannot afford to wait the real one.
func NewWithDeadline[T any](d time.Duration) *Broker[T] {
	return &Broker[T]{deadline: d, subs: map[int]*Sub[T]{}}
}

// Subscribe attaches a subscriber with the given buffer size.
func (b *Broker[T]) Subscribe(buffer int) *Sub[T] {
	b.mu.Lock()
	defer b.mu.Unlock()
	id := b.nextID
	b.nextID++
	s := &Sub[T]{C: make(chan T, buffer)}
	s.cancel = func() {
		b.mu.Lock()
		delete(b.subs, id)
		b.mu.Unlock()
	}
	b.subs[id] = s
	return s
}

// Publish delivers v to every subscriber under the lane's semantics:
// Lossy skips a full buffer and counts the drop; MustDeliver blocks up
// to the deadline per subscriber and only then gives up.
func (b *Broker[T]) Publish(lane Lane, v T) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, s := range b.subs {
		switch lane {
		case Lossy:
			select {
			case s.C <- v:
			default:
				b.dropped.Add(1)
			}
		case MustDeliver:
			t := time.NewTimer(b.deadline)
			select {
			case s.C <- v:
				t.Stop()
			case <-t.C:
				// The subscriber is wedged past its budget. Giving up
				// here is what keeps the UI unable to stall the core.
			}
		}
	}
}

// Dropped returns the lossy drop count so the sidebar debug toggle and
// ari doctor can surface a struggling UI (doc 02 section 3.2).
func (b *Broker[T]) Dropped() uint64 { return b.dropped.Load() }
