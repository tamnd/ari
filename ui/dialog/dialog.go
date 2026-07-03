// Package dialog is the modal layer (doc 02 section 8). A dialog is a
// widget that owns input while it is on top; the Overlay stacks them
// LIFO so a dialog can open another (theme picker from onboarding,
// confirmation from a form) and closing returns to the one below.
//
// The stack also owns grace arming: a dialog that appears mid-keystroke
// must not eat that keystroke as an answer. Keys are swallowed until
// the user has been quiet for a beat, with a hard cap so a dialog can
// never stay unanswerable, and a same-dialog reopen right after a close
// skips the grace because the user already saw it.
package dialog

import (
	"time"

	btea "charm.land/bubbletea/v2"
	uv "github.com/charmbracelet/ultraviolet"

	"github.com/tamnd/ari/ui/tea"
)

// Action is what a dialog hands back from HandleMsg. It is opaque to
// the overlay: each dialog defines its own action types and the root
// model switches on them, so the modal layer knows nothing about
// permissions, themes, or whatever else opens a dialog. An alias, so a
// dialog package can return plain any without importing this one.
type Action = any

// Closed is the action the overlay emits when it pops a dialog on
// escape, so the root can react to a dismissal it did not request.
type Closed struct{ ID string }

// Dialog is one modal surface. ID identifies the dialog kind for
// same-ID reopen detection; HandleMsg consumes input while on top and
// returns an Action (nil for handled-but-nothing-to-report); Draw
// paints it into its area.
type Dialog interface {
	ID() string
	HandleMsg(msg btea.Msg) Action
	Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor
}

// Grace arming tunables. A key is swallowed until the user has been
// quiet for graceQuiet since their previous key, but never past
// graceCap after the dialog opened. A reopen of the same dialog ID
// within reopenWindow of its close arms immediately.
const (
	graceQuiet   = 200 * time.Millisecond
	graceCap     = 1500 * time.Millisecond
	reopenWindow = 30 * time.Second
)

type entry struct {
	d        Dialog
	openedAt time.Time
	armed    bool // set once, never cleared: arming is one-way
}

// Overlay is the LIFO dialog stack. All methods take the current time
// explicitly so arming is testable without a clock; the root passes
// time.Now().
type Overlay struct {
	stack []entry

	// lastKey is when the user last pressed any key, on a dialog or
	// not; the root feeds every key through HandleMsg, so the overlay
	// sees them all while it is non-empty. Quiet time is measured
	// against this.
	lastKey time.Time

	// Same-ID reopen bookkeeping.
	closedID string
	closedAt time.Time
}

// Push opens a dialog on top of the stack.
func (o *Overlay) Push(d Dialog, now time.Time) {
	e := entry{d: d, openedAt: now}
	if d.ID() == o.closedID && now.Sub(o.closedAt) <= reopenWindow {
		e.armed = true
	}
	o.stack = append(o.stack, e)
}

// Pop removes the top dialog without emitting an action, for
// programmatic closes (the dialog answered, the request was cancelled).
func (o *Overlay) Pop(now time.Time) {
	if len(o.stack) == 0 {
		return
	}
	top := o.stack[len(o.stack)-1]
	o.stack = o.stack[:len(o.stack)-1]
	o.closedID, o.closedAt = top.d.ID(), now
}

// Active returns the top dialog, or nil when the stack is empty.
func (o *Overlay) Active() Dialog {
	if len(o.stack) == 0 {
		return nil
	}
	return o.stack[len(o.stack)-1].d
}

// Len reports the stack depth.
func (o *Overlay) Len() int { return len(o.stack) }

// HandleMsg routes a message to the top dialog. The bool reports
// whether the overlay consumed the message; false means the stack is
// empty and the root should route it elsewhere. Unarmed keys are
// consumed and dropped; escape pops the top dialog and emits Closed.
func (o *Overlay) HandleMsg(msg btea.Msg, now time.Time) (Action, bool) {
	if len(o.stack) == 0 {
		return nil, false
	}
	top := &o.stack[len(o.stack)-1]

	key, isKey := msg.(btea.KeyPressMsg)
	if isKey {
		if !top.armed {
			quietSince := o.lastKey
			if quietSince.Before(top.openedAt) {
				quietSince = top.openedAt
			}
			top.armed = now.Sub(top.openedAt) >= graceCap ||
				now.Sub(quietSince) >= graceQuiet
		}
		o.lastKey = now
		if !top.armed {
			return nil, true // in-flight typing, not an answer
		}
		if key.String() == "esc" {
			id := top.d.ID()
			o.Pop(now)
			return Closed{ID: id}, true
		}
	}
	return top.d.HandleMsg(msg), true
}

// Draw paints the top dialog. Dialogs below it stay hidden; dimming
// the content behind the modal is the root's concern.
func (o *Overlay) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	if len(o.stack) == 0 {
		return nil
	}
	return o.stack[len(o.stack)-1].d.Draw(scr, area)
}
