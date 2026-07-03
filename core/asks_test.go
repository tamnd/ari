package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestAskRoundTrip: a waiter blocks, Respond-shaped delivery unblocks
// it with the decision intact.
func TestAskRoundTrip(t *testing.T) {
	a := newAsks()
	got := make(chan RespondRequest, 1)
	go func() {
		r, err := a.Wait(context.Background(), "s1", "perm1")
		if err != nil {
			t.Errorf("wait: %v", err)
		}
		got <- r
	}()

	// The waiter registers asynchronously; deliver retries until it is
	// there, the same way a human answer always arrives after the ask.
	deadline := time.After(2 * time.Second)
	for {
		err := a.deliver(RespondRequest{Session: "s1", RequestID: "perm1", Decision: AllowSession})
		if err == nil {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("deliver never found the waiter: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
	if r := <-got; r.Decision != AllowSession {
		t.Fatalf("decision = %q, want allow_session", r.Decision)
	}
}

// TestAskNoWaiter: answering a request nobody is waiting on is an error
// the client sees, not a silent drop.
func TestAskNoWaiter(t *testing.T) {
	a := newAsks()
	err := a.deliver(RespondRequest{Session: "s1", RequestID: "ghost", Decision: Deny})
	if err == nil {
		t.Fatal("deliver with no waiter must error")
	}
	if KindOf(err) != ErrPermission {
		t.Fatalf("error kind = %s, want permission", KindOf(err))
	}
}

// TestAskCancelUnblocks: a cancelled turn unwinds a prompt nobody
// answered, and the request is deregistered afterwards.
func TestAskCancelUnblocks(t *testing.T) {
	a := newAsks()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := a.Wait(ctx, "s1", "perm2")
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("wait returned %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("wait did not unblock on cancel")
	}
	if err := a.deliver(RespondRequest{Session: "s1", RequestID: "perm2"}); err == nil {
		t.Fatal("the cancelled request must be deregistered")
	}
}

// TestSessionsScopeRequestIDs: the same request id on two sessions is
// two distinct waits, because each session's pipeline numbers its own.
func TestSessionsScopeRequestIDs(t *testing.T) {
	a := newAsks()
	one := make(chan RespondRequest, 1)
	two := make(chan RespondRequest, 1)
	for _, w := range []struct {
		s  SessionID
		ch chan RespondRequest
	}{{"s1", one}, {"s2", two}} {
		go func() {
			r, err := a.Wait(context.Background(), w.s, "perm1")
			if err != nil {
				t.Errorf("wait %s: %v", w.s, err)
			}
			w.ch <- r
		}()
	}
	deliver := func(s SessionID, d RespondChoice) {
		deadline := time.After(2 * time.Second)
		for a.deliver(RespondRequest{Session: s, RequestID: "perm1", Decision: d}) != nil {
			select {
			case <-deadline:
				t.Fatalf("no waiter appeared for %s", s)
			case <-time.After(time.Millisecond):
			}
		}
	}
	deliver("s1", Allow)
	deliver("s2", Deny)
	if r := <-one; r.Decision != Allow {
		t.Fatalf("s1 got %q, want allow", r.Decision)
	}
	if r := <-two; r.Decision != Deny {
		t.Fatalf("s2 got %q, want deny", r.Decision)
	}
}
