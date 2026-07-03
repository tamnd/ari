package input

import (
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	btea "charm.land/bubbletea/v2"

	"github.com/tamnd/ari/kernel/eval"
)

func TestMain(m *testing.M) { eval.Main(m) }

// clock is a hand-cranked time source.
type clock struct{ t time.Time }

func (c *clock) now() time.Time       { return c.t }
func (c *clock) tick(d time.Duration) { c.t = c.t.Add(d) }
func newFilterAt(c *clock) *Filter    { return &Filter{now: c.now} }
func wheel(b btea.MouseButton, x, y int) btea.Msg {
	return btea.MouseWheelMsg{Button: b, X: x, Y: y}
}

// TestWheelBurstCoalesces: a storm of wheel events inside one window
// becomes one message carrying the summed delta.
func TestWheelBurstCoalesces(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f := newFilterAt(c)

	// First event of the burst flushes immediately (lastWheel is zero).
	first := f.Filter(nil, wheel(btea.MouseWheelDown, 1, 2))
	if first != (CoalescedWheel{Delta: 1, X: 1, Y: 2}) {
		t.Fatalf("first wheel = %v, want delta 1", first)
	}

	// Nine more inside the window are swallowed.
	for range 9 {
		c.tick(time.Millisecond)
		if out := f.Filter(nil, wheel(btea.MouseWheelDown, 1, 2)); out != nil {
			t.Fatalf("mid-burst wheel leaked: %v", out)
		}
	}

	// Past the window, the next event carries the whole accumulated sum.
	c.tick(sampleWindow)
	out := f.Filter(nil, wheel(btea.MouseWheelDown, 3, 4))
	if out != (CoalescedWheel{Delta: 10, X: 3, Y: 4}) {
		t.Fatalf("flush = %v, want the 10 accumulated steps at the latest position", out)
	}
}

// TestWheelDirectionsCancel: up and down inside a burst sum to zero and
// emit nothing.
func TestWheelDirectionsCancel(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f := newFilterAt(c)
	f.Filter(nil, wheel(btea.MouseWheelDown, 0, 0)) // flushes delta 1
	c.tick(time.Millisecond)
	f.Filter(nil, wheel(btea.MouseWheelUp, 0, 0)) // swallowed, acc -1
	c.tick(time.Millisecond)
	f.Filter(nil, wheel(btea.MouseWheelDown, 0, 0)) // swallowed, acc 0
	c.tick(sampleWindow)
	if out := f.Filter(nil, wheel(btea.MouseWheelUp, 0, 0)); out != nil {
		// acc 0 + up = -1... the flush carries -1, which is real input.
		if out != (CoalescedWheel{Delta: -1, X: 0, Y: 0}) {
			t.Fatalf("flush after cancel = %v, want delta -1", out)
		}
	}
}

// TestMotionSampled: motion passes at most once per window.
func TestMotionSampled(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f := newFilterAt(c)
	passed := 0
	for range 32 {
		if f.Filter(nil, btea.MouseMotionMsg{X: 1}) != nil {
			passed++
		}
		c.tick(time.Millisecond)
	}
	if passed != 2 { // t=0 and t=16ms
		t.Fatalf("motion events passed = %d over 32ms, want 2", passed)
	}
}

// TestKeysNeverDelayed: keys, paste, and resize pass through even
// mid-burst.
func TestKeysNeverDelayed(t *testing.T) {
	c := &clock{t: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}
	f := newFilterAt(c)
	f.Filter(nil, wheel(btea.MouseWheelDown, 0, 0))
	c.tick(time.Millisecond)
	for _, msg := range []btea.Msg{
		btea.KeyPressMsg{Code: 'a'},
		btea.PasteMsg{},
		btea.WindowSizeMsg{Width: 80, Height: 24},
	} {
		if out := f.Filter(nil, msg); out == nil {
			t.Errorf("%T was delayed by the mouse filter", msg)
		}
	}
}

// TestEditorCommand: cursor flags per editor family, extra flags in the
// env value preserved, and the fallback chain VISUAL, EDITOR, vi.
func TestEditorCommand(t *testing.T) {
	for _, tc := range []struct {
		visual, editor string
		wantBin        string
		wantArgs       []string
	}{
		{"", "", "vi", []string{"+call cursor(3,7)", "/tmp/p.md"}},
		{"", "nvim", "nvim", []string{"+call cursor(3,7)", "/tmp/p.md"}},
		{"", "nano", "nano", []string{"+3,7", "/tmp/p.md"}},
		{"", "hx", "hx", []string{"/tmp/p.md:3:7"}},
		{"", "code -n", "code", []string{"-n", "--wait", "--goto", "/tmp/p.md:3:7"}},
		{"", "emacs", "emacs", []string{"/tmp/p.md"}},
		{"vim", "nano", "vim", []string{"+call cursor(3,7)", "/tmp/p.md"}}, // VISUAL wins
	} {
		t.Setenv("VISUAL", tc.visual)
		t.Setenv("EDITOR", tc.editor)
		c := editorCommand("/tmp/p.md", 3, 7)
		if got := c.Args[0]; !strings.HasSuffix(got, tc.wantBin) {
			t.Errorf("VISUAL=%q EDITOR=%q: bin = %q, want %q", tc.visual, tc.editor, got, tc.wantBin)
		}
		if got := c.Args[1:]; !slices.Equal(got, tc.wantArgs) {
			t.Errorf("VISUAL=%q EDITOR=%q: args = %q, want %q", tc.visual, tc.editor, got, tc.wantArgs)
		}
	}
}

// TestOpenEditorRoundTrip drives the hatch's pieces the way ExecProcess
// does: stage the prompt, run the editor (a script here) on it, hand
// the exit to the reload callback. The edit comes back and the temp
// file is gone.
func TestOpenEditorRoundTrip(t *testing.T) {
	fake := t.TempDir() + "/ed"
	writeExec(t, fake, "#!/bin/sh\nprintf 'edited body\\n' > \"$1\"\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fake)

	path, err := writePrompt("original")
	if err != nil {
		t.Fatal(err)
	}
	msg := reload(path)(editorCommand(path, 1, 1).Run())
	closed, ok := msg.(EditorClosed)
	if !ok {
		t.Fatalf("message = %T, want EditorClosed", msg)
	}
	if closed.Err != nil || closed.Content != "edited body" {
		t.Fatalf("EditorClosed = %+v, want the edited body", closed)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp prompt file survived: %v", err)
	}
}

// TestOpenEditorFailure: a failing editor surfaces its error, delivers
// no stale content, and still cleans up.
func TestOpenEditorFailure(t *testing.T) {
	fake := t.TempDir() + "/ed"
	writeExec(t, fake, "#!/bin/sh\nexit 3\n")
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", fake)

	path, err := writePrompt("original")
	if err != nil {
		t.Fatal(err)
	}
	msg := reload(path)(editorCommand(path, 1, 1).Run())
	closed := msg.(EditorClosed)
	if closed.Err == nil {
		t.Fatal("failing editor reported no error")
	}
	if closed.Content != "" {
		t.Fatalf("failing editor delivered content %q", closed.Content)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("temp prompt file survived a failed edit: %v", err)
	}
}

func writeExec(t *testing.T, path, script string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}
