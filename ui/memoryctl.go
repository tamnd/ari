package ui

import (
	"context"
	"time"

	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/dialog"
	"github.com/tamnd/ari/ui/memory"
	"github.com/tamnd/ari/ui/theme"
)

// memIndexLoaded carries a refreshed pinned index back to the update loop.
type memIndexLoaded struct {
	index string
	err   error
}

// memResults carries a completed archival search.
type memResults struct {
	hits []memory.Hit
	err  error
}

// memForgot reports a forget landing: archived is false when the user denied it.
type memForgot struct {
	archived bool
	err      error
}

// MemoryController owns the memory panel: it opens it, feeds it the live index
// and search results off the client, tails fold events onto its log, and routes
// a forget through the client, which runs it past the permission pipeline. It
// holds a reference to the pushed panel so an async result lands on the dialog
// the user is looking at; the reference clears when the dialog closes.
type MemoryController struct {
	client Client
	th     theme.Theme
	ns     string
	panel  *memory.Panel
}

// NewMemory builds the controller.
func NewMemory(client Client, th theme.Theme, ns string) *MemoryController {
	return &MemoryController{client: client, th: th, ns: ns}
}

// SetTheme swaps the palette for a panel opened after the change.
func (mc *MemoryController) SetTheme(th theme.Theme) {
	mc.th = th
	if mc.panel != nil {
		mc.panel.SetTheme(th)
	}
}

// Open pushes the panel and loads its index. An open dialog owns input, so a
// second memory keypress never reaches here; the panel closes on escape like
// every other dialog, which is what Closed clears up after.
func (mc *MemoryController) Open(overlay *dialog.Overlay, now time.Time) btea.Cmd {
	if mc.panel != nil {
		return nil
	}
	mc.panel = memory.New(mc.th)
	mc.panel.SetNamespace(mc.ns)
	overlay.Push(mc.panel, now)
	return mc.loadIndex()
}

// Closed clears the panel reference when the overlay pops it on escape.
func (mc *MemoryController) Closed() { mc.panel = nil }

// Apply folds a bus message into the panel. Only fold events matter here; the
// panel ignores the rest of the stream.
func (mc *MemoryController) Apply(msg btea.Msg) {
	if mc.panel == nil {
		return
	}
	if m, ok := msg.(bus.MemoryFoldedMsg); ok {
		mc.panel.AddFold(memory.Fold{
			Namespace:   m.Namespace,
			Merged:      m.Merged,
			Reflections: m.Reflections,
			Archived:    m.Archived,
			Candidates:  m.Candidates,
		})
	}
}

// Search runs the archival recall off the update loop.
func (mc *MemoryController) Search(query string) btea.Cmd {
	if query == "" {
		return nil
	}
	client := mc.client
	return func() btea.Msg {
		hits, err := client.MemorySearch(context.Background(), query)
		out := memResults{err: err}
		for _, h := range hits {
			out.hits = append(out.hits, memory.Hit{ID: h.ID, Label: h.Label, Body: h.Body, Stale: h.Stale})
		}
		return out
	}
}

// Forget routes an archive through the client. The call blocks until the user
// resolves the permission request the pipeline raises, so it runs off the loop;
// the perm dialog stacks over the panel while it waits.
func (mc *MemoryController) Forget(id, session string) btea.Cmd {
	if id == "" {
		return nil
	}
	client := mc.client
	return func() btea.Msg {
		archived, err := client.MemoryForget(context.Background(), session, id)
		return memForgot{archived: archived, err: err}
	}
}

// loadIndex fetches the live pinned index off the update loop.
func (mc *MemoryController) loadIndex() btea.Cmd {
	client := mc.client
	return func() btea.Msg {
		index, err := client.MemoryIndex(context.Background())
		return memIndexLoaded{index: index, err: err}
	}
}

// OnIndex lands a refreshed index on the panel.
func (mc *MemoryController) OnIndex(m memIndexLoaded) {
	if mc.panel != nil && m.err == nil {
		mc.panel.SetIndex(m.index)
	}
}

// OnResults lands a completed search on the panel.
func (mc *MemoryController) OnResults(m memResults) {
	if mc.panel != nil && m.err == nil {
		mc.panel.SetResults(m.hits)
	}
}

// OnForgot refreshes the index after an archive so the row that left drops out
// of the pinned view. A denied forget archived nothing, so nothing changed.
func (mc *MemoryController) OnForgot(m memForgot) btea.Cmd {
	if m.err != nil || !m.archived || mc.panel == nil {
		return nil
	}
	return mc.loadIndex()
}
