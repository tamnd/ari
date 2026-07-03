package ant

import (
	"context"
	"errors"
	"testing"

	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/permission"
)

// canned answers Wait with a fixed decision.
type canned struct {
	decision core.RespondChoice
	err      error
	sawKey   string
}

func (c *canned) Wait(_ context.Context, s core.SessionID, request string) (core.RespondRequest, error) {
	c.sawKey = string(s) + "/" + request
	return core.RespondRequest{Session: s, RequestID: request, Decision: c.decision}, c.err
}

func askReq() *permission.Request {
	return &permission.Request{ID: "perm1", Suggestions: []string{"sh(go test:*)"}}
}

// TestResolveAllow: a plain allow answers the one call and writes no rule.
func TestResolveAllow(t *testing.T) {
	w := &worker{session: "s1", asks: &canned{decision: core.Allow}, pipe: &permission.Pipeline{}}
	res, ok := w.resolve(context.Background(), askReq())
	if !ok || res.Behavior != permission.Allow {
		t.Fatalf("resolve = %+v ok=%v, want allow", res, ok)
	}
	if w.asks.(*canned).sawKey != "s1/perm1" {
		t.Fatalf("waited on %s, want s1/perm1", w.asks.(*canned).sawKey)
	}
}

// TestResolveAllowSession: the session answer allows the call and adds
// the suggested rule to the session's pipeline.
func TestResolveAllowSession(t *testing.T) {
	pipe := &permission.Pipeline{}
	w := &worker{session: "s1", asks: &canned{decision: core.AllowSession}, pipe: pipe}
	res, ok := w.resolve(context.Background(), askReq())
	if !ok || res.Behavior != permission.Allow {
		t.Fatalf("resolve = %+v ok=%v, want allow", res, ok)
	}
	if len(pipe.Rules.Allow) != 1 || pipe.Rules.Allow[0].Layer != permission.LayerSession {
		t.Fatalf("session rule not added: %+v", pipe.Rules.Allow)
	}
}

// TestResolveDeny: the deny answer carries a message the model reads.
func TestResolveDeny(t *testing.T) {
	w := &worker{session: "s1", asks: &canned{decision: core.Deny}, pipe: &permission.Pipeline{}}
	res, ok := w.resolve(context.Background(), askReq())
	if !ok || res.Behavior != permission.Deny || res.Message == "" {
		t.Fatalf("resolve = %+v ok=%v, want deny with a message", res, ok)
	}
}

// TestResolveAbstains: no asker, or a wait that errors (a cancelled
// turn), abstains so the standing Ask becomes the loop's refusal.
func TestResolveAbstains(t *testing.T) {
	for name, w := range map[string]*worker{
		"nil asker": {session: "s1", pipe: &permission.Pipeline{}},
		"wait err":  {session: "s1", asks: &canned{err: errors.New("cancelled")}, pipe: &permission.Pipeline{}},
	} {
		if _, ok := w.resolve(context.Background(), askReq()); ok {
			t.Errorf("%s: resolve answered, want abstain", name)
		}
	}
}
