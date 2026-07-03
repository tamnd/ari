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

func callEdit(t *testing.T, tc *ToolContext, args string) (*Result, error) {
	t.Helper()
	e := NewEdit()
	if err := e.ValidateInput(context.Background(), json.RawMessage(args), tc); err != nil {
		return nil, err
	}
	return e.Call(context.Background(), json.RawMessage(args), tc, nil)
}

// readIntoState arms the gate the way a real session does: through the
// read tool, then the loop's Files.Apply of the state effect, so the
// state key is the resolved path read records.
func readIntoState(t *testing.T, tc *ToolContext, path string) string {
	t.Helper()
	res, err := callRead(t, tc, fmt.Sprintf(`{"file_path":%q}`, path))
	if err != nil {
		t.Fatalf("arming read: %v", err)
	}
	tc.Files.Apply(res.StateEffect)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func TestEditWithoutAReadIsRejected(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "main.go", "package main\n")
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}

	_, err = callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"package main","new_string":"package app"}`, path))
	if err == nil {
		t.Fatal("editing an unread file must be rejected")
	}
	want := fmt.Sprintf("you have not read %s in this session; read it before editing", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestEditOfAChangedFileIsRejected(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "main.go", "package main\n")
	resolved := readIntoState(t, tc, path)

	// Someone else moves the file after the read.
	writeFile(t, tc.Cwd, "main.go", "package main\n\nfunc main() {}\n")

	_, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"package main","new_string":"package app"}`, path))
	if err == nil {
		t.Fatal("editing a changed file must be rejected")
	}
	want := fmt.Sprintf("%s changed since you read it; read it again before editing", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestZeroOccurrenceIsRejectedWithTheWhitespaceHint(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "main.go", "func main() {\n\treturn\n}\n")
	resolved := readIntoState(t, tc, path)

	_, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"func run() {","new_string":"func run2() {"}`, path))
	if err == nil {
		t.Fatal("a zero-occurrence match must be rejected")
	}
	want := fmt.Sprintf("old_string was not found in %s; the text must match exactly including whitespace and indentation", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

// TestNearMatchIsRejectedNotFuzzed is the adversarial no-fuzzy check
// (D8, doc 04 section 14.1): a near-match that any fuzzy cascade would
// happily land must be a hard rejection, and the file must not move.
func TestNearMatchIsRejectedNotFuzzed(t *testing.T) {
	tc := testContext(t)
	content := "func main() {\n\tfmt.Println(\"hi\")\n}\n"
	path := writeFile(t, tc.Cwd, "main.go", content)
	readIntoState(t, tc, path)

	// Missing one space before the brace, off by a single byte.
	_, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"func main(){","new_string":"func start(){"}`, path))
	if err == nil {
		t.Fatal("a near-match must be rejected, not fuzzily applied")
	}
	if !strings.Contains(err.Error(), "old_string was not found in") {
		t.Errorf("reason = %q, want the exact-match rejection", err.Error())
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != content {
		t.Error("a rejected edit must leave the file byte-identical")
	}
}

func TestNonUniqueMatchNamesTheCountAndBothFixes(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "list.txt", "item\nitem\nitem\n")
	resolved := readIntoState(t, tc, path)

	_, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"item","new_string":"entry"}`, path))
	if err == nil {
		t.Fatal("a non-unique match without replace_all must be rejected")
	}
	want := fmt.Sprintf("old_string matches 3 places in %s; add surrounding lines to make it unique, or set replace_all to true to change all 3", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestReplaceAllChangesEveryOccurrence(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "list.txt", "item\nitem\nitem\n")
	resolved := readIntoState(t, tc, path)

	res, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"item","new_string":"entry","replace_all":true}`, path))
	if err != nil {
		t.Fatalf("replace_all edit: %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "entry\nentry\nentry\n" {
		t.Errorf("file = %q", got)
	}
	want := fmt.Sprintf("edited %s (3 occurrences)", resolved)
	if res.Model != want {
		t.Errorf("model = %q, want %q", res.Model, want)
	}
}

func TestIdenticalStringsAreRejected(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "a.txt", "same\n")
	readIntoState(t, tc, path)

	_, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"same","new_string":"same"}`, path))
	if err == nil {
		t.Fatal("identical old and new must be rejected")
	}
	want := "old_string and new_string are identical, there is nothing to change"
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

// TestTwoEditsInOneTurnBothSucceed proves the refresh after an edit
// keeps the ant editing its own work without a re-read between edits
// (doc 04 section 6.2).
func TestTwoEditsInOneTurnBothSucceed(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "main.go", "package main\n\nfunc main() {}\n")
	readIntoState(t, tc, path)

	if _, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"package main","new_string":"package app"}`, path)); err != nil {
		t.Fatalf("first edit: %v", err)
	}
	if _, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"func main() {}","new_string":"func run() {}"}`, path)); err != nil {
		t.Fatalf("second edit after the first, no re-read: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "package app\n\nfunc run() {}\n" {
		t.Errorf("file = %q", got)
	}
}

// TestExternalWriteBetweenValidationAndCallIsCaught pries validation
// and Call apart the way a real batch does, mutates the file in the
// gap, and requires the re-read under the lock to refuse (doc 04
// section 6.2).
func TestExternalWriteBetweenValidationAndCallIsCaught(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "main.go", "package main\n")
	resolved := readIntoState(t, tc, path)

	e := NewEdit()
	args := json.RawMessage(fmt.Sprintf(`{"file_path":%q,"old_string":"package main","new_string":"package app"}`, path))
	if err := e.ValidateInput(context.Background(), args, tc); err != nil {
		t.Fatalf("validation before the race: %v", err)
	}

	// The world moves between validation and the critical section.
	writeFile(t, tc.Cwd, "main.go", "package main // moved\n")

	_, err := e.Call(context.Background(), args, tc, nil)
	if err == nil {
		t.Fatal("the re-read under the lock must catch the external write")
	}
	want := fmt.Sprintf("%s changed since you read it; read it again before editing", resolved)
	if err.Error() != want {
		t.Errorf("reason = %q, want %q", err.Error(), want)
	}
}

func TestEditRefreshesFileStateAndReportsTheEffect(t *testing.T) {
	tc := testContext(t)
	path := writeFile(t, tc.Cwd, "a.txt", "one\ntwo\n")
	resolved := readIntoState(t, tc, path)

	res, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"two","new_string":"three\nfour"}`, path))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if res.StateEffect == nil {
		t.Fatal("every successful edit must carry a FileStateEffect")
	}
	if res.StateEffect.Path != resolved {
		t.Errorf("effect path = %q, want %q", res.StateEffect.Path, resolved)
	}
	if res.StateEffect.Lines != 3 {
		t.Errorf("effect lines = %d, want 3", res.StateEffect.Lines)
	}
	cur, _ := os.ReadFile(path)
	if res.StateEffect.Hash != HashBytes(cur) {
		t.Error("effect hash must match the bytes on disk")
	}
	if !tc.Files.Fresh(resolved, HashBytes(cur)) {
		t.Error("the map must be fresh for the file the edit just wrote")
	}
}

func TestEditDiffRendererGolden(t *testing.T) {
	tc := testContext(t)
	content := "package main\n\nimport \"fmt\"\n\nfunc main() {\n\tfmt.Println(\"one\")\n\tfmt.Println(\"two\")\n\tfmt.Println(\"three\")\n\tfmt.Println(\"four\")\n\tfmt.Println(\"five\")\n\tfmt.Println(\"six\")\n\tfmt.Println(\"seven\")\n\tfmt.Println(\"eight\")\n\tfmt.Println(\"nine\")\n\tfmt.Println(\"ten\")\n}\n"
	path := writeFile(t, tc.Cwd, "main.go", content)
	readIntoState(t, tc, path)

	// Two changes far enough apart to force two hunks.
	res, err := callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"import \"fmt\"","new_string":"import (\n\t\"fmt\"\n\t\"os\"\n)"}`, path))
	if err != nil {
		t.Fatalf("first edit: %v", err)
	}
	_ = res
	res, err = callEdit(t, tc, fmt.Sprintf(`{"file_path":%q,"old_string":"\tfmt.Println(\"ten\")","new_string":"\tfmt.Fprintln(os.Stderr, \"ten\")"}`, path))
	if err != nil {
		t.Fatalf("second edit: %v", err)
	}

	display, ok := res.Display.(EditDisplay)
	if !ok {
		t.Fatalf("display is %T, want EditDisplay", res.Display)
	}
	root, err := filepath.EvalSymlinks(tc.Cwd)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.ReplaceAll(display.Diff, root, "<root>")
	eval.Golden(t, "edit_diff", got)
}

func TestUnifiedDiffTwoHunksGolden(t *testing.T) {
	var oldB, newB strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&oldB, "line %d\n", i)
		switch i {
		case 5:
			newB.WriteString("line five\n")
		case 25:
			// deleted
		default:
			fmt.Fprintf(&newB, "line %d\n", i)
			if i == 20 {
				newB.WriteString("line 20.5\n")
			}
		}
	}
	got := UnifiedDiff("/tmp/sample.txt", oldB.String(), newB.String())
	eval.Golden(t, "edit_diff_hunks", got)
}

func TestUnifiedDiffIdenticalTextsIsEmpty(t *testing.T) {
	if d := UnifiedDiff("/x", "same\n", "same\n"); d != "" {
		t.Errorf("identical texts must diff empty, got %q", d)
	}
}
