package core

import (
	"context"
	"encoding/json"
	"os"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/memory"
	"github.com/tamnd/ari/permission"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
)

// MemoryHit is one row a memory search returned, shaped for a client: the id a
// forget names, the label and body a human reads, and whether a file under it
// changed so the client can mark a stale row. It is the ranked recall the loop
// runs, projected off the store's Memory so the UI never imports the store.
type MemoryHit struct {
	ID    string
	Label string
	Body  string
	Stale bool
}

// PinnedIndex renders a namespace's live pinned index, the same bytes the
// consolidator builds into the prompt prefix (D14). A client shows it so a
// developer sees exactly what the ant carries for free every turn.
func (c *Colony) PinnedIndex(ctx context.Context, ns string) (string, error) {
	pins, err := c.memory.PinnedRows(ctx, ns)
	if err != nil {
		return "", Wrap(ErrNest, err, "reading the pinned index")
	}
	rows := make([]memory.Row, len(pins))
	for i, p := range pins {
		rows[i] = memory.Row{Label: p.Label, Anchors: p.Anchors}
	}
	return memory.RenderIndex(rows, memory.DefaultIndexCap), nil
}

// RecallMemory runs the same ranked recall the loop's recall tool runs, FTS
// only, and returns the hits for a client to browse. It bumps access stats
// exactly as a model recall would, because browsing memory is recalling it.
func (c *Colony) RecallMemory(ctx context.Context, ns, query string, limit int) ([]MemoryHit, error) {
	rows, err := c.memory.Recall(ctx, ns, query, nil, limit)
	if err != nil {
		return nil, Wrap(ErrNest, err, "recalling memory")
	}
	hits := make([]MemoryHit, len(rows))
	for i, m := range rows {
		hits[i] = MemoryHit{ID: m.ID, Label: m.Label, Body: m.Body, Stale: m.Stale}
	}
	return hits, nil
}

// ForgetMemory archives a memory from a client through the real permission
// pipeline: the forget tool decides, a permission.requested rides the stream so
// the client's dialog shows the row that would leave, and the answer comes back
// through the same asks bridge a model turn uses. A client has no privileged
// path to mutate memory; a forget from the panel is the same decision as a
// forget from the loop (doc 05, doc 07). It reports whether a row was archived,
// which a deny leaves false.
func (c *Colony) ForgetMemory(ctx context.Context, s SessionID, ns, id string) (bool, error) {
	ft := memory.NewForget(c.memory)
	tc := &tool.ToolContext{
		Cwd:       c.nest.Root,
		Ant:       workerAnt,
		Namespace: ns,
		Journal:   colonyJournal{c},
	}
	pipe := &permission.Pipeline{
		Mode:     permission.Mode(c.config.Mode),
		Paths:    c.mutationPaths(),
		Journal:  colonyJournal{c},
		Resolver: permission.ResolverFunc(c.forgetResolver(s)),
	}
	input, err := json.Marshal(map[string]string{"id": id})
	if err != nil {
		return false, Wrap(ErrInternal, err, "encoding the forget call")
	}
	call := permission.Call{
		Tool:    ft,
		Input:   input,
		TC:      tc,
		CallID:  session.NewID(),
		Session: string(s),
		Cwd:     c.nest.Root,
	}
	d := pipe.Decide(ctx, call)
	if d.Behavior != permission.Allow {
		return false, nil
	}
	if _, err := ft.Call(ctx, input, tc, nil); err != nil {
		return false, Wrap(ErrNest, err, "archiving the memory")
	}
	return true, nil
}

// forgetResolver blocks a forget's Ask on the client's answer, the same wait a
// running turn's resolver uses, keyed by the panel's active session so the
// answer lands even if the user switched away.
func (c *Colony) forgetResolver(s SessionID) func(context.Context, *permission.Request) (permission.Resolution, bool) {
	return func(ctx context.Context, req *permission.Request) (permission.Resolution, bool) {
		ans, err := c.asks.Wait(ctx, s, req.ID)
		if err != nil {
			return permission.Resolution{}, false
		}
		switch ans.Decision {
		case Allow, AllowSession:
			return permission.Resolution{Behavior: permission.Allow}, true
		default:
			return permission.Resolution{Behavior: permission.Deny, Message: "the user denied this call"}, true
		}
	}
}

// mutationPaths resolves the locations the safety floor protects. A forget
// writes no files, so the floor passes; the paths are set anyway so the pipeline
// is the same shape the loop builds.
func (c *Colony) mutationPaths() permission.Paths {
	home, _ := os.UserHomeDir()
	return permission.Paths{
		Root:         tool.ResolveMutationPath(c.nest.Root),
		Nest:         tool.ResolveMutationPath(c.nest.ProjectDir()),
		GlobalNest:   tool.ResolveMutationPath(c.nest.Global),
		Home:         tool.ResolveMutationPath(home),
		GlobalConfig: tool.ResolveMutationPath(c.nest.GlobalConfig()),
	}
}

// colonyJournal adapts the colony's journal to the tool.Journal seam the
// permission pipeline emits its requested and resolved pair through.
type colonyJournal struct{ c *Colony }

func (j colonyJournal) Append(e event.Event) event.Event { return j.c.journal.Append(e) }
