package cmd

import (
	"context"
	"encoding/json"

	"github.com/tamnd/ari/agent"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/ui"
	"github.com/tamnd/ari/ui/parts"
)

// colonyClient adapts the core facade to the ui.Client seam. The ui
// package declares the interface and never imports core; this file is
// the one place the two sides meet (D2, the import-graph guard).
type colonyClient struct {
	c  *core.Colony
	ns string // the worker ant's memory namespace, for the memory panel
}

func (a colonyClient) NewSession(ctx context.Context, title string) (string, error) {
	id, err := a.c.NewSession(ctx, core.NewSessionRequest{Title: title})
	return string(id), err
}

func (a colonyClient) Sessions(ctx context.Context) ([]ui.SessionInfo, error) {
	sums, err := a.c.ListSessions(ctx)
	if err != nil {
		return nil, err
	}
	infos := make([]ui.SessionInfo, 0, len(sums))
	for _, s := range sums {
		infos = append(infos, ui.SessionInfo{ID: string(s.ID), Title: s.Title})
	}
	return infos, nil
}

func (a colonyClient) Submit(ctx context.Context, session, text string) (string, error) {
	id, err := a.c.Submit(ctx, core.SubmitRequest{Session: core.SessionID(session), Text: text})
	return string(id), err
}

func (a colonyClient) Cancel(ctx context.Context, session string) error {
	return a.c.Cancel(ctx, core.SessionID(session))
}

func (a colonyClient) Respond(ctx context.Context, session, requestID, decision string) error {
	return a.c.Respond(ctx, core.RespondRequest{
		Session:   core.SessionID(session),
		RequestID: requestID,
		Decision:  core.RespondChoice(decision),
	})
}

func (a colonyClient) MemoryIndex(ctx context.Context) (string, error) {
	return a.c.PinnedIndex(ctx, a.ns)
}

func (a colonyClient) MemorySearch(ctx context.Context, query string) ([]ui.MemoryHit, error) {
	hits, err := a.c.RecallMemory(ctx, a.ns, query, memorySearchLimit)
	if err != nil {
		return nil, err
	}
	out := make([]ui.MemoryHit, len(hits))
	for i, h := range hits {
		out[i] = ui.MemoryHit{ID: h.ID, Label: h.Label, Body: h.Body, Stale: h.Stale}
	}
	return out, nil
}

func (a colonyClient) MemoryForget(ctx context.Context, session, id string) (bool, error) {
	return a.c.ForgetMemory(ctx, core.SessionID(session), a.ns, id)
}

func (a colonyClient) Transcript(ctx context.Context, sess, ant string) ([]parts.Part, error) {
	t, err := a.c.LoadSidechain(ctx, core.SessionID(sess), ant)
	if err != nil {
		return nil, err
	}
	return sidechainParts(t), nil
}

// sidechainParts reduces a stored sidechain to the render parts the drill-in
// draws. This is the read-side mirror of the streaming projection the chat runs
// live: an assistant entry unfolds into its reasoning, its text, and one part
// per tool call, a tool entry becomes a result keyed to its call, and a user
// entry is plain text. The reduction is lossy on purpose, since the drill-in is
// a read-only glance at a worker's run, not a resumable session, so a tool
// result carries its content as a string the per-tool renderer previews rather
// than the typed display data the live path threads through.
func sidechainParts(t session.Transcript) []parts.Part {
	var out []parts.Part
	for _, e := range t.Entries {
		switch e.Type {
		case session.EntryUser:
			var b struct {
				Text string `json:"text"`
			}
			_ = json.Unmarshal(e.Body, &b)
			out = append(out, parts.Part{Kind: parts.KindText, Role: parts.RoleUser, Text: b.Text, Finished: e.Time})
		case session.EntryAnt:
			var b agent.AntBody
			if json.Unmarshal(e.Body, &b) != nil {
				continue
			}
			if b.Thinking != "" {
				out = append(out, parts.Part{Kind: parts.KindReasoning, Role: parts.RoleAssistant, Text: b.Thinking, Finished: e.Time})
			}
			if b.Text != "" {
				out = append(out, parts.Part{Kind: parts.KindText, Role: parts.RoleAssistant, Text: b.Text, Finished: e.Time})
			}
			for _, call := range b.Calls {
				out = append(out, parts.Part{
					Kind: parts.KindToolCall, Role: parts.RoleAssistant,
					Tool: call.Name, Call: call.ID, Args: json.RawMessage(call.Input), Finished: e.Time,
				})
			}
		case session.EntryTool:
			var b agent.ToolBody
			if json.Unmarshal(e.Body, &b) != nil {
				continue
			}
			out = append(out, parts.Part{
				Kind: parts.KindToolResult, Role: parts.RoleTool,
				Tool: b.Tool, Call: b.Call, Result: b.Content, OK: !b.IsErr, Finished: e.Time,
			})
		}
	}
	return out
}

// memorySearchLimit caps how many hits the panel search asks for, the recall
// tool's own ceiling so the panel and the model see the same ranked top.
const memorySearchLimit = 20
