package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
)

// writeMaxResult is small on purpose: the model-facing result is a
// one-line confirmation (doc 04 section 3.3).
const writeMaxResult = 2000

type writeArgs struct {
	FilePath string `json:"file_path"` // absolute, required
	Content  string `json:"content"`   // full new file contents, required
}

// WriteDisplay is the typed data the UI renders for a write: the whole
// new content plus whether the file is new, so the permission dialog
// can show old versus new. Never sent to the model.
type WriteDisplay struct {
	Path    string
	Content string
	Created bool
}

// writeTool creates a new file or overwrites an existing one whole. It
// is the blunt instrument; edit is the scalpel (doc 04 section 5).
type writeTool struct {
	Base
	mu *sync.Mutex
}

// NewWrite builds the write tool. It shares edit's serialization
// through the loop's non-concurrency-safe partition; the lock here
// covers a same-batch race the scheduler cannot see.
func NewWrite() Tool { return writeTool{mu: &sync.Mutex{}} }

func (writeTool) Name() string { return "write" }

func (writeTool) Schema() Schema {
	return Schema{
		Name:        "write",
		Description: "Create a file or overwrite one you have read; parents are created as needed.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Absolute path to write."},
				"content": {"type": "string", "description": "The full new file contents."}
			},
			"required": ["file_path", "content"]
		}`),
	}
}

func (writeTool) MaxResultSize() int { return writeMaxResult }

// MatchPrefix matches rule content against the target path, so a rule
// like write(/src/:*) or write(**/*.pem) can single out paths.
func (writeTool) MatchPrefix(raw json.RawMessage) PrefixMatcher {
	var a writeArgs
	_ = json.Unmarshal(raw, &a)
	return contentMatcher{value: a.FilePath}
}

func (writeTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if a.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if !filepath.IsAbs(a.FilePath) {
		return fmt.Errorf("file_path must be an absolute path, got %q; resolve it against the working directory first", a.FilePath)
	}
	return nil
}

func (w writeTool) Call(ctx context.Context, raw json.RawMessage, tc *ToolContext, _ ProgressFunc) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var a writeArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	path, err := resolveForWrite(a.FilePath)
	if err != nil {
		return nil, err
	}

	// The gate check and the write are one critical section, so a
	// concurrent mutation between check and rename is caught, not
	// clobbered (doc 04 sections 5.1 and 6.2).
	w.mu.Lock()
	defer w.mu.Unlock()

	mode := fs.FileMode(0o644)
	created := true
	if onDisk, statErr := os.Stat(path); statErr == nil {
		if onDisk.IsDir() {
			return nil, fmt.Errorf("%s is a directory, not a file", path)
		}
		created = false
		mode = onDisk.Mode()
		cur, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil, fmt.Errorf("cannot overwrite %s: %v", path, readErr)
		}
		// The read-before-write gate: overwriting a file the ant has not
		// seen in its current state is a blind destructive act (D8). A
		// file this session created is already in the map, so
		// create-then-write needs no intervening read.
		if tc == nil || tc.Files == nil || !tc.Files.Fresh(path, HashBytes(cur)) {
			return nil, fmt.Errorf("cannot overwrite %s: it has not been read, or it changed since you read it; read it first", path)
		}
	} else if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("cannot create parents for %s: %v", path, err)
	}

	content := []byte(a.Content)
	if err := atomicWrite(path, content, mode); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	lines := len(splitLines(content))
	if len(content) == 0 {
		lines = 0
	}
	hash := HashBytes(content)
	if tc != nil && tc.Files != nil {
		// Refresh so the next edit or write sees fresh state and does not
		// trip the staleness check on the file it just wrote.
		tc.Files.Set(path, hash, info.ModTime(), lines)
	}
	return &Result{
		Model:       fmt.Sprintf("wrote %s (%d lines)", path, lines),
		Display:     WriteDisplay{Path: path, Content: a.Content, Created: created},
		StateEffect: &FileStateEffect{Path: path, Hash: hash, Mtime: info.ModTime(), Lines: lines},
	}, nil
}

// resolveForWrite resolves symlinks so the file-state key matches what
// read recorded. The file, and even its parents, may not exist yet, so
// the nearest existing ancestor is what resolves and the missing tail
// is rejoined; otherwise a create under a symlinked temp dir would key
// differently from the edit that follows it.
func resolveForWrite(path string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved, nil
	}
	dir, tail := filepath.Dir(path), filepath.Base(path)
	for {
		if resolved, err := filepath.EvalSymlinks(dir); err == nil {
			return filepath.Join(resolved, tail), nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return filepath.Clean(path), nil
		}
		tail = filepath.Join(filepath.Base(dir), tail)
		dir = parent
	}
}

// atomicWrite writes to a temp file in the same directory and renames
// it over the target, so a crash mid-write never leaves a half-written
// file. Mode is preserved for an existing target (doc 04 section 5.1).
func atomicWrite(path string, content []byte, mode fs.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".ari-write-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op after a successful rename

	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}
