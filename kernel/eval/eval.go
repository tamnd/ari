// Package eval is the harness the rest of ari is tested with: golden
// files for every renderer, offline replay sets for the loop, token
// budgets for the demo tasks, and goleak for every package (D23).
//
// It exists before the features it gates. The only tree dependency it is
// allowed is event/, the shared vocabulary; depending on a feature
// package would make it circular with the code it tests, and a test
// asserts the import set stays that small.
package eval

import (
	"flag"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"
)

// TB is the slice of testing.TB the helpers need. It exists so the
// harness self-tests can hand in a recorder and prove a drifted golden or
// a blown budget really fails, not just hopefully fails.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// update is the one flag behind every golden in the repo:
//
//	go test ./<pkg> -args -update
//
// regenerates that package's goldens, and the diff shows up in review.
var update = flag.Bool("update", false, "rewrite golden files instead of diffing")

// Updating reports whether this test run should rewrite goldens.
func Updating() bool { return *update }

// Golden compares got against testdata/<name>.golden in the calling
// package, creating or rewriting the file under -update. Names may carry
// slashes for grouping (theme/dark/read_result).
func Golden(t TB, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name)+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("golden %s: %v", name, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("golden %s: %v (regenerate with -args -update)", name, err)
	}
	if got != string(want) {
		t.Errorf("golden %s drifted; if the change is deliberate, regenerate with -args -update\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

// Main is the TestMain every package uses so a leaked goroutine fails the
// build (doc 01 section 6.6):
//
//	func TestMain(m *testing.M) { eval.Main(m) }
func Main(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// NoLeaks asserts no unexpected goroutine is alive right now. Tests that
// start and stop a component mid-test use this to catch the leak at the
// call site rather than at package exit.
func NoLeaks(t TB) {
	t.Helper()
	if err := goleak.Find(); err != nil {
		t.Errorf("leaked goroutines: %v", err)
	}
}

// Budget is a named token ceiling for a demo task. The budgets are
// asserted constants, not aspirations (plan/01 section 6); zero means
// that dimension is unbounded.
type Budget struct {
	Name   string
	Input  int64 // input tokens, cache reads included
	Output int64
	Turns  int // provider calls
}

// Usage is what a task actually spent, summed from ledger.turn events.
type Usage struct {
	Input  int64
	Output int64
	Turns  int
}

// Add accumulates one ledger turn into the usage.
func (u *Usage) Add(input, output, cacheRead int64) {
	u.Input += input + cacheRead
	u.Output += output
	u.Turns++
}

// WithinBudget fails the test when the usage crosses the budget, naming
// the dimension and the overrun so a regression reads like a perf report.
func WithinBudget(t TB, u Usage, b Budget) {
	t.Helper()
	check := func(dim string, got, max int64) {
		if max > 0 && got > max {
			t.Errorf("budget %s: %s %d exceeds the %d ceiling by %d", b.Name, dim, got, max, got-max)
		}
	}
	check("input tokens", u.Input, b.Input)
	check("output tokens", u.Output, b.Output)
	check("provider calls", int64(u.Turns), int64(b.Turns))
}
