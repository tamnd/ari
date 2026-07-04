package core

import (
	"context"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// foldedColony builds a colony with one live memory row folded from two
// near-duplicate candidates, and returns the colony plus the row's id and
// namespace so a forget test has something real to archive.
func foldedColony(t *testing.T) (c *Colony, ns, id string) {
	t.Helper()
	reg := cheapRegistry(t, scripted.Response{
		Text:  "run make gen after editing the schema",
		Usage: provider.Usage{Output: 7},
		Stop:  "end_turn",
	})
	c = openColony(t, WithRegistry(reg))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	ns = "ant_worker"
	if err := c.memory.InsertCandidate(ctx, "c1", obsCand(ns, "run make gen after editing schema definition files", "schema.go")); err != nil {
		t.Fatal(err)
	}
	if err := c.memory.InsertCandidate(ctx, "c2", obsCand(ns, "make gen must run after editing schema definition files", "schema.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Fold(ctx); err != nil {
		t.Fatal(err)
	}
	hits, err := c.RecallMemory(ctx, ns, "gen", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("fold produced no recallable row to forget")
	}
	return c, ns, hits[0].ID
}

// awaitRequest reads the stream until a permission.requested arrives and returns
// its id.
func awaitRequest(t *testing.T, sub *Subscription) string {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case e := <-sub.C:
			if e.Type != event.TypePermissionRequested {
				continue
			}
			var req event.PermissionRequested
			if err := e.Decode(&req); err != nil {
				t.Fatal(err)
			}
			return req.ID
		case <-deadline:
			t.Fatal("no permission.requested reached the stream")
		}
	}
}

// answer delivers a decision to the blocked resolver, retrying until the waiter
// has registered, the same race Respond handles in the running loop.
func answer(t *testing.T, c *Colony, s SessionID, reqID string, d RespondChoice) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		err := c.Respond(context.Background(), RespondRequest{Session: s, RequestID: reqID, Decision: d})
		if err == nil {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("never delivered the answer: %v", err)
		case <-time.After(time.Millisecond):
		}
	}
}

// TestForgetMemoryAllowArchives: a forget from a client raises a real
// permission.requested on the stream, and an allow archives the row so it drops
// out of recall.
func TestForgetMemoryAllowArchives(t *testing.T) {
	c, ns, id := foldedColony(t)
	ctx := context.Background()
	sub, err := c.Events(ctx, EventFilter{Types: []event.Type{event.TypePermissionRequested}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	s := SessionID("s1")
	type result struct {
		archived bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		archived, err := c.ForgetMemory(ctx, s, ns, id)
		done <- result{archived, err}
	}()

	answer(t, c, s, awaitRequest(t, sub), Allow)

	r := <-done
	if r.err != nil {
		t.Fatalf("forget: %v", r.err)
	}
	if !r.archived {
		t.Fatal("allow did not archive the row")
	}
	hits, err := c.RecallMemory(ctx, ns, "gen", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 0 {
		t.Fatalf("archived row still recalls: %+v", hits)
	}
}

// TestForgetMemoryDenyKeepsRow: a deny archives nothing and the row stays live.
func TestForgetMemoryDenyKeepsRow(t *testing.T) {
	c, ns, id := foldedColony(t)
	ctx := context.Background()
	sub, err := c.Events(ctx, EventFilter{Types: []event.Type{event.TypePermissionRequested}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	s := SessionID("s1")
	type result struct {
		archived bool
		err      error
	}
	done := make(chan result, 1)
	go func() {
		archived, err := c.ForgetMemory(ctx, s, ns, id)
		done <- result{archived, err}
	}()

	answer(t, c, s, awaitRequest(t, sub), Deny)

	r := <-done
	if r.err != nil {
		t.Fatalf("forget: %v", r.err)
	}
	if r.archived {
		t.Fatal("deny reported an archive")
	}
	hits, err := c.RecallMemory(ctx, ns, "gen", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("denied forget still dropped the row")
	}
}
