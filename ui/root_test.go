package ui

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	btea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/parts"
	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/theme"
)

// fakeClient records every call; no real core anywhere near these tests.
type fakeClient struct {
	mu       sync.Mutex
	sessions []SessionInfo
	submits  []string
	responds [][3]string // session, request id, decision
	cancels  []string
	index    string
	hits     []MemoryHit
	forgets  [][2]string // session, id
	forgetOK bool
	scripts  map[string][]parts.Part // ant key to its sidechain
}

func (f *fakeClient) NewSession(context.Context, string) (string, error) { return "s1", nil }

func (f *fakeClient) Sessions(context.Context) ([]SessionInfo, error) {
	return f.sessions, nil
}

func (f *fakeClient) Submit(_ context.Context, session, text string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.submits = append(f.submits, session+":"+text)
	return "t1", nil
}

func (f *fakeClient) Cancel(_ context.Context, session string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cancels = append(f.cancels, session)
	return nil
}

func (f *fakeClient) Respond(_ context.Context, session, id, decision string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.responds = append(f.responds, [3]string{session, id, decision})
	return nil
}

func (f *fakeClient) MemoryIndex(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.index, nil
}

func (f *fakeClient) MemorySearch(_ context.Context, query string) ([]MemoryHit, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits, nil
}

func (f *fakeClient) MemoryForget(_ context.Context, session, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgets = append(f.forgets, [2]string{session, id})
	return f.forgetOK, nil
}

func (f *fakeClient) Transcript(_ context.Context, _, ant string) ([]parts.Part, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.scripts[ant], nil
}

// harness owns a root with a controllable clock.
type harness struct {
	m      *Model
	client *fakeClient
	clock  time.Time
}

func newHarness(t *testing.T, o Options) *harness {
	t.Helper()
	h := &harness{client: &fakeClient{}, clock: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)}
	o.Client = h.client
	if o.Theme.Name == "" {
		o.Theme = theme.Dark()
	}
	o.Keys = keys.Default()
	o.Now = func() time.Time { return h.clock }
	h.m = New(o)
	h.m.Init()
	h.m.Update(btea.WindowSizeMsg{Width: 120, Height: 40})
	return h
}

// press advances the clock past any dialog grace, then sends a key and
// runs whatever command it produced, feeding messages back in.
func (h *harness) press(k btea.KeyPressMsg) {
	h.clock = h.clock.Add(2 * time.Second)
	h.send(k)
}

func (h *harness) send(msg btea.Msg) {
	_, cmd := h.m.Update(msg)
	for cmd != nil {
		out := cmd()
		cmd = nil
		if out != nil {
			if b, ok := out.(btea.BatchMsg); ok {
				for _, c := range b {
					if inner := c(); inner != nil {
						_, cmd = h.m.Update(inner)
					}
				}
				continue
			}
			_, cmd = h.m.Update(out)
		}
	}
}

func kp(s string) btea.KeyPressMsg {
	switch s {
	case "enter":
		return btea.KeyPressMsg{Code: btea.KeyEnter}
	case "esc":
		return btea.KeyPressMsg{Code: btea.KeyEscape}
	case "ctrl+p":
		return btea.KeyPressMsg{Code: 'p', Mod: btea.ModCtrl}
	case "ctrl+t":
		return btea.KeyPressMsg{Code: 't', Mod: btea.ModCtrl}
	case "ctrl+w":
		return btea.KeyPressMsg{Code: 'w', Mod: btea.ModCtrl}
	case "ctrl+e":
		return btea.KeyPressMsg{Code: 'e', Mod: btea.ModCtrl}
	case "ctrl+l":
		return btea.KeyPressMsg{Code: 'l', Mod: btea.ModCtrl}
	case "up":
		return btea.KeyPressMsg{Code: btea.KeyUp}
	case "down":
		return btea.KeyPressMsg{Code: btea.KeyDown}
	case "backspace":
		return btea.KeyPressMsg{Code: btea.KeyBackspace}
	}
	return btea.KeyPressMsg{Code: rune(s[0]), Text: s}
}

func typeText(h *harness, s string) {
	for _, r := range s {
		h.press(kp(string(r)))
	}
}

func permReq(id string) bus.PermissionRequestedMsg {
	var m bus.PermissionRequestedMsg
	m.Meta = bus.Meta{Session: "s1"}
	m.PermissionRequested = event.PermissionRequested{
		ID: id, Call: "c1", Tool: "sh",
		Consequence: event.Consequence{Kind: "command", Content: "go test ./..."},
	}
	return m
}

// TestSubmitCreatesSessionAndEntersChat: typing plus enter walks the
// landing-to-chat transition through a real submit round trip.
func TestSubmitCreatesSessionAndEntersChat(t *testing.T) {
	h := newHarness(t, Options{})
	typeText(h, "hi")
	h.press(kp("enter"))
	if got := h.client.submits; len(got) != 1 || got[0] != "s1:hi" {
		t.Fatalf("submits = %v, want [s1:hi]", got)
	}
	if h.m.session != "s1" || h.m.state != StateChat {
		t.Fatalf("session=%q state=%v, want s1 in chat", h.m.session, h.m.state)
	}
	if h.m.editor.Value() != "" {
		t.Fatal("editor kept the submitted text")
	}
}

// TestRouterDialogOwnsInput: with a permission dialog open, chat and
// editor keys go nowhere and the dialog's choice keys work.
func TestRouterDialogOwnsInput(t *testing.T) {
	h := newHarness(t, Options{})
	typeText(h, "x")
	h.send(permReq("p1"))
	if h.m.overlay.Len() != 1 {
		t.Fatal("permission request did not open a dialog")
	}
	h.press(kp("z")) // would type into the editor if routing leaked
	if got := h.m.editor.Value(); got != "x" {
		t.Fatalf("editor received %q while a dialog was open", got)
	}
	h.press(kp("d")) // deny shortcut on the perm dialog
	if h.m.overlay.Len() != 0 {
		t.Fatal("decision did not close the dialog")
	}
	if got := h.client.responds; len(got) != 1 || got[0] != [3]string{"s1", "p1", "deny"} {
		t.Fatalf("responds = %v, want the deny on s1/p1", got)
	}
}

// TestPermGraceSwallowsInFlightKey: a key arriving right after the
// prompt opens is swallowed, not turned into an approval (doc 02
// section 7.3).
func TestPermGraceSwallowsInFlightKey(t *testing.T) {
	h := newHarness(t, Options{})
	h.send(permReq("p1"))
	h.send(kp("a")) // no clock advance: mid-keystroke
	if len(h.client.responds) != 0 {
		t.Fatalf("in-flight key approved a permission: %v", h.client.responds)
	}
	if h.m.overlay.Len() != 1 {
		t.Fatal("dialog vanished during grace")
	}
}

// TestPermResolvedElsewherePops: a rule resolving the request closes
// the stale prompt.
func TestPermResolvedElsewherePops(t *testing.T) {
	h := newHarness(t, Options{})
	h.send(permReq("p1"))
	var res bus.PermissionResolvedMsg
	res.Meta = bus.Meta{Session: "s1"}
	res.PermissionResolved = event.PermissionResolved{ID: "p1", Behavior: "allow"}
	h.send(res)
	if h.m.overlay.Len() != 0 {
		t.Fatal("resolved request left its dialog up")
	}
}

// TestFocusToggleRoutesChatKeys: ctrl+w moves focus and j/k scroll the
// chat instead of typing.
func TestFocusToggleRoutesChatKeys(t *testing.T) {
	h := newHarness(t, Options{})
	h.press(kp("ctrl+w"))
	if h.m.focus != FocusChat {
		t.Fatal("focus did not move to the chat")
	}
	h.press(kp("j"))
	if h.m.editor.Value() != "" {
		t.Fatal("chat-scope key leaked into the editor")
	}
	h.press(kp("ctrl+w"))
	if h.m.focus != FocusEditor {
		t.Fatal("focus did not come back")
	}
}

// TestThemePickSwitches: ctrl+t opens the picker; choosing light swaps
// the palette everywhere.
func TestThemePickSwitches(t *testing.T) {
	h := newHarness(t, Options{})
	h.press(kp("ctrl+t"))
	if h.m.overlay.Len() != 1 {
		t.Fatal("theme picker did not open")
	}
	typeText(h, "light")
	h.press(kp("enter"))
	if h.m.theme.Name != "light" {
		t.Fatalf("theme = %q, want light", h.m.theme.Name)
	}
	if h.m.overlay.Len() != 0 {
		t.Fatal("picker stayed open after choosing")
	}
}

// TestPaletteDebugToggle: the palette's debug entry flips the sidebar's
// drop counter surface.
func TestPaletteDebugToggle(t *testing.T) {
	h := newHarness(t, Options{Drops: func() uint64 { return 3 }})
	h.press(kp("ctrl+p"))
	typeText(h, "debug")
	h.press(kp("enter"))
	if !h.m.sidebar.st.Debug {
		t.Fatal("palette debug entry did not toggle the sidebar")
	}
}

// TestSessionSwitcherFilters: the palette loads sessions and the picker
// filters them; choosing one retargets future turns.
func TestSessionSwitcherFilters(t *testing.T) {
	h := newHarness(t, Options{})
	h.client.sessions = []SessionInfo{
		{ID: "s1", Title: "fix the bus"},
		{ID: "s2", Title: "write the docs"},
	}
	h.press(kp("ctrl+p"))
	typeText(h, "session")
	h.press(kp("enter"))
	if h.m.overlay.Len() != 1 {
		t.Fatal("session switcher did not open")
	}
	typeText(h, "docs")
	h.press(kp("enter"))
	if h.m.session != "s2" {
		t.Fatalf("session = %q, want s2", h.m.session)
	}
}

// TestRebindTakesEffectNextKeypress: after SetKeymap, the old submit
// key types and the new one submits, no restart.
func TestRebindTakesEffectNextKeypress(t *testing.T) {
	h := newHarness(t, Options{})
	km, err := keys.Default().Apply(map[string][]string{"submit": {"ctrl+s"}})
	if err != nil {
		t.Fatal(err)
	}
	h.m.SetKeymap(km)
	typeText(h, "hello")
	h.press(kp("enter")) // now types a newline path, not submit
	if len(h.client.submits) != 0 {
		t.Fatal("old binding still submits after the rebind")
	}
	h.press(btea.KeyPressMsg{Code: 's', Mod: btea.ModCtrl})
	if len(h.client.submits) != 1 {
		t.Fatalf("new binding did not submit: %v", h.client.submits)
	}
}

// TestOnboardingFlow: first run walks provider, credential, tier, init
// through the overlay and lands with the outcome persisted.
func TestOnboardingFlow(t *testing.T) {
	var saved []string
	h := newHarness(t, Options{FirstRun: true, Onboarded: func(o splash.Outcome) error {
		saved = append(saved, o.Provider+"/"+o.Tier)
		return nil
	}})
	if h.m.state != StateOnboarding || h.m.overlay.Len() != 1 {
		t.Fatal("first run did not open onboarding")
	}
	h.press(kp("enter")) // provider: first option
	h.press(kp("enter")) // credential notice
	h.press(kp("enter")) // tier: first option
	h.press(kp("enter")) // init: first option
	if h.m.state != StateLanding || h.m.overlay.Len() != 0 {
		t.Fatalf("after onboarding: state=%v overlays=%d", h.m.state, h.m.overlay.Len())
	}
	if len(saved) != 1 {
		t.Fatalf("outcome persisted %d times, want once", len(saved))
	}
}

// TestForeignSessionEventsIgnored: once pinned to a session, another
// session's stream does not touch this view.
func TestForeignSessionEventsIgnored(t *testing.T) {
	h := newHarness(t, Options{})
	typeText(h, "hi")
	h.press(kp("enter"))
	var msg bus.TurnStartedMsg
	msg.Meta = bus.Meta{Session: "other"}
	msg.TurnStarted = event.TurnStarted{ID: "t9", Prompt: "not ours"}
	h.send(msg)
	if !h.m.chat.Empty() {
		t.Fatal("foreign session's turn landed in this view")
	}
}

// TestViewGolden pins the landing frame and a small conversation frame,
// plain text, both layout modes.
func TestViewGolden(t *testing.T) {
	h := newHarness(t, Options{
		Cwd: "/home/tam/ari", Model: "claude-sonnet-5", Provider: "anthropic",
		ContextWindow: 200000,
	})
	var b strings.Builder
	b.WriteString("== landing wide ==\n" + renderPlain(h.m, 140, 32))

	var ts bus.TurnStartedMsg
	ts.Meta = bus.Meta{Session: "s1", Turn: "t1"}
	ts.TurnStarted = event.TurnStarted{ID: "t1", Prompt: "say hi"}
	h.send(ts)
	var td bus.TextDeltaMsg
	td.Meta = bus.Meta{Session: "s1", Turn: "t1"}
	td.TextDelta = event.TextDelta{Part: 0, Text: "hello from the ant"}
	h.send(td)
	var tf bus.TurnFinishedMsg
	tf.Meta = bus.Meta{Session: "s1", Turn: "t1"}
	tf.TurnFinished = event.TurnFinished{ID: "t1", Reason: "end_turn"}
	h.send(tf)
	b.WriteString("== chat wide ==\n" + renderPlain(h.m, 140, 32))
	b.WriteString("== chat compact ==\n" + renderPlain(h.m, 80, 24))
	eval.Golden(t, "frames", b.String())
}

// workerWoke builds a woke message for the current session so it survives the
// root's session filter and reaches the colony controller.
func workerWoke(ant, task string) bus.WorkerWokeMsg {
	var m bus.WorkerWokeMsg
	m.Meta = bus.Meta{Session: "s1"}
	m.WorkerWoke = event.WorkerWoke{Ant: ant, Task: task, Tier: "cheap", File: "surveyor." + task}
	return m
}

// TestColonyPanelTogglesAndShowsLiveAnts drives the panel end to end from the
// root: a worker wakes on the live session, ctrl+l opens the panel, the frame
// shows the live ant, and escape closes it. The worker event reaches the panel
// through the same projection path every other panel reads.
func TestColonyPanelTogglesAndShowsLiveAnts(t *testing.T) {
	h := newHarness(t, Options{})
	typeText(h, "hi")
	h.press(kp("enter")) // into chat on session s1

	h.send(workerWoke("forager-0", "sub-a"))
	h.send(workerWoke("forager-1", "sub-b"))

	h.press(kp("ctrl+l"))
	if h.m.overlay.Len() != 1 {
		t.Fatal("ctrl+l did not open the colony panel")
	}
	out := renderPlain(h.m, 120, 40)
	for _, want := range []string{"forager-0", "forager-1"} {
		if !strings.Contains(out, want) {
			t.Errorf("open colony panel missing %q:\n%s", want, out)
		}
	}

	h.press(kp("esc"))
	if h.m.overlay.Len() != 0 {
		t.Fatal("escape did not close the colony panel")
	}
	if h.m.colony.open {
		t.Fatal("closing the panel left the open flag set")
	}
}

// TestColonyDrillInFromRoot drives the drill-in through the whole root: a
// worker wakes carrying its sidechain locator, the panel opens, the cursor
// walks to the second lane, enter drills in and the root runs the fetch it
// returns, and the frame shows that lane's transcript read-only. This is the
// path a user takes, keys in and a live sidechain out.
func TestColonyDrillInFromRoot(t *testing.T) {
	h := newHarness(t, Options{})
	h.client.scripts = map[string][]parts.Part{
		"surveyor.sub-b": {{Kind: parts.KindText, Role: parts.RoleAssistant, Text: "found the leak in cache.go"}},
	}
	typeText(h, "hi")
	h.press(kp("enter")) // into chat on session s1

	h.send(workerWoke("forager-0", "sub-a"))
	h.send(workerWoke("forager-1", "sub-b"))

	h.press(kp("ctrl+l"))
	h.press(kp("down"))  // cursor to forager-1
	h.press(kp("enter")) // drill in; the root runs the fetch this returns

	out := renderPlain(h.m, 120, 40)
	if !strings.Contains(out, "found the leak in cache.go") {
		t.Errorf("drill-in did not show the worker transcript:\n%s", out)
	}
	if !strings.Contains(out, "read-only") {
		t.Errorf("drill-in missing the read-only marker:\n%s", out)
	}

	h.press(kp("backspace"))
	back := renderPlain(h.m, 120, 40)
	if !strings.Contains(back, "forager-0") || !strings.Contains(back, "forager-1") {
		t.Errorf("backspace did not return to the list:\n%s", back)
	}
}

// renderPlain draws the full frame and strips styling for asserting.
func renderPlain(m *Model, w, h int) string {
	m.Update(btea.WindowSizeMsg{Width: w, Height: h})
	return ansi.Strip(m.View().Content) + "\n"
}
