// Package fold holds the consolidator, the one writer of live memory. Worker
// ants propose candidates; the consolidator weighs them at idle or session end
// and folds the survivors into recallable memory (D12). It runs on the cheap
// model tier (D17) and is the poisoning defense the ship gate proves:
// repetition cannot inflate a memory's rank, the model selects rather than
// invents, and anchors and evidence are copied from candidates, never authored
// by the model.
package fold

import "github.com/tamnd/ari/event"

// FoldReport is the accounting of one fold over one namespace: what came in,
// what the fold wrote, and what it spent. It is richer than the wire event so
// the ledger and the fixtures can check the fold's work; WirePayload narrows
// it to the schema the bus carries.
type FoldReport struct {
	Namespace   string // the namespace this fold covered
	Candidates  int    // pending candidates the fold read
	Merged      int    // live rows the fold wrote (observations plus reflections)
	Reflections int    // of Merged, how many were reflections
	Demoted     int    // rows the fold demoted for staleness (slice 7)
	Evaporated  int    // session rows the fold cleared (slice 7)
	IndexLines  int    // pinned index lines the fold rebuilt (slice 8)
	TokensCheap int    // cheap-tier tokens the fold spent
}

// WirePayload narrows a FoldReport to the MemoryFolded event the bus carries.
// The wire shape stays minimal on purpose: a client learns a fold happened and
// its net effect on the namespace, not the fold's internal accounting.
func (r FoldReport) WirePayload() event.MemoryFolded {
	return event.MemoryFolded{
		Namespace: r.Namespace,
		Merged:    r.Merged,
		Archived:  r.Demoted + r.Evaporated,
	}
}
