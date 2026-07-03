package ui

import (
	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/ui/keys"
)

// Controller handles a key action already matched in its scope. A nil
// return means the action produced no command; the root never forwards
// an unmatched key here.
type Controller interface {
	Handle(a keys.Action, k btea.KeyPressMsg) btea.Cmd
}

// Router decides which scope owns input this frame. It is a table, not
// a switch: precedence is dialog > focused pane > global, resolved
// before any key is matched, which is the fix for the giant root-model
// key switch (doc 02 section 18.2).
type Router struct{}

// Route resolves the scope and the controller that owns it. A dialog
// owns input entirely; its keys never reach a pane. The nil controller
// for the dialog case is deliberate: the overlay handles the raw
// message itself, grace period and all.
func (Router) Route(m *Model) (keys.Scope, Controller) {
	if m.overlay.Len() > 0 {
		return keys.Dialog, nil
	}
	switch m.focus {
	case FocusEditor:
		return keys.Editor, m.editor
	case FocusChat:
		return keys.Chat, m.chat
	default:
		return keys.Global, nil
	}
}
