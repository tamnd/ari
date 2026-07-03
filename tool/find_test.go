package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/kernel/eval"
)

func callFind(t *testing.T, tc *ToolContext, args string) (*Result, error) {
	t.Helper()
	f := NewFind()
	if err := f.ValidateInput(context.Background(), json.RawMessage(args), tc); err != nil {
		return nil, err
	}
	return f.Call(context.Background(), json.RawMessage(args), tc, nil)
}

// touch pins a file's mtime so ranking is deterministic.
func touch(t *testing.T, path string, at time.Time) {
	t.Helper()
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
}

func TestGlobRanksByMtimeNewestFirst(t *testing.T) {
	tc := testContext(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	old := writeFile(t, tc.Cwd, "old.go", "package a\n")
	mid := writeFile(t, tc.Cwd, "sub/mid.go", "package b\n")
	fresh := writeFile(t, tc.Cwd, "fresh.go", "package c\n")
	writeFile(t, tc.Cwd, "notes.txt", "not go\n")
	touch(t, old, base)
	touch(t, mid, base.Add(time.Hour))
	touch(t, fresh, base.Add(2*time.Hour))

	res, err := callFind(t, tc, `{"glob":"**/*.go"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	want := fresh + "\n" + mid + "\n" + old + "\n"
	if res.Model != want {
		t.Errorf("glob result = %q, want newest first %q", res.Model, want)
	}
}

func TestContentSearchRanksByMatchCountThenMtime(t *testing.T) {
	tc := testContext(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	sparse := writeFile(t, tc.Cwd, "sparse.go", "package a\n\nvar needle = 1\n")
	dense := writeFile(t, tc.Cwd, "dense.go", "needle one\nneedle two\nneedle three\n")
	touch(t, sparse, base.Add(time.Hour)) // fresher, but fewer hits
	touch(t, dense, base)

	res, err := callFind(t, tc, `{"content":"needle"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	// The densest file leads even though the sparse one is fresher.
	want := dense + ":1: needle one\n" +
		dense + ":2: needle two\n" +
		dense + ":3: needle three\n" +
		sparse + ":3: var needle = 1\n"
	if res.Model != want {
		t.Errorf("content result = %q, want %q", res.Model, want)
	}

	d, ok := res.Display.(FindDisplay)
	if !ok {
		t.Fatalf("Display = %T, want FindDisplay", res.Display)
	}
	if d.Total != 4 || d.Capped || len(d.Files) != 2 {
		t.Errorf("display = %+v", d)
	}
}

func TestIgnoreRulesSkipNoiseAndGitignore(t *testing.T) {
	tc := testContext(t)
	writeFile(t, tc.Cwd, "keep.go", "needle\n")
	writeFile(t, tc.Cwd, "node_modules/dep/x.go", "needle\n")
	writeFile(t, tc.Cwd, ".git/config", "needle\n")
	writeFile(t, tc.Cwd, "vendor/v.go", "needle\n")
	writeFile(t, tc.Cwd, "dist/out.go", "needle\n")
	writeFile(t, tc.Cwd, "debug.log", "needle\n")
	writeFile(t, tc.Cwd, ".gitignore", "dist/\n*.log\n")

	res, err := callFind(t, tc, `{"content":"needle"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	for _, banned := range []string{"node_modules", ".git/", "vendor", "dist", "debug.log"} {
		if strings.Contains(res.Model, banned) {
			t.Errorf("ignored path %q leaked into the result:\n%s", banned, res.Model)
		}
	}
	if !strings.Contains(res.Model, "keep.go:1: needle") {
		t.Errorf("the real hit went missing:\n%s", res.Model)
	}
}

func TestCapReportsTruncationAndHowToNarrow(t *testing.T) {
	tc := testContext(t)
	var b strings.Builder
	for i := range 10 {
		fmt.Fprintf(&b, "needle %d\n", i)
	}
	writeFile(t, tc.Cwd, "wide.txt", b.String())

	res, err := callFind(t, tc, `{"content":"needle","limit":3}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if !strings.Contains(res.Model, "showing the top 3 of 10 matches, tighten the glob or add a path to see the rest") {
		t.Errorf("cap must say what was dropped and how to narrow:\n%s", res.Model)
	}
	if strings.Count(res.Model, "needle") != 3 {
		t.Errorf("want exactly 3 matches shown:\n%s", res.Model)
	}
	d := res.Display.(FindDisplay)
	if !d.Capped || d.Total != 10 {
		t.Errorf("display = %+v", d)
	}
}

func TestNeitherGlobNorContentIsAValidationError(t *testing.T) {
	tc := testContext(t)
	_, err := callFind(t, tc, `{}`)
	if err == nil {
		t.Fatal("want a validation error")
	}
	want := "set glob to find files by name or content to search inside them"
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestBadRegexpIsAValidationError(t *testing.T) {
	tc := testContext(t)
	_, err := callFind(t, tc, `{"content":"["}`)
	if err == nil || !strings.Contains(err.Error(), "regular expression") {
		t.Errorf("want the regexp error, got %v", err)
	}
}

func TestGlobNarrowsAContentSearch(t *testing.T) {
	tc := testContext(t)
	writeFile(t, tc.Cwd, "a.go", "needle\n")
	writeFile(t, tc.Cwd, "a.txt", "needle\n")

	res, err := callFind(t, tc, `{"glob":"*.go","content":"needle"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if strings.Contains(res.Model, "a.txt") {
		t.Errorf("glob must narrow the content search:\n%s", res.Model)
	}
	if !strings.Contains(res.Model, "a.go:1: needle") {
		t.Errorf("the narrowed hit went missing:\n%s", res.Model)
	}
}

func TestNoMatchesSaysSo(t *testing.T) {
	tc := testContext(t)
	writeFile(t, tc.Cwd, "a.go", "package a\n")

	res, err := callFind(t, tc, `{"content":"no_such_needle"}`)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if res.Model != "(no matches)\n" {
		t.Errorf("empty search = %q", res.Model)
	}
}

// TestFindRendererGolden pins both renderings with the temp root
// normalized out (D23).
func TestFindRendererGolden(t *testing.T) {
	tc := testContext(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	a := writeFile(t, tc.Cwd, "cmd/main.go", "package main\n\nfunc main() { run() }\n")
	b := writeFile(t, tc.Cwd, "run.go", "package main\n\nfunc run() {}\n")
	touch(t, a, base.Add(time.Hour))
	touch(t, b, base)

	paths, err := callFind(t, tc, `{"glob":"**/*.go"}`)
	if err != nil {
		t.Fatalf("glob find: %v", err)
	}
	eval.Golden(t, "find_paths", strings.ReplaceAll(paths.Model, tc.Cwd, "<root>"))

	grep, err := callFind(t, tc, `{"content":"run"}`)
	if err != nil {
		t.Fatalf("content find: %v", err)
	}
	eval.Golden(t, "find_matches", strings.ReplaceAll(grep.Model, tc.Cwd, "<root>"))
}
