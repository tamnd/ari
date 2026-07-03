package bus

import (
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
)

func ev(t event.Type) event.Event {
	e, _ := event.New(t, "s", "t", nil)
	return e
}

// TestLossyDropsAndCounts pins the D-lane contract: a full lossy
// subscriber loses the oldest events, the drops are counted, and the
// publisher never blocks.
func TestLossyDropsAndCounts(t *testing.T) {
	b := New()
	s := b.Subscribe(Lossy, 4)

	done := make(chan struct{})
	go func() {
		for range 100 {
			b.Publish(ev(event.TypeTextDelta))
		}
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("publisher blocked on a lossy lane")
	}

	if got := s.Dropped(); got != 96 {
		t.Errorf("dropped = %d, want 96", got)
	}
	// The 4 survivors are the newest 4.
	if n := len(s.ch); n != 4 {
		t.Errorf("buffered = %d, want 4", n)
	}
}

// TestMustDeliverBlocks pins the control-lane contract: the publisher
// waits for a slow subscriber instead of dropping.
func TestMustDeliverBlocks(t *testing.T) {
	b := New()
	s := b.Subscribe(MustDeliver, 1)

	published := make(chan struct{})
	go func() {
		b.Publish(ev(event.TypePermissionRequested)) // fills the buffer
		b.Publish(ev(event.TypePermissionRequested)) // blocks
		close(published)
	}()

	select {
	case <-published:
		t.Fatal("publish returned while the subscriber was full")
	case <-time.After(50 * time.Millisecond):
	}

	<-s.C // drain one; the blocked publish completes
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("publish never completed after drain")
	}
	if s.Dropped() != 0 {
		t.Errorf("must-deliver lane dropped %d events", s.Dropped())
	}
}

// TestCancelUnblocksPublisher pins that cancelling a must-deliver
// subscription releases a publisher stuck on it.
func TestCancelUnblocksPublisher(t *testing.T) {
	b := New()
	s := b.Subscribe(MustDeliver, 1)
	b.Publish(ev(event.TypeError))

	published := make(chan struct{})
	go func() {
		b.Publish(ev(event.TypeError))
		close(published)
	}()
	time.Sleep(20 * time.Millisecond)
	s.Cancel()
	select {
	case <-published:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock the publisher")
	}
}

// TestTypeFilter pins that a filtered subscription only sees its types.
func TestTypeFilter(t *testing.T) {
	b := New()
	s := b.Subscribe(MustDeliver, 8, event.TypeLedgerTurn)
	b.Publish(ev(event.TypeTextDelta))
	b.Publish(ev(event.TypeLedgerTurn))
	b.Publish(ev(event.TypeLog))

	got := <-s.C
	if got.Type != event.TypeLedgerTurn {
		t.Errorf("got %s, want %s", got.Type, event.TypeLedgerTurn)
	}
	if n := len(s.ch); n != 0 {
		t.Errorf("unexpected extra events: %d", n)
	}
}

// TestConcurrent hammers the broker from many publishers and subscribers
// under -race. Correctness bar: no panic, no deadlock, and a cancelled
// subscription stops receiving.
func TestConcurrent(t *testing.T) {
	b := New()
	var wg sync.WaitGroup

	for range 4 {
		s := b.Subscribe(Lossy, 8)
		wg.Go(func() {
			for {
				select {
				case <-s.C:
				case <-s.Done():
					return
				}
			}
		})
		time.AfterFunc(30*time.Millisecond, s.Cancel)
	}

	var pubs sync.WaitGroup
	for range 8 {
		pubs.Go(func() {
			for range 500 {
				b.Publish(ev(event.TypeTextDelta))
			}
		})
	}
	pubs.Wait()
	wg.Wait()
}
