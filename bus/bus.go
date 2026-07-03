// Package bus is the in-process event broker between the core and its
// clients. Publishing never blocks the agent loop: streaming lanes are
// lossy under a slow subscriber, control lanes are not.
package bus

import (
	"sync"
	"sync/atomic"

	"github.com/tamnd/ari/event"
)

// Lane picks the delivery contract for a subscription.
type Lane int

const (
	// Lossy drops the oldest buffered event when a subscriber falls
	// behind. Streaming deltas ride this lane; a stalled TUI must never
	// stall the agent.
	Lossy Lane = iota
	// MustDeliver blocks the publisher until the subscriber drains.
	// Permission requests and terminal events ride this lane; dropping
	// one would wedge a turn.
	MustDeliver
)

// Sub is one subscription. Receive from C in a select that also watches
// Done. C is never closed (a publisher may be mid-send when the
// subscription is cancelled); Done closing is the termination signal.
type Sub struct {
	C      <-chan event.Event
	ch     chan event.Event
	lane   Lane
	types  map[event.Type]bool // nil means all
	drops  atomic.Uint64
	b      *Bus
	once   sync.Once
	closed chan struct{}
}

// Done closes when the subscription is cancelled.
func (s *Sub) Done() <-chan struct{} { return s.closed }

// Dropped reports how many events this subscription lost to backpressure.
// Always zero on a MustDeliver lane.
func (s *Sub) Dropped() uint64 { return s.drops.Load() }

// Cancel removes the subscription. Events already buffered in C stay
// readable; no new ones arrive after Done closes.
func (s *Sub) Cancel() {
	s.once.Do(func() {
		s.b.mu.Lock()
		for i, x := range s.b.subs {
			if x == s {
				s.b.subs = append(s.b.subs[:i], s.b.subs[i+1:]...)
				break
			}
		}
		s.b.mu.Unlock()
		close(s.closed)
	})
}

// Bus fans events out to subscribers.
type Bus struct {
	mu   sync.Mutex
	subs []*Sub
}

// New returns an empty broker.
func New() *Bus { return &Bus{} }

// Subscribe registers a subscriber. buffer is the channel depth; types
// filters delivery to the listed event types, or everything when empty.
func (b *Bus) Subscribe(lane Lane, buffer int, types ...event.Type) *Sub {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan event.Event, buffer)
	s := &Sub{C: ch, ch: ch, lane: lane, b: b, closed: make(chan struct{})}
	if len(types) > 0 {
		s.types = make(map[event.Type]bool, len(types))
		for _, t := range types {
			s.types[t] = true
		}
	}
	b.mu.Lock()
	b.subs = append(b.subs, s)
	b.mu.Unlock()
	return s
}

// Publish delivers ev to every matching subscriber under its lane's
// contract. On a Lossy lane a full buffer sheds its oldest event and the
// drop is counted; on a MustDeliver lane Publish waits until the
// subscriber drains or cancels.
func (b *Bus) Publish(ev event.Event) {
	b.mu.Lock()
	subs := make([]*Sub, len(b.subs))
	copy(subs, b.subs)
	b.mu.Unlock()

	for _, s := range subs {
		if s.types != nil && !s.types[ev.Type] {
			continue
		}
		switch s.lane {
		case MustDeliver:
			select {
			case s.ch <- ev:
			case <-s.closed:
			}
		default:
			select {
			case s.ch <- ev:
			case <-s.closed:
			default:
				// Full: shed the oldest, then try once more. If a
				// concurrent receive already made room, nothing sheds.
				select {
				case <-s.ch:
					s.drops.Add(1)
				default:
				}
				select {
				case s.ch <- ev:
				default:
					// Lost the race again; drop the new event instead
					// of looping against a live consumer.
					s.drops.Add(1)
				}
			}
		}
	}
}
