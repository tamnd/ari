package fold

import (
	"context"
	"strings"

	"github.com/tamnd/ari/memory"
	"github.com/tamnd/ari/memory/sqlite"
)

// PinnedIndex returns a namespace's pinned index, the cached markdown block two
// carries. It reads the cache the consolidator maintains and never rebuilds on a
// turn, so the same namespace returns the same bytes call after call between
// folds and the cache_control breakpoint holds (D14). The first read of a cold
// namespace builds the index once from the current pins, the session's initial
// fold boundary; every read after is served from the cache until a fold rebuilds
// it. This is the ant.Memory seam the runner threads.
func (c *Consolidator) PinnedIndex(ctx context.Context, ns string) (string, error) {
	c.idxMu.RLock()
	idx, ok := c.indexes[ns]
	c.idxMu.RUnlock()
	if ok {
		return idx, nil
	}
	return c.rebuildIndex(ctx, ns)
}

// rebuildIndex renders the pinned index for a namespace from its current pins
// and stores it in the cache. It is the only writer of the cache, called at a
// fold boundary and on the cold first read, never mid-turn.
func (c *Consolidator) rebuildIndex(ctx context.Context, ns string) (string, error) {
	rows, err := c.store.PinnedRows(ctx, ns)
	if err != nil {
		return "", err
	}
	idx := memory.RenderIndex(toIndexRows(rows), c.cap)
	c.idxMu.Lock()
	c.indexes[ns] = idx
	c.idxMu.Unlock()
	return idx, nil
}

// toIndexRows narrows the store's pinned rows to the renderer's row shape, the
// label and anchors that fit on one index line.
func toIndexRows(rows []sqlite.PinnedRow) []memory.Row {
	out := make([]memory.Row, len(rows))
	for i, r := range rows {
		out[i] = memory.Row{Label: r.Label, Anchors: r.Anchors}
	}
	return out
}

// indexLineCount reports how many lines an index rendered to, for the fold
// report. An empty index is zero lines.
func indexLineCount(idx string) int {
	if idx == "" {
		return 0
	}
	return strings.Count(idx, "\n") + 1
}
