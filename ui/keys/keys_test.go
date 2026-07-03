package keys

import (
	"strings"
	"testing"
)

// TestDefaultsValid: the built-in map has no collisions and a binding
// for every action.
func TestDefaultsValid(t *testing.T) {
	m := Default()
	if errs := m.collisions(); len(errs) > 0 {
		t.Fatalf("default keymap collides: %v", errs)
	}
	for a := range actionCount {
		if len(m.Binding(a).Keys()) == 0 {
			t.Errorf("action %s has no default keys", a)
		}
		if _, ok := scopeOf[a]; !ok {
			t.Errorf("action %s has no scope", a)
		}
	}
}

// TestLookupScoped: the same key resolves per scope, and a key from
// another scope does not leak in.
func TestLookupScoped(t *testing.T) {
	m := Default()
	if a, ok := m.Lookup(Editor, "enter"); !ok || a != Submit {
		t.Errorf("editor enter = %v %v, want Submit", a, ok)
	}
	if a, ok := m.Lookup(Dialog, "enter"); !ok || a != Confirm {
		t.Errorf("dialog enter = %v %v, want Confirm", a, ok)
	}
	if _, ok := m.Lookup(Global, "enter"); ok {
		t.Error("global scope matched enter")
	}
	if a, ok := m.Lookup(Global, "ctrl+c"); !ok || a != Quit {
		t.Errorf("global ctrl+c = %v %v, want Quit", a, ok)
	}
}

// TestApplyRebind: an override replaces the key set, help follows the
// new first key, and the old key stops matching.
func TestApplyRebind(t *testing.T) {
	m, err := Default().Apply(map[string][]string{"submit": {"ctrl+s"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if a, ok := m.Lookup(Editor, "ctrl+s"); !ok || a != Submit {
		t.Errorf("rebound submit not on ctrl+s: %v %v", a, ok)
	}
	if _, ok := m.Lookup(Editor, "enter"); ok {
		t.Error("old submit key still matches after rebind")
	}
	if h := m.Binding(Submit).Help(); h.Key != "ctrl+s" || h.Desc != "send" {
		t.Errorf("help after rebind = %+v, want ctrl+s / send", h)
	}
}

// TestApplyDoesNotMutateReceiver: the original map survives an overlay.
func TestApplyDoesNotMutateReceiver(t *testing.T) {
	base := Default()
	if _, err := base.Apply(map[string][]string{"submit": {"ctrl+s"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if a, ok := base.Lookup(Editor, "enter"); !ok || a != Submit {
		t.Errorf("base map mutated by Apply: %v %v", a, ok)
	}
}

// TestApplyErrors: unknown actions, empty key lists, and same-scope
// collisions all surface, together, with names in the message.
func TestApplyErrors(t *testing.T) {
	_, err := Default().Apply(map[string][]string{
		"warp_speed": {"ctrl+q"},
		"newline":    {},
		"submit":     {"ctrl+e"}, // collides with external_editor in Editor
	})
	if err == nil {
		t.Fatal("bad config loaded cleanly")
	}
	for _, want := range []string{"warp_speed", "newline", "ctrl+e"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}

// TestPaneGlobalCollision: a pane key that shadows a global one is an
// error, because panes fall through to global.
func TestPaneGlobalCollision(t *testing.T) {
	if _, err := Default().Apply(map[string][]string{"scroll_up": {"ctrl+c"}}); err == nil {
		t.Fatal("chat binding on the quit key loaded cleanly")
	}
	// Dialog does not fall through, so it may reuse a global key.
	if _, err := Default().Apply(map[string][]string{"confirm": {"ctrl+c"}}); err != nil {
		t.Fatalf("dialog reuse of a global key rejected: %v", err)
	}
}

// TestHelpReadsLiveBindings: rebinding changes what help shows.
func TestHelpReadsLiveBindings(t *testing.T) {
	m, err := Default().Apply(map[string][]string{"quit": {"ctrl+x"}})
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	found := false
	for _, b := range m.Help(Chat) {
		if b.Help().Desc == "quit" {
			found = true
			if b.Help().Key != "ctrl+x" {
				t.Errorf("help shows %q for quit, want ctrl+x", b.Help().Key)
			}
		}
	}
	if !found {
		t.Error("chat help does not include quit")
	}
}
