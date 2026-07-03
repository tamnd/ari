package core

import (
	"context"
	"sync"
)

// Asks tracks permission requests that are waiting on a human. The
// pipeline's resolver blocks in Wait during a turn; a client's Respond
// call delivers the answer. This is the seam slice 7 left open: the
// pipeline decides, the client resolves, and the two only meet here.
type Asks struct {
	mu      sync.Mutex
	pending map[askKey]chan RespondRequest
}

// askKey scopes a request id to its session. Each session's pipeline
// numbers its own requests, so ids alone collide across sessions.
type askKey struct {
	session SessionID
	request string
}

func newAsks() *Asks {
	return &Asks{pending: map[askKey]chan RespondRequest{}}
}

// Wait registers the request and blocks until Respond answers it or ctx
// ends, which is how a cancelled turn unwinds a prompt nobody answered.
func (a *Asks) Wait(ctx context.Context, s SessionID, request string) (RespondRequest, error) {
	key := askKey{session: s, request: request}
	ch := make(chan RespondRequest, 1)
	a.mu.Lock()
	a.pending[key] = ch
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		delete(a.pending, key)
		a.mu.Unlock()
	}()
	select {
	case r := <-ch:
		return r, nil
	case <-ctx.Done():
		return RespondRequest{}, ctx.Err()
	}
}

// deliver hands an answer to its waiter. An answer nobody is waiting
// for is an error the client sees, not a silent drop.
func (a *Asks) deliver(req RespondRequest) error {
	key := askKey{session: req.Session, request: req.RequestID}
	a.mu.Lock()
	ch, ok := a.pending[key]
	if ok {
		delete(a.pending, key)
	}
	a.mu.Unlock()
	if !ok {
		return Errf(ErrPermission, "no outstanding request %s on session %s", req.RequestID, req.Session)
	}
	ch <- req // buffered; the one send after the delete cannot block
	return nil
}
