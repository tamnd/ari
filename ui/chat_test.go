package ui

import (
	"strings"
	"testing"
	"time"

	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/theme"
)

// feed builds a chat and replays a scripted M0 turn into it.
func feed(t *testing.T) *ChatController {
	t.Helper()
	clock := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	c := NewChat(theme.Dark(), func() time.Time { return clock })
	meta := bus.Meta{Session: "s1", Turn: "t1"}

	var ts bus.TurnStartedMsg
	ts.Meta, ts.TurnStarted = meta, event.TurnStarted{ID: "t1", Prompt: "fix the bug"}
	c.Apply(ts)

	for _, chunk := range []string{"looking at ", "the test"} {
		var td bus.TextDeltaMsg
		td.Meta, td.TextDelta = meta, event.TextDelta{Part: 0, Text: chunk}
		c.Apply(td)
	}
	var te bus.TextEndMsg
	te.Meta, te.TextEnd = meta, event.TextEnd{Part: 0}
	c.Apply(te)

	var tls bus.ToolStartMsg
	tls.Meta = meta
	tls.ToolStart = event.ToolStart{Part: 1, Call: "c1", Tool: "sh", Input: `{"cmd":"go test"}`}
	c.Apply(tls)
	var tle bus.ToolEndMsg
	tle.Meta = meta
	tle.ToolEnd = event.ToolEnd{Part: 1, Call: "c1", Tool: "sh", OK: true, Display: "ok  \tpkg\t0.1s"}
	c.Apply(tle)

	var lg bus.LedgerTurnMsg
	lg.Meta = meta
	lg.LedgerTurn = event.LedgerTurn{Turn: "t1", Model: "claude-sonnet-5", Input: 900, Output: 120}
	c.Apply(lg)
	var tf bus.TurnFinishedMsg
	tf.Meta, tf.TurnFinished = meta, event.TurnFinished{ID: "t1", Reason: "end_turn"}
	c.Apply(tf)
	return c
}

// TestProjectionOrder: the parts land in event order, one item each.
func TestProjectionOrder(t *testing.T) {
	c := feed(t)
	want := []string{"user:t1", "t1/0", "t1/1", "call:c1", "fin:t1"}
	if len(c.order) != len(want) {
		t.Fatalf("projected %d parts %v, want %v", len(c.order), c.order, want)
	}
	for i, k := range want {
		if c.order[i] != k {
			t.Errorf("part %d = %s, want %s", i, c.order[i], k)
		}
	}
}

// TestStreamingAccumulates: deltas append to one part and bump its
// version each time, which is what the list memo keys on.
func TestStreamingAccumulates(t *testing.T) {
	c := feed(t)
	p := c.parts["t1/0"]
	if p.Text != "looking at the test" {
		t.Fatalf("streamed text = %q", p.Text)
	}
	if p.Finished.IsZero() {
		t.Fatal("text.end did not freeze the part")
	}
	if p.Version < 3 {
		t.Fatalf("version = %d after two deltas and an end, want at least 3", p.Version)
	}
}

// TestLedgerAttachesToFinish: the finish part carries the turn's usage
// whichever order ledger.turn and turn.finished arrive in.
func TestLedgerAttachesToFinish(t *testing.T) {
	c := feed(t) // ledger before finish
	if got := c.parts["fin:t1"].Usage.Input; got != 900 {
		t.Fatalf("usage input = %d, want 900", got)
	}
}

// TestChatGolden pins a full projected conversation render.
func TestChatGolden(t *testing.T) {
	c := feed(t)
	buf := uv.NewScreenBuffer(72, 24)
	c.Draw(buf, buf.Bounds())
	eval.Golden(t, "conversation", buf.Buffer.String())
}

// TestLandingShowsWordmark: an empty chat draws the logo, not a blank
// pane.
func TestLandingShowsWordmark(t *testing.T) {
	c := NewChat(theme.Dark(), time.Now)
	buf := uv.NewScreenBuffer(72, 24)
	c.Draw(buf, buf.Bounds())
	out := buf.Buffer.String()
	if !strings.Contains(out, "type a prompt to start") {
		t.Fatalf("landing pane missing the hint:\n%s", out)
	}
}

// TestThemeRebuildKeepsParts: swapping themes rebuilds the list without
// losing the conversation.
func TestThemeRebuildKeepsParts(t *testing.T) {
	c := feed(t)
	c.SetTheme(theme.Light())
	if c.list.Len() != len(c.order) {
		t.Fatalf("rebuilt list has %d items, want %d", c.list.Len(), len(c.order))
	}
}
