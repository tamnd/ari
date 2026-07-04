package cmd

import (
	"encoding/json"
	"testing"

	"github.com/tamnd/ari/agent"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/ui/parts"
)

func mustBody(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// sidechainParts is the read-side mirror of the live chat projection, so its
// contract is that one stored assistant entry unfolds into reasoning, text, and
// a part per tool call in that order, a tool entry becomes a keyed result, and
// a user entry is plain text.
func TestSidechainPartsUnfoldsEntries(t *testing.T) {
	tr := session.Transcript{Entries: []session.Entry{
		{Type: session.EntryUser, Body: mustBody(t, map[string]string{"text": "survey the api package"})},
		{Type: session.EntryAnt, Body: mustBody(t, agent.AntBody{
			Thinking: "start with the handlers",
			Text:     "reading api/handlers.go",
			Calls: []provider.ToolCall{
				{ID: "c1", Name: "read", Input: `{"path":"api/handlers.go"}`},
			},
		})},
		{Type: session.EntryTool, Body: mustBody(t, agent.ToolBody{
			Call: "c1", Tool: "read", Content: "package api\n\nfunc Handle() {}",
		})},
	}}

	got := sidechainParts(tr)
	if len(got) != 5 {
		t.Fatalf("want 5 parts, got %d: %+v", len(got), got)
	}

	want := []struct {
		kind parts.Kind
		role parts.Role
	}{
		{parts.KindText, parts.RoleUser},
		{parts.KindReasoning, parts.RoleAssistant},
		{parts.KindText, parts.RoleAssistant},
		{parts.KindToolCall, parts.RoleAssistant},
		{parts.KindToolResult, parts.RoleTool},
	}
	for i, w := range want {
		if got[i].Kind != w.kind || got[i].Role != w.role {
			t.Errorf("part %d: got kind %d role %d, want kind %d role %d", i, got[i].Kind, got[i].Role, w.kind, w.role)
		}
	}

	// The tool call and its result share a call id so the chat list pairs them.
	if got[3].Call != "c1" || got[4].Call != "c1" {
		t.Errorf("call id lost: call %q result %q", got[3].Call, got[4].Call)
	}
	if got[4].Result != "package api\n\nfunc Handle() {}" || !got[4].OK {
		t.Errorf("tool result content or ok wrong: %+v", got[4])
	}
}

// A worker that produced nothing drills in to nothing, and a text-only turn
// with no thinking and no calls yields exactly one part.
func TestSidechainPartsHandlesEmptyAndTextOnly(t *testing.T) {
	if got := sidechainParts(session.Transcript{}); got != nil {
		t.Errorf("an empty transcript has no parts, got %d", len(got))
	}
	tr := session.Transcript{Entries: []session.Entry{
		{Type: session.EntryAnt, Body: mustBody(t, agent.AntBody{Text: "done"})},
	}}
	got := sidechainParts(tr)
	if len(got) != 1 || got[0].Kind != parts.KindText || got[0].Text != "done" {
		t.Errorf("text-only turn: want one text part, got %+v", got)
	}
}
