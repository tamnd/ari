package permission

import (
	"context"
	"sync"
	"testing"
)

// TestAllowSessionViaResolver: a resolver that answers "allow for
// session" adds the suggested rule, so the identical call next time is
// allowed by rule without asking anyone.
func TestAllowSessionViaResolver(t *testing.T) {
	p := newPipeline(t, ModeAsk, nil, nil, nil)
	asked := 0
	p.Resolver = ResolverFunc(func(_ context.Context, req *Request) (Resolution, bool) {
		asked++
		rules, err := ParseAll(req.Suggestions, LayerSession)
		if err != nil {
			t.Fatalf("suggestions did not parse: %v", err)
		}
		p.AddAllow(rules...)
		return Resolution{Behavior: Allow}, true
	})

	first := p.Decide(context.Background(), shCall(p, "go test ./..."))
	if first.Behavior != Allow {
		t.Fatalf("first call = %s, want allow via resolver", first.Behavior)
	}
	second := p.Decide(context.Background(), shCall(p, "go test ./ui"))
	if second.Behavior != Allow {
		t.Fatalf("second call = %s, want allow via the session rule", second.Behavior)
	}
	if second.Reason.Stage != StageAllow {
		t.Fatalf("second call decided at %s, want the allow-rule stage", second.Reason.Stage)
	}
	if asked != 1 {
		t.Fatalf("resolver ran %d times, want once", asked)
	}
}

// TestResolverDenyStands: a deny answer is final for that call and
// writes no rule.
func TestResolverDenyStands(t *testing.T) {
	p := newPipeline(t, ModeAsk, nil, nil, nil)
	p.Resolver = ResolverFunc(func(context.Context, *Request) (Resolution, bool) {
		return Resolution{Behavior: Deny, Message: "the user denied this call"}, true
	})
	d := p.Decide(context.Background(), shCall(p, "rm -rf build"))
	if d.Behavior != Deny {
		t.Fatalf("decision = %s, want deny", d.Behavior)
	}
	if len(p.ruleset().Allow) != 0 {
		t.Fatal("a deny answer must not write a rule")
	}
}

// TestAddAllowRaces: concurrent Decide calls while AddAllow appends,
// which is exactly what a parallel read batch does when the user
// answers "allow for session" on one of them. Run under -race.
func TestAddAllowRaces(t *testing.T) {
	p := newPipeline(t, ModeAsk, nil, nil, []string{"sh(ls:*)"})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 50 {
				p.Decide(context.Background(), shCall(p, "ls -la"))
			}
		})
	}
	for range 50 {
		p.AddAllow(MustParse("sh(go test:*)", LayerSession))
	}
	wg.Wait()
}
