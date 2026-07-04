package tool

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tamnd/ari/lsp"
)

// fakeLSP is a stand-in language server keyed on file content: on a touch
// it reads the file and reports one error when the content carries an
// obvious type mistake, so a test can introduce a Go error and see it
// clear on the fix without a real gopls. The real Service is exercised in
// the lsp package; here the seam is what matters.
type fakeLSP struct {
	mu      sync.Mutex
	diags   map[string][]lsp.Diagnostic
	touches int
}

func newFakeLSP() *fakeLSP { return &fakeLSP{diags: map[string][]lsp.Diagnostic{}} }

func (f *fakeLSP) Touch(_ context.Context, path string, _ lsp.TouchKind) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touches++
	// A string literal assigned to an int is the introduced error the
	// fixture toggles; anything else is clean.
	if strings.Contains(string(b), `int = "`) {
		f.diags[path] = []lsp.Diagnostic{{
			Line: 3, Col: 5, Severity: "error",
			Message: "cannot use untyped string as int value in variable declaration",
		}}
	} else {
		delete(f.diags, path)
	}
	return nil
}

func (f *fakeLSP) Diagnostics(path string) []lsp.Diagnostic {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.diags[path]
}

// projectFakeLSP adds the optional outward-looking half of the seam, so a
// write folds capped diagnostics for other files that went red.
type projectFakeLSP struct {
	*fakeLSP
	project map[string][]lsp.Diagnostic
}

func (p *projectFakeLSP) ProjectDiagnostics(exclude string) map[string][]lsp.Diagnostic {
	out := map[string][]lsp.Diagnostic{}
	for path, ds := range p.project {
		if path != exclude {
			out[path] = ds
		}
	}
	return out
}

// TestEditFoldsIntroducedErrorAndClearsItOnFix is the slice 6 fixture: an
// edit that introduces a Go type error comes back with that error in the
// model-facing result, and the corrected edit comes back clean.
func TestEditFoldsIntroducedErrorAndClearsItOnFix(t *testing.T) {
	tc := testContext(t)
	tc.LSP = newFakeLSP()
	const clean = "package main\n\nvar x int = 0\n"
	path := writeFile(t, tc.Cwd, "main.go", clean)
	readIntoState(t, tc, path)

	// Introduce the error.
	res, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"int = 0","new_string":"int = \"oops\""}`, path))
	if err != nil {
		t.Fatalf("introducing edit: %v", err)
	}
	if !strings.Contains(res.Model, "<diagnostics") || !strings.Contains(res.Model, "ERROR [3:5]") {
		t.Fatalf("introduced error not folded into result:\n%s", res.Model)
	}
	disp, ok := res.Display.(EditDisplay)
	if !ok || len(disp.Diagnostics) != 1 {
		t.Fatalf("edit display carries %d diagnostics, want 1", len(disp.Diagnostics))
	}

	// Re-read so the gate is armed on the now-broken file, then correct it.
	readIntoState(t, tc, path)
	res, err = callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"int = \"oops\"","new_string":"int = 0"}`, path))
	if err != nil {
		t.Fatalf("correcting edit: %v", err)
	}
	if strings.Contains(res.Model, "<diagnostics") {
		t.Fatalf("corrected edit still carries diagnostics:\n%s", res.Model)
	}
	if disp := res.Display.(EditDisplay); len(disp.Diagnostics) != 0 {
		t.Fatalf("corrected edit display carries %d diagnostics, want 0", len(disp.Diagnostics))
	}
}

// TestWriteFoldsOwnAndProjectDiagnostics proves a whole-file overwrite
// reports its own file's error and the capped project-wide errors for
// callers that newly went red.
func TestWriteFoldsOwnAndProjectDiagnostics(t *testing.T) {
	tc := testContext(t)
	caller := writeFile(t, tc.Cwd, "caller.go", "package main\n")
	fake := &projectFakeLSP{
		fakeLSP: newFakeLSP(),
		project: map[string][]lsp.Diagnostic{
			caller: {{Line: 7, Col: 2, Severity: "error", Message: "not enough arguments in call to Run"}},
		},
	}
	tc.LSP = fake

	path := writeFile(t, tc.Cwd, "main.go", "package main\n\nvar x int = 0\n")
	readIntoState(t, tc, path)

	res, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":%q}`, path, "package main\n\nvar x int = \"oops\"\n"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if !strings.Contains(res.Model, "main.go") || !strings.Contains(res.Model, "ERROR [3:5]") {
		t.Fatalf("own-file error not folded into write result:\n%s", res.Model)
	}
	if !strings.Contains(res.Model, "caller.go") || !strings.Contains(res.Model, "not enough arguments") {
		t.Fatalf("project-wide error not folded into write result:\n%s", res.Model)
	}
}

// TestWarmTouchesInBackground proves read fires a background full sync so
// the server has parsed the file before the edit that follows.
func TestWarmTouchesInBackground(t *testing.T) {
	tc := testContext(t)
	fake := newFakeLSP()
	tc.LSP = fake
	path := writeFile(t, tc.Cwd, "main.go", "package main\n\nvar x int = 0\n")

	if _, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path)); err != nil {
		t.Fatalf("read: %v", err)
	}
	// The warm is fire-and-forget, so poll briefly for the touch to land.
	for range 100 {
		fake.mu.Lock()
		n := fake.touches
		fake.mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("read did not warm the language server in the background")
}
