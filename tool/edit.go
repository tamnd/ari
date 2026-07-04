package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/tamnd/ari/lsp"
)

// editMaxResult holds a diff preview big enough for the model to
// verify the change landed where it meant (doc 04 section 3.3).
const editMaxResult = 100_000

type editArgs struct {
	FilePath   string `json:"file_path"`             // absolute, required
	OldString  string `json:"old_string"`            // exact text to find, required
	NewString  string `json:"new_string"`            // replacement text, required
	ReplaceAll bool   `json:"replace_all,omitempty"` // replace every occurrence
}

// EditDisplay is the typed data the UI renders for an edit: a real
// unified diff, syntax-highlighted by the TUI, plus any diagnostics the
// language server reported for the edited file, shown under the diff.
// Never sent to the model.
type EditDisplay struct {
	Path        string
	Diff        string
	Diagnostics []Diagnostic
}

// editTool replaces an exact, unique old string with a new string,
// behind the read-before-write gate, atomically, with a file-state
// refresh. No fuzzy matcher cascade, not one, not behind a flag (D8,
// doc 04 section 6).
type editTool struct {
	Base
	mu *sync.Mutex
}

// NewEdit builds the edit tool.
func NewEdit() Tool { return editTool{mu: &sync.Mutex{}} }

func (editTool) Name() string { return "edit" }

func (editTool) Schema() Schema {
	return Schema{
		Name:        "edit",
		Description: "Replace an exact, unique string in a file you have read; set replace_all for every occurrence.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Absolute path to the file to edit."},
				"old_string": {"type": "string", "description": "The exact text to find, including whitespace and indentation."},
				"new_string": {"type": "string", "description": "The replacement text."},
				"replace_all": {"type": "boolean", "description": "Replace every occurrence instead of requiring a unique match."}
			},
			"required": ["file_path", "old_string", "new_string"]
		}`),
	}
}

func (editTool) MaxResultSize() int { return editMaxResult }

// MatchPrefix matches rule content against the target path, so a rule
// like edit(/src/:*) or edit(**/*.pem) can single out paths.
func (editTool) MatchPrefix(raw json.RawMessage) PrefixMatcher {
	var a editArgs
	_ = json.Unmarshal(raw, &a)
	return contentMatcher{value: a.FilePath}
}

// ValidateInput runs the gate and the uniqueness check so the model
// gets its teaching rejection before permissions ever ask a human.
// Call re-verifies everything under the lock because the world can
// move between validation and there (doc 04 section 6.2).
func (editTool) ValidateInput(_ context.Context, raw json.RawMessage, tc *ToolContext) error {
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if a.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if !filepath.IsAbs(a.FilePath) {
		return fmt.Errorf("file_path must be an absolute path, got %q; resolve it against the working directory first", a.FilePath)
	}
	if a.OldString == "" {
		return fmt.Errorf("old_string is required")
	}
	if a.OldString == a.NewString {
		return fmt.Errorf("old_string and new_string are identical, there is nothing to change")
	}
	path, err := filepath.EvalSymlinks(a.FilePath)
	if err != nil {
		return fmt.Errorf("cannot edit %s: %v", a.FilePath, err)
	}
	cur, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("cannot edit %s: %v", path, err)
	}
	if _, err := checkGateAndMatch(a, path, string(cur), tc); err != nil {
		return err
	}
	return nil
}

func (e editTool) Call(ctx context.Context, raw json.RawMessage, tc *ToolContext, _ ProgressFunc) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var a editArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	path, err := filepath.EvalSymlinks(a.FilePath)
	if err != nil {
		return nil, fmt.Errorf("cannot edit %s: %v", a.FilePath, err)
	}

	// The critical section: re-read, re-check, replace, write, without
	// yielding. A concurrent edit in the same batch or an external
	// process could have moved the file since validation (doc 04
	// section 6.2).
	e.mu.Lock()
	defer e.mu.Unlock()

	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot edit %s: %v", path, err)
	}
	raw2, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot edit %s: %v", path, err)
	}
	cur := string(raw2)
	n, err := checkGateAndMatch(a, path, cur, tc)
	if err != nil {
		return nil, err
	}

	var next string
	if a.ReplaceAll {
		next = strings.ReplaceAll(cur, a.OldString, a.NewString)
	} else {
		next = strings.Replace(cur, a.OldString, a.NewString, 1)
	}
	if err := atomicWrite(path, []byte(next), info.Mode()); err != nil {
		return nil, err
	}
	after, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	hash := HashBytes([]byte(next))
	lines := len(splitLines([]byte(next)))
	if next == "" {
		lines = 0
	}
	// Refresh the map so the ant's own edits do not lock it out of the
	// file on the next edit (doc 04 section 6.2).
	tc.Files.Set(path, hash, after.ModTime(), lines)

	model := fmt.Sprintf("edited %s", path)
	if a.ReplaceAll && n > 1 {
		model = fmt.Sprintf("edited %s (%d occurrences)", path, n)
	}

	// The self-correcting loop: touch the file the server just saw change
	// and fold any errors into the result, so the next turn knows exactly
	// where the edit broke rather than guessing whether it compiled (doc
	// 04 section 6). An edit is a surgical change, so it reports its own
	// file, not the project.
	diags := diagnose(ctx, tc, path, lsp.TouchDocument)

	return &Result{
		Model:       appendDiagnostics(model, path, diags),
		Display:     EditDisplay{Path: path, Diff: UnifiedDiff(path, cur, next), Diagnostics: diags},
		StateEffect: &FileStateEffect{Path: path, Hash: hash, Mtime: after.ModTime(), Lines: lines},
	}, nil
}

// checkGateAndMatch is the shared heart of validation and the critical
// section: the read-before-write gate, then the uniqueness rule. Every
// rejection names the file, states what went wrong, and gives the
// concrete next action, because a rejection that just says "no match"
// wastes a turn (doc 04 section 6.1).
func checkGateAndMatch(a editArgs, path, cur string, tc *ToolContext) (int, error) {
	if tc == nil || tc.Files == nil {
		return 0, fmt.Errorf("you have not read %s in this session; read it before editing", path)
	}
	if _, seen := tc.Files.Entry(path); !seen {
		return 0, fmt.Errorf("you have not read %s in this session; read it before editing", path)
	}
	if !tc.Files.Fresh(path, HashBytes([]byte(cur))) {
		return 0, fmt.Errorf("%s changed since you read it; read it again before editing", path)
	}
	n := strings.Count(cur, a.OldString)
	if n == 0 {
		return 0, fmt.Errorf("old_string was not found in %s; the text must match exactly including whitespace and indentation", path)
	}
	if n > 1 && !a.ReplaceAll {
		return 0, fmt.Errorf("old_string matches %d places in %s; add surrounding lines to make it unique, or set replace_all to true to change all %d", n, path, n)
	}
	return n, nil
}
