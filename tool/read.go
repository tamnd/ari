package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"unicode/utf8"
)

const (
	readDefaultLimit = 2000 // lines per page before the model must ask for more
	readSniffLen     = 8192 // bytes checked for the binary refusal
)

type readArgs struct {
	FilePath string `json:"file_path"`        // absolute, required
	Offset   int    `json:"offset,omitempty"` // 1-based first line, default 1
	Limit    int    `json:"limit,omitempty"`  // max lines, default 2000
}

// readTool turns a path into numbered lines the model can reason about
// and reference in an edit. Everything about it is shaped by two
// downstream needs: the model must be able to cite a line, and the read
// must arm the edit gate (doc 04 section 4).
type readTool struct {
	Base
}

// NewRead builds the read tool.
func NewRead() Tool { return readTool{} }

func (readTool) Name() string { return "read" }

func (readTool) Schema() Schema {
	return Schema{
		Name:        "read",
		Description: "Read a file as numbered lines; page big files with offset and limit.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"file_path": {"type": "string", "description": "Absolute path to the file to read."},
				"offset": {"type": "integer", "description": "1-based first line to return. Default 1."},
				"limit": {"type": "integer", "description": "Maximum lines to return. Default 2000."}
			},
			"required": ["file_path"]
		}`),
	}
}

func (readTool) IsReadOnly(json.RawMessage) bool        { return true }
func (readTool) IsConcurrencySafe(json.RawMessage) bool { return true }

// MaxResultSize is zero: read never spills. If it did, reading a large
// file would hand the model a spill path, the model would read that
// path, and read would chase its own tail forever. read bounds its
// output the honest way, by paginating (doc 04 section 3.3).
func (readTool) MaxResultSize() int { return 0 }

func (readTool) ValidateInput(_ context.Context, raw json.RawMessage, _ *ToolContext) error {
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if a.FilePath == "" {
		return fmt.Errorf("file_path is required")
	}
	if !filepath.IsAbs(a.FilePath) {
		return fmt.Errorf("file_path must be an absolute path, got %q; resolve it against the working directory first", a.FilePath)
	}
	if a.Offset < 0 || a.Limit < 0 {
		return fmt.Errorf("offset and limit must not be negative")
	}
	return nil
}

func (readTool) Call(ctx context.Context, raw json.RawMessage, tc *ToolContext, _ ProgressFunc) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var a readArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	// Symlinks resolve to the real path so the file-state map never
	// treats a link and its target as two independently-armed files
	// (doc 04 section 14.3).
	path, err := filepath.EvalSymlinks(a.FilePath)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", a.FilePath, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory, not a file; use find to list it", path)
	}

	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read %s: %v", path, err)
	}

	if len(content) == 0 {
		// A warning, not empty content, so the model does not mistake a
		// successful empty read for a failed call.
		return &Result{
			Model:       "<system-reminder>(file is empty)</system-reminder>",
			StateEffect: &FileStateEffect{Path: path, Hash: HashBytes(content), Mtime: info.ModTime(), Lines: 0},
		}, nil
	}

	if isBinary(content) {
		// The refusal and the M7 image-attachment path share this
		// detection; they just diverge on what they return.
		return nil, fmt.Errorf("%s is a binary file (%d bytes); read cannot display it", path, info.Size())
	}

	// The file is real text the model is about to reason over, so warm the
	// language server in the background now: the edit that usually follows a
	// read then finds a server that has already parsed the file (doc 04
	// section 6). This runs even for an unchanged re-read, since the server
	// may have been spawned since.
	warm(tc, path)

	hash := HashBytes(content)
	lines := splitLines(content)

	// An unchanged re-read returns a stub, not the content again, so the
	// transcript never carries the same file three times (doc 04
	// section 4.1).
	if tc != nil && tc.Files != nil && tc.Files.Fresh(path, hash) {
		return &Result{
			Model:       fmt.Sprintf("(file unchanged since last read, %d lines)", len(lines)),
			StateEffect: &FileStateEffect{Path: path, Hash: hash, Mtime: info.ModTime(), Lines: len(lines)},
		}, nil
	}

	offset := a.Offset
	if offset == 0 {
		offset = 1
	}
	limit := a.Limit
	if limit == 0 {
		limit = readDefaultLimit
	}
	if offset > len(lines) {
		return nil, fmt.Errorf("%s has %d lines, offset %d is past the end", path, len(lines), offset)
	}

	end := min(offset-1+limit, len(lines))
	page := lines[offset-1 : end]

	text := addLineNumbers(page, offset)
	if end < len(lines) {
		text += fmt.Sprintf("\n(showing lines %d-%d of %d; continue with offset=%d)", offset, end, len(lines), end+1)
	}

	return &Result{
		Model:       text,
		StateEffect: &FileStateEffect{Path: path, Hash: hash, Mtime: info.ModTime(), Lines: len(lines)},
	}, nil
}

// addLineNumbers renders cat -n format: the number counts from the real
// file line, so an offset read still shows true line numbers (doc 04
// section 4.1).
func addLineNumbers(lines []string, startLine int) string {
	var b strings.Builder
	for i, line := range lines {
		fmt.Fprintf(&b, "%6d\t%s\n", startLine+i, line)
	}
	return b.String()
}

// splitLines splits file content the way cat -n counts it: a trailing
// newline does not add a phantom last line.
func splitLines(content []byte) []string {
	s := string(content)
	s = strings.TrimSuffix(s, "\n")
	return strings.Split(s, "\n")
}

// isBinary sniffs the head of the content for bytes no text file
// carries: a NUL, or content that is not valid UTF-8.
func isBinary(content []byte) bool {
	head := content
	truncated := false
	if len(head) > readSniffLen {
		head = head[:readSniffLen]
		truncated = true
	}
	if slices.Contains(head, 0) {
		return true
	}
	if truncated {
		// Tolerate a rune cut at the sniff boundary.
		for i := 0; i < 3 && len(head) > 0 && !utf8.Valid(head); i++ {
			head = head[:len(head)-1]
		}
	}
	return !utf8.Valid(head)
}
