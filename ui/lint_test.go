package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootStaysSmall is the soft line-count lint from doc 02 section 18:
// the named anti-pattern is a 4300-line root model, and this test is
// the tripwire. If a file here outgrows the cap, split a controller
// out; do not raise the cap without a spec change.
func TestRootStaysSmall(t *testing.T) {
	const cap = 400
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(b), "\n"); n > cap {
			t.Errorf("%s is %d lines, cap %d: behavior belongs in a controller, coordination in the root", f, n, cap)
		}
	}
}
