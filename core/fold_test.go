package core

import (
	"context"
	"testing"

	"github.com/tamnd/ari/event"
	memsqlite "github.com/tamnd/ari/memory/sqlite"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// cheapRegistry wires a scripted provider as the whole cheap tier, the model
// the fold runs on. Each response answers one Summarize call in order.
func cheapRegistry(t *testing.T, responses ...scripted.Response) *provider.Registry {
	t.Helper()
	reg := provider.NewRegistry()
	reg.AddProvider(scripted.New(responses...))
	if err := reg.AddTier("cheap", []provider.ChainLink{{Provider: "scripted", Model: "cheap-model"}}); err != nil {
		t.Fatalf("AddTier: %v", err)
	}
	return reg
}

func obsCand(ns, body, anchor string) memsqlite.Candidate {
	return memsqlite.Candidate{
		Namespace: ns, Kind: memsqlite.KindObservation, Body: body, Importance: 4,
		Anchors: []memsqlite.Anchor{{Kind: "file", Ref: anchor}},
		Source:  memsqlite.Source{Ant: "worker"},
	}
}

// TestFoldRunsOnCheapTierAndEmits: pending candidates fold on the cheap tier,
// the fold's turn is metered under the consolidator, and a memory.folded event
// reaches the stream with the net effect on the namespace.
func TestFoldRunsOnCheapTierAndEmits(t *testing.T) {
	reg := cheapRegistry(t, scripted.Response{
		Text:  "run make gen after editing the schema",
		Usage: provider.Usage{Output: 7},
		Stop:  "end_turn",
	})
	c := openColony(t, WithRegistry(reg))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}

	ns := "ant_worker"
	// Two near-duplicates so the fold merges them in one cheap-tier turn.
	if err := c.memory.InsertCandidate(ctx, "c1", obsCand(ns, "run make gen after editing schema definition files", "schema.go")); err != nil {
		t.Fatal(err)
	}
	if err := c.memory.InsertCandidate(ctx, "c2", obsCand(ns, "make gen must run after editing schema definition files", "schema.go")); err != nil {
		t.Fatal(err)
	}

	sub, err := c.Events(ctx, EventFilter{Types: []event.Type{event.TypeMemoryFolded}})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Cancel()

	reports, err := c.Fold(ctx)
	if err != nil {
		t.Fatalf("fold: %v", err)
	}
	if len(reports) != 1 || reports[0].Merged != 1 {
		t.Fatalf("reports = %+v, want one namespace with one merged row", reports)
	}

	evs := collect(t, sub, event.TypeMemoryFolded)
	var folded event.MemoryFolded
	found := false
	for _, e := range evs {
		if e.Type == event.TypeMemoryFolded {
			if err := e.Decode(&folded); err != nil {
				t.Fatal(err)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no memory.folded event reached the stream")
	}
	if folded.Namespace != ns || folded.Merged != 1 || folded.Archived != 0 {
		t.Fatalf("memory.folded = %+v, want {%s 1 0}", folded, ns)
	}

	// The fold's turn was metered on the cheap tier under the consolidator.
	con := c.Ledger().ByAnt(consolidatorAnt)
	if con.Requests != 1 {
		t.Fatalf("consolidator ledger requests = %d, want 1", con.Requests)
	}
	if con.OutTokens != 7 {
		t.Fatalf("consolidator out tokens = %d, want 7", con.OutTokens)
	}
}

// TestFoldWithNoCandidatesSpendsNothing: a fold with nothing pending never
// touches the cheap tier, so a dead or unconfigured tier is harmless when
// there is no work.
func TestFoldWithNoCandidatesSpendsNothing(t *testing.T) {
	reg := provider.NewRegistry() // no cheap tier at all
	c := openColony(t, WithRegistry(reg))
	ctx := context.Background()
	if err := c.Start(ctx); err != nil {
		t.Fatal(err)
	}
	reports, err := c.Fold(ctx)
	if err != nil {
		t.Fatalf("fold over empty colony: %v", err)
	}
	if len(reports) != 0 {
		t.Fatalf("reports = %+v, want none", reports)
	}
	if c.Ledger().All().Requests != 0 {
		t.Fatal("an empty fold metered a turn")
	}
}
