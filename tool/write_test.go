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

func callWrite(t *testing.T, tc *ToolContext, args string) (*Result, error) {
	t.Helper()
	w := NewWrite()
	if err := w.ValidateInput(context.Background(), json.RawMessage(args), tc); err != nil {
		return nil, err
	}
	return w.Call(context.Background(), json.RawMessage(args), tc, nil)
}

func TestWriteCreatesAFileAndItsParents(t *testing.T) {
	tc := testContext(t)
	path := filepath.Join(tc.Cwd, "deep", "nested", "new.txt")

	res, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"one\ntwo\n"}`, path))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "one\ntwo\n" {
		t.Errorf("file = %q", got)
	}
	if !strings.Contains(res.Model, "(2 lines)") {
		t.Errorf("model = %q, want the line count", res.Model)
	}
	display, ok := res.Display.(WriteDisplay)
	if !ok || !display.Created {
		t.Errorf("display = %#v, want Created true", res.Display)
	}
}

func TestWriteOnAnUnreadExistingFileIsRefused(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "config.yaml", "port: 8080\n")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"port: 9090\n"}`, path))
	if err == nil {
		t.Fatal("overwriting an unread file must be refused")
	}
	want := fmt.Sprintf("cannot overwrite %s: it has not been read, or it changed since you read it; read it first", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "port: 8080\n" {
		t.Error("a refused write must leave the file byte-identical")
	}
}

func TestWriteAfterAReadOverwritesAndRefreshes(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "config.yaml", "port: 8080\n")
	resolved := readIntoState(t, tc, path)

	res, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"port: 9090\n"}`, path))
	if err != nil {
		t.Fatalf("write after read: %v", err)
	}
	display, ok := res.Display.(WriteDisplay)
	if !ok || display.Created {
		t.Errorf("display = %#v, want Created false for an overwrite", res.Display)
	}
	cur, _ := os.ReadFile(path)
	if !tc.Files.Fresh(resolved, HashBytes(cur)) {
		t.Error("the map must be fresh for the file the write just produced")
	}
}

func TestWriteOnAFileChangedSinceTheReadIsRefused(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "config.yaml", "port: 8080\n")
	readIntoState(t, tc, path)

	// Someone else moves the file after the read.
	writeFile(t, tc.Cwd, "config.yaml", "port: 8081\n")

	_, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"port: 9090\n"}`, path))
	if err == nil {
		t.Fatal("overwriting a changed file must be refused")
	}
	if !strings.Contains(err.Error(), "read it first") {
		t.Errorf("reason = %q", err.Error())
	}
}

// TestWriteThenEditNeedsNoInterveningRead is the create-then-edit flow
// from the slice DoD: the write's own refresh arms the gate.
func TestWriteThenEditNeedsNoInterveningRead(t *testing.T) {
	tc := testContext(t)
	path := filepath.Join(tc.Cwd, "fresh.go")

	if _, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"package main\n"}`, path)); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"package main","new_string":"package app"}`, path)); err != nil {
		t.Fatalf("edit right after create, no read: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package app\n" {
		t.Errorf("file = %q", got)
	}
}

func TestWritePreservesTheExistingMode(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "run.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(path, 0o755); err != nil {
		t.Fatal(err)
	}
	readIntoState(t, tc, path)

	if _, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"#!/bin/sh\necho bye\n"}`, path)); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %v, want 0755 preserved across the overwrite", info.Mode().Perm())
	}
}

// TestWriteIsAtomicViaTempFileAndRename checks the observable half of
// atomicity: the content swaps whole and no temp file survives in the
// directory afterward (doc 04 section 5.1).
func TestWriteIsAtomicViaTempFileAndRename(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "data.txt", "old\n")
	readIntoState(t, tc, path)

	if _, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"new\n"}`, path)); err != nil {
		t.Fatalf("write: %v", err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".ari-write-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind: %v", leftovers)
	}
}

func TestWriteRelativePathIsAValidationError(t *testing.T) {
	tc := testContext(t)
	_, err := callWrite(t, tc, `{"file_path":"./x","content":"y"}`)
	if err == nil {
		t.Fatal("want a validation error for a relative path")
	}
	want := `file_path must be an absolute path, got "./x"; resolve it against the working directory first`
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestWriteEmptyContentCountsZeroLines(t *testing.T) {
	tc := testContext(t)
	path := filepath.Join(tc.Cwd, "empty.txt")

	res, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":""}`, path))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if res.StateEffect == nil || res.StateEffect.Lines != 0 {
		t.Errorf("effect = %#v, want 0 lines", res.StateEffect)
	}
}

func TestWriteConfirmationRendererGolden(t *testing.T) {
	tc := testContext(t)
	path := filepath.Join(tc.Cwd, "hello.go")

	res, err := callWrite(t, tc, fmt.Sprintf(`{"file_path":%q,"content":"package main\n\nfunc main() {}\n"}`, path))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	root, err := filepath.EvalSymlinks(tc.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(res.Model, root, "<root>")
	got = strings.ReplaceAll(got, tc.Cwd, "<root>")
	eval.Golden(t, "write_confirm", got)
}
