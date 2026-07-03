package dialog

import (
	"testing"
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/tea"
)

func TestMain(m *testing.M) { eval.Main(m) }

// fake records what reaches it and answers with its own action type.
type fake struct {
	id   string
	got  []btea.Msg
	mark rune // painted into the top-left cell by Draw
}

type answered struct{ id string }

func (f *fake) ID() string { return f.id }

func (f *fake) HandleMsg(msg btea.Msg) Action {
	f.got = append(f.got, msg)
	return answered{id: f.id}
}

func (f *fake) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	scr.SetCell(area.Min.X, area.Min.Y, &uv.Cell{Content: string(f.mark), Width: 1})
	return nil
}

var t0 = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func key(r rune) btea.KeyPressMsg  { return btea.KeyPressMsg{Code: r} }
func esc() btea.KeyPressMsg        { return btea.KeyPressMsg{Code: btea.KeyEscape} }
func at(d time.Duration) time.Time { return t0.Add(d) }

// TestEmptyPassesThrough: with no dialog up, the overlay consumes
// nothing.
func TestEmptyPassesThrough(t *testing.T) {
	var o Overlay
	if act, handled := o.HandleMsg(key('x'), t0); handled || act != nil {
		t.Fatalf("empty overlay handled a message: %v %v", act, handled)
	}
	if o.Active() != nil {
		t.Fatal("empty overlay has an active dialog")
	}
}

// TestGraceSwallowsInFlightKey: a key arriving right after the dialog
// opens is dropped, not answered; after a quiet gap the next key goes
// through.
func TestGraceSwallowsInFlightKey(t *testing.T) {
	var o Overlay
	d := &fake{id: "perm"}
	o.Push(d, t0)

	act, handled := o.HandleMsg(key('y'), at(50*time.Millisecond))
	if !handled || act != nil {
		t.Fatalf("in-flight key: act=%v handled=%v, want swallowed", act, handled)
	}
	if len(d.got) != 0 {
		t.Fatalf("in-flight key reached the dialog: %v", d.got)
	}

	act, _ = o.HandleMsg(key('y'), at(300*time.Millisecond))
	if act != (answered{id: "perm"}) {
		t.Fatalf("post-quiet key: act=%v, want the dialog's answer", act)
	}
}

// TestGraceContinuousTypingHitsCap: someone typing straight through the
// dialog stays swallowed until the hard cap, never forever.
func TestGraceContinuousTypingHitsCap(t *testing.T) {
	var o Overlay
	d := &fake{id: "perm"}
	// A keystroke just before the dialog opens starts the quiet clock.
	o.lastKey = t0.Add(-10 * time.Millisecond)
	o.Push(d, t0)

	elapsed := 50 * time.Millisecond
	for ; elapsed < graceCap; elapsed += 100 * time.Millisecond {
		if act, _ := o.HandleMsg(key('a'), at(elapsed)); act != nil {
			t.Fatalf("key at %v answered before the cap: %v", elapsed, act)
		}
	}
	if act, _ := o.HandleMsg(key('a'), at(graceCap)); act == nil {
		t.Fatalf("key at the cap still swallowed")
	}
}

// TestReopenSkipsGrace: closing a dialog and reopening the same ID
// right away arms immediately; the user already read it.
func TestReopenSkipsGrace(t *testing.T) {
	var o Overlay
	o.Push(&fake{id: "perm"}, t0)
	o.Pop(at(1 * time.Second))

	d := &fake{id: "perm"}
	o.Push(d, at(2*time.Second))
	if act, _ := o.HandleMsg(key('y'), at(2*time.Second+10*time.Millisecond)); act == nil {
		t.Fatal("reopened dialog swallowed the key; grace should be skipped")
	}

	// A different ID, or the same one after the window, gets grace again.
	o.Pop(at(3 * time.Second))
	o.Push(&fake{id: "perm"}, at(3*time.Second).Add(reopenWindow+time.Second))
	if act, _ := o.HandleMsg(key('y'), at(3*time.Second).Add(reopenWindow+time.Second+10*time.Millisecond)); act != nil {
		t.Fatal("stale reopen answered inside grace")
	}
}

// TestEscPops: escape closes the top dialog and reports which one.
func TestEscPops(t *testing.T) {
	var o Overlay
	a, b := &fake{id: "a"}, &fake{id: "b"}
	o.Push(a, t0)
	o.Push(b, t0)

	act, _ := o.HandleMsg(esc(), at(2*time.Second))
	if act != (Closed{ID: "b"}) {
		t.Fatalf("esc action = %v, want Closed{b}", act)
	}
	if o.Active() != a {
		t.Fatalf("active after pop = %v, want a", o.Active())
	}
	act, _ = o.HandleMsg(esc(), at(4*time.Second))
	if act != (Closed{ID: "a"}) {
		t.Fatalf("second esc = %v, want Closed{a}", act)
	}
	if o.Len() != 0 {
		t.Fatalf("stack depth = %d after popping both", o.Len())
	}
}

// TestLIFO: messages reach the top dialog only, and popping resumes the
// one below.
func TestLIFO(t *testing.T) {
	var o Overlay
	a, b := &fake{id: "a"}, &fake{id: "b"}
	o.Push(a, t0)
	o.Push(b, at(1*time.Millisecond))

	o.HandleMsg(key('x'), at(2*time.Second))
	if len(a.got) != 0 || len(b.got) != 1 {
		t.Fatalf("routing: a got %d, b got %d, want 0 and 1", len(a.got), len(b.got))
	}

	o.Pop(at(3 * time.Second))
	o.HandleMsg(key('x'), at(6*time.Second))
	if len(a.got) != 1 {
		t.Fatalf("after pop, a got %d messages, want 1", len(a.got))
	}
}

// TestNonKeyMsgsBypassGrace: only keys arm-gate; anything else reaches
// the dialog immediately (a tick, a result arriving).
func TestNonKeyMsgsBypassGrace(t *testing.T) {
	var o Overlay
	d := &fake{id: "perm"}
	o.Push(d, t0)

	type tick struct{}
	if act, handled := o.HandleMsg(tick{}, at(time.Millisecond)); !handled || act == nil {
		t.Fatalf("non-key message blocked by grace: act=%v handled=%v", act, handled)
	}
	if len(d.got) != 1 {
		t.Fatalf("dialog got %d messages, want 1", len(d.got))
	}
}

// TestDrawTopOnly: only the top dialog paints.
func TestDrawTopOnly(t *testing.T) {
	var o Overlay
	o.Push(&fake{id: "a", mark: 'A'}, t0)
	o.Push(&fake{id: "b", mark: 'B'}, t0)

	buf := uv.NewScreenBuffer(4, 2)
	o.Draw(buf, buf.Bounds())
	if got := buf.CellAt(0, 0).Content; got != "B" {
		t.Fatalf("top-left cell = %q, want B", got)
	}
}
