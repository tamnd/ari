package eval

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
)

func TestMain(m *testing.M) {
	Main(m)
}

// recorder is a fake TB so the self-tests can prove a helper fails when
// it should, which is the point of building the harness first.
type recorder struct {
	errors []string
	fatal  string
}

func (r *recorder) Helper() {}
func (r *recorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}
func (r *recorder) Fatalf(format string, args ...any) {
	r.fatal = fmt.Sprintf(format, args...)
	panic(r) // unwind like testing.T.Fatalf; recovered by the caller
}

func expectFatal(t *testing.T, r *recorder, fn func()) {
	t.Helper()
	defer func() {
		if rec := recover(); rec != nil && rec != any(r) {
			panic(rec)
		}
	}()
	fn()
}

func TestGoldenCatchesDrift(t *testing.T) {
	dir := t.TempDir()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(cwd); err != nil {
			t.Fatal(err)
		}
	})

	path := filepath.Join("testdata", "self.golden")
	if err := os.MkdirAll("testdata", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("stable render\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	ok := &recorder{}
	Golden(ok, "self", "stable render\n")
	if len(ok.errors) != 0 || ok.fatal != "" {
		t.Errorf("matching golden reported a failure: %v %v", ok.errors, ok.fatal)
	}

	drift := &recorder{}
	Golden(drift, "self", "drifted render\n")
	if len(drift.errors) == 0 {
		t.Error("a drifted golden must fail the test")
	}

	missing := &recorder{}
	expectFatal(t, missing, func() { Golden(missing, "absent", "anything") })
	if missing.fatal == "" || !strings.Contains(missing.fatal, "-update") {
		t.Errorf("a missing golden must be fatal and name the update flag: %q", missing.fatal)
	}
}

func TestWithinBudgetCatchesOverrun(t *testing.T) {
	b := Budget{Name: "refactor-demo", Input: 12000, Output: 3000, Turns: 6}

	var under Usage
	under.Add(4000, 1000, 3000)
	under.Add(1000, 500, 3000)
	ok := &recorder{}
	WithinBudget(ok, under, b)
	if len(ok.errors) != 0 {
		t.Errorf("an in-budget task failed: %v", ok.errors)
	}

	var over Usage
	over.Add(9000, 2500, 4000) // 13000 input crosses the 12000 ceiling
	bad := &recorder{}
	WithinBudget(bad, over, b)
	if len(bad.errors) != 1 || !strings.Contains(bad.errors[0], "input tokens") {
		t.Errorf("an overrun must fail naming the dimension: %v", bad.errors)
	}

	var chatty Usage
	for range 7 {
		chatty.Add(100, 10, 0)
	}
	calls := &recorder{}
	WithinBudget(calls, chatty, b)
	if len(calls.errors) != 1 || !strings.Contains(calls.errors[0], "provider calls") {
		t.Errorf("a call-count overrun must fail: %v", calls.errors)
	}
}

func mustEvent(t *testing.T, typ event.Type, payload any) event.Event {
	t.Helper()
	e, err := event.New(typ, "s1", "t1", payload)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestReplayRoundTripAndMismatch(t *testing.T) {
	want := []event.Event{
		mustEvent(t, event.TypeTurnStarted, event.TurnStarted{ID: "t1", Ant: "worker", Prompt: "hi"}),
		mustEvent(t, event.TypeTextDelta, event.TextDelta{Part: 0, Text: "hello"}),
		mustEvent(t, event.TypeTurnFinished, event.TurnFinished{ID: "t1", Reason: "done"}),
	}
	s := Script{Name: "self", Prompt: "hi", Want: want}

	// The same sequence with different Seq and Time must pass: those are
	// run-time assignments, not contract.
	replayed := make([]event.Event, len(want))
	copy(replayed, want)
	for i := range replayed {
		replayed[i].Seq = uint64(i + 100)
		replayed[i].Time = replayed[i].Time.Add(5 * time.Minute)
	}
	ok := &recorder{}
	Replay(ok, s, func(Script) ([]event.Event, error) { return replayed, nil })
	if len(ok.errors) != 0 || ok.fatal != "" {
		t.Errorf("an identical replay failed: %v %v", ok.errors, ok.fatal)
	}

	// A diverged payload must fail pointing at the event index.
	diverged := make([]event.Event, len(want))
	copy(diverged, want)
	diverged[1] = mustEvent(t, event.TypeTextDelta, event.TextDelta{Part: 0, Text: "goodbye"})
	bad := &recorder{}
	Replay(bad, s, func(Script) ([]event.Event, error) { return diverged, nil })
	if len(bad.errors) != 1 || !strings.Contains(bad.errors[0], "event 1") {
		t.Errorf("a diverged replay must fail at the index: %v", bad.errors)
	}

	// A truncated sequence must fail on length.
	short := &recorder{}
	Replay(short, s, func(Script) ([]event.Event, error) { return want[:2], nil })
	if len(short.errors) != 1 || !strings.Contains(short.errors[0], "got 2 events, want 3") {
		t.Errorf("a truncated replay must fail on length: %v", short.errors)
	}
}

func TestScriptSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.json")
	s := Script{
		Name:   "demo",
		Prompt: "rename Greeting to Greet",
		Want: []event.Event{
			mustEvent(t, event.TypeTurnStarted, event.TurnStarted{ID: "t1", Ant: "worker", Prompt: "rename"}),
		},
	}
	if err := SaveScript(path, s); err != nil {
		t.Fatal(err)
	}
	back, err := LoadScript(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Name != s.Name || back.Prompt != s.Prompt || len(back.Want) != 1 {
		t.Errorf("round trip lost data: %+v", back)
	}
	if back.Want[0].Type != event.TypeTurnStarted {
		t.Errorf("event type lost: %s", back.Want[0].Type)
	}
	// The file is human-readable, indented JSON.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "\n  \"name\": \"demo\"") {
		t.Error("replay sets must be indented for human reading")
	}
}

// TestNoLeaksCatchesALeakedGoroutine proves the goleak wiring sees a
// deliberately leaked goroutine, then stops it so the package's own
// TestMain stays green.
func TestNoLeaksCatchesALeakedGoroutine(t *testing.T) {
	stop := make(chan struct{})
	started := make(chan struct{})
	go func() {
		close(started)
		<-stop
	}()
	<-started

	leak := &recorder{}
	NoLeaks(leak)
	if len(leak.errors) == 0 {
		t.Error("NoLeaks missed a live goroutine")
	}
	close(stop)
}

// TestHarnessImports pins the no-feature-dependency rule: eval may import
// the standard library, goleak, and event/, nothing else from the tree.
func TestHarnessImports(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		data, err := os.ReadFile(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.SplitSeq(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.Contains(line, `"github.com/tamnd/ari/`) {
				continue
			}
			if !strings.Contains(line, `"github.com/tamnd/ari/event"`) {
				t.Errorf("%s: the harness may only import event/ from the tree, found %s", e.Name(), line)
			}
		}
	}
}
