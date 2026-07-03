package ant

import "context"

// Memory is the store seam the ant holds and does not call in M0. M2's
// two-tier provenanced memory (D11) implements it; the ant's loop is
// untouched then, because block two already reserves the pinned index's
// slot and the runner already threads this interface (doc 01 section
// 2.1, plan/01 slice 9).
type Memory interface {
	// PinnedIndex renders the ant's pinned memory index as capped
	// markdown, one line per pin (D11, doc 07).
	PinnedIndex(ctx context.Context, namespace string) (string, error)
}
