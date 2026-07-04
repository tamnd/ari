package ui

import (
	"strings"
	"testing"

	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/ui/bus"
)

func ctrlR() btea.KeyPressMsg { return btea.KeyPressMsg{Code: 'r', Mod: btea.ModCtrl} }
func ctrlD() btea.KeyPressMsg { return btea.KeyPressMsg{Code: 'd', Mod: btea.ModCtrl} }

// TestMemoryPanelOpensWithIndex: ctrl+r opens the panel and the live pinned
// index lands in the rendered box.
func TestMemoryPanelOpensWithIndex(t *testing.T) {
	h := newHarness(t, Options{})
	h.client.index = "- keep one transport (file:transport.go)"
	h.press(ctrlR())
	if h.m.overlay.Len() != 1 {
		t.Fatal("ctrl+r did not open the memory panel")
	}
	if out := renderPlain(h.m, 100, 30); !strings.Contains(out, "keep one transport") {
		t.Fatalf("panel did not show the pinned index:\n%s", out)
	}
	// Escape closes it, like every other dialog, and clears the controller's
	// panel reference so a later reopen starts fresh.
	h.press(kp("esc"))
	if h.m.overlay.Len() != 0 {
		t.Fatal("esc did not close the panel")
	}
	if h.m.memory.panel != nil {
		t.Fatal("closing the panel left a stale controller reference")
	}
}

// TestMemoryPanelSearches: a query plus enter runs the recall through the client
// and the hits render in the panel.
func TestMemoryPanelSearches(t *testing.T) {
	h := newHarness(t, Options{})
	h.client.hits = []MemoryHit{{ID: "01aa", Label: "shared transport", Body: "one http.Transport"}}
	h.press(ctrlR())
	typeText(h, "transport")
	h.press(kp("enter"))
	out := renderPlain(h.m, 100, 30)
	if !strings.Contains(out, "shared transport") {
		t.Fatalf("search hits did not render:\n%s", out)
	}
}

// TestMemoryPanelTailsFolds: a fold event on the stream lands on the panel's log
// with its counts intact.
func TestMemoryPanelTailsFolds(t *testing.T) {
	h := newHarness(t, Options{})
	h.press(ctrlR())
	var m bus.MemoryFoldedMsg
	m.Namespace = "worker/main"
	m.Merged, m.Reflections, m.Archived, m.Candidates = 3, 1, 2, 5
	h.send(m)
	out := renderPlain(h.m, 100, 30)
	if !strings.Contains(out, "worker/main") || !strings.Contains(out, "+3 (1 refl), 2 archived, 5 seen") {
		t.Fatalf("fold did not land on the panel log:\n%s", out)
	}
}

// TestMemoryPanelForgetRoutesThroughClient: ctrl+d on the highlighted hit routes
// a forget through the client, which is where the permission pipeline runs.
func TestMemoryPanelForgetRoutesThroughClient(t *testing.T) {
	h := newHarness(t, Options{})
	h.client.hits = []MemoryHit{{ID: "01aa", Label: "shared transport"}}
	h.client.forgetOK = true
	h.press(ctrlR())
	typeText(h, "x")
	h.press(kp("enter"))
	h.press(ctrlD())
	if got := h.client.forgets; len(got) != 1 || got[0] != [2]string{"", "01aa"} {
		t.Fatalf("forgets = %v, want one archive of 01aa", got)
	}
}
