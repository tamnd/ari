package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/kernel/eval"
)

// testContext builds the temp-dir ToolContext every tool test runs
// against, no real session needed (doc 04 section 2.3).
func testContext(t *testing.T) *ToolContext {
	t.Helper()
	dir := t.TempDir()
	return &ToolContext{
		Cwd:   dir,
		Files: NewFileState(),
		Spill: NewDiskSpill(filepath.Join(dir, ".ari", "spill")),
	}
}

func writeFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func callRead(t *testing.T, tc *ToolContext, args string) (*Result, error) {
	t.Helper()
	r := NewRead()
	if err := r.ValidateInput(context.Background(), json.RawMessage(args), tc); err != nil {
		return nil, err
	}
	return r.Call(context.Background(), json.RawMessage(args), tc, nil)
}

func TestReadNumbersFromTheTrueFileLine(t *testing.T) {
	tc := testContext(t)
	var b strings.Builder
	for i := 1; i <= 10; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	path := writeFile(t, tc.Cwd, "ten.txt", b.String())

	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q,"offset":4,"limit":3}`, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// An offset read shows true file line numbers, not slice-relative
	// ones (doc 04 section 4.1).
	want := "     4\tline 4\n     5\tline 5\n     6\tline 6\n"
	if !strings.HasPrefix(res.Model, want) {
		t.Errorf("offset read = %q, want prefix %q", res.Model, want)
	}
	if !strings.Contains(res.Model, "(showing lines 4-6 of 10; continue with offset=7)") {
		t.Errorf("truncated page must report where to continue, got %q", res.Model)
	}
}

func TestRelativePathIsAModelFacingValidationError(t *testing.T) {
	tc := testContext(t)
	_, err := callRead(t, tc, `{"file_path":"./x"}`)
	if err == nil {
		t.Fatal("want a validation error for a relative path")
	}
	want := `file_path must be an absolute path, got "./x"; resolve it against the working directory first`
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestUnchangedRereadReturnsTheStub(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "a.txt", "one\ntwo\nthree\n")
	args := fmt.Sprintf(`{"file_path":%q}`, path)

	first, err := callRead(t, tc, args)
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	tc.Files.Apply(first.StateEffect)

	second, err := callRead(t, tc, args)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if second.Model != "(file unchanged since last read, 3 lines)" {
		t.Errorf("re-read = %q, want the stub", second.Model)
	}

	// A change on disk brings the content back.
	writeFile(t, tc.Cwd, "a.txt", "one\ntwo\nthree\nfour\n")
	third, err := callRead(t, tc, args)
	if err != nil {
		t.Fatalf("third read: %v", err)
	}
	if !strings.Contains(third.Model, "\tfour") {
		t.Errorf("a changed file must be re-sent in full, got %q", third.Model)
	}
}

func TestEverySuccessfulReadWritesAFileStateEffect(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "b.txt", "alpha\nbeta\n")

	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	e := res.StateEffect
	if e == nil {
		t.Fatal("a successful read must arm the edit gate (D8)")
	}
	resolved, _ := filepath.EvalSymlinks(path)
	if e.Path != resolved {
		t.Errorf("effect path = %q, want %q", e.Path, resolved)
	}
	if e.Hash != HashBytes([]byte("alpha\nbeta\n")) {
		t.Errorf("effect hash = %q, want the content hash", e.Hash)
	}
	if e.Lines != 2 {
		t.Errorf("effect lines = %d, want 2", e.Lines)
	}
	if e.Mtime.IsZero() {
		t.Error("effect mtime must be set")
	}
}

func TestReadResolvesSymlinksForTheStateKey(t *testing.T) {
	tc := testContext(t)
	target := writeFile(t, tc.Cwd, "real.txt", "content\n")
	link := filepath.Join(tc.Cwd, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}

	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, link))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	resolved, _ := filepath.EvalSymlinks(target)
	if res.StateEffect.Path != resolved {
		t.Errorf("a link and its target must share one state key, got %q want %q",
			res.StateEffect.Path, resolved)
	}
}

// TestReadNeverSpills is the DoD check: a file larger than any cap
// comes back paginated, not as a spill pointer (doc 04 section 3.3).
func TestReadNeverSpills(t *testing.T) {
	tc := testContext(t)
	var b strings.Builder
	for i := 1; i <= 5000; i++ {
		fmt.Fprintf(&b, "this line is padding so the file outgrows every result cap %d\n", i)
	}
	path := writeFile(t, tc.Cwd, "huge.txt", b.String())

	r := NewRead()
	if r.MaxResultSize() != 0 {
		t.Fatalf("read.MaxResultSize = %d, want 0 (never spill)", r.MaxResultSize())
	}
	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	capped, err := ApplyResultBudget(res, r, tc)
	if err != nil {
		t.Fatalf("budget: %v", err)
	}
	if capped != res.Model {
		t.Error("the loop's budget must pass a read result through untouched")
	}
	if strings.Contains(capped, "Full output at") {
		t.Error("a read must never hand the model a spill pointer")
	}
	if !strings.Contains(capped, "(showing lines 1-2000 of 5000; continue with offset=2001)") {
		t.Error("a big file must paginate at the 2000-line default and say how to continue")
	}
}

func TestEmptyFileReturnsTheWarning(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "empty.txt", "")

	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if res.Model != "<system-reminder>(file is empty)</system-reminder>" {
		t.Errorf("empty read = %q, want the warning", res.Model)
	}
	if res.StateEffect == nil {
		t.Error("an empty read still arms the gate")
	}
}

func TestBinaryFileIsRefusedWithTypeAndSize(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "blob.bin", "PK\x03\x04\x00\x00binary\x00stuff")

	_, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err == nil {
		t.Fatal("want a refusal on a binary file")
	}
	if !strings.Contains(err.Error(), "binary file") || !strings.Contains(err.Error(), "bytes") {
		t.Errorf("refusal must name the type and size, got %q", err.Error())
	}
}

func TestOffsetPastTheEndIsAnError(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "short.txt", "only\n")

	_, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q,"offset":9}`, path))
	if err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Errorf("want the past-the-end error, got %v", err)
	}
}

func TestDirectoryIsRefused(t *testing.T) {
	tc := testContext(t)
	_, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, tc.Cwd))
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Errorf("want the directory refusal, got %v", err)
	}
}

// TestReadRendererGolden pins the model-facing cat -n rendering (D23).
func TestReadRendererGolden(t *testing.T) {
	tc := testContext(t)
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"hi\")\n}\n"
	path := writeFile(t, tc.Cwd, "main.go", content)

	full, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	eval.Golden(t, "read_result", full.Model)

	paged, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q,"offset":5,"limit":2}`, path))
	if err != nil {
		t.Fatalf("paged read: %v", err)
	}
	eval.Golden(t, "read_paged", paged.Model)
}
