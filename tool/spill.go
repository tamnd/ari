package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// SpillStore persists oversized tool results and returns a reference
// the model can read back. Files live under the project nest and are
// swept on session end (doc 04 section 3.2).
type SpillStore interface {
	Put(content string) (SpillRef, error)
}

// SpillRef points at a spilled result on disk.
type SpillRef struct {
	Path string // absolute path the model can read() or find over
}

// DiskSpill writes spilled results into one directory, one numbered
// file per spill. The loop wires it to the nest's session state dir and
// calls Sweep when the session ends.
type DiskSpill struct {
	mu  sync.Mutex
	dir string
	n   int
}

// NewDiskSpill builds a store over dir, creating it on first Put.
func NewDiskSpill(dir string) *DiskSpill {
	return &DiskSpill{dir: dir}
}

// Put writes the content and returns where it lives.
func (s *DiskSpill) Put(content string) (SpillRef, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.MkdirAll(s.dir, 0o755); err != nil {
		return SpillRef{}, err
	}
	s.n++
	path := filepath.Join(s.dir, fmt.Sprintf("spill-%05d.txt", s.n))
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return SpillRef{}, err
	}
	return SpillRef{Path: path}, nil
}

// Sweep removes every spilled file, for session end.
func (s *DiskSpill) Sweep() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return os.RemoveAll(s.dir)
}

// ApplyResultBudget caps the model-facing text and spills the
// remainder. The loop runs it after Call returns, before the result
// becomes a tool_result. The preview is head-heavy on purpose: the
// first three quarters of the budget is the head, the last quarter the
// tail, because the top of a command's output is usually what matters
// (doc 04 section 3.2).
func ApplyResultBudget(r *Result, t Tool, tc *ToolContext) (string, error) {
	max := t.MaxResultSize()
	if max == 0 || len(r.Model) <= max {
		return r.Model, nil // no cap, or under it
	}
	ref, err := tc.Spill.Put(r.Model)
	if err != nil {
		return "", err
	}
	head := r.Model[:max*3/4]
	tail := r.Model[len(r.Model)-max/4:]
	return fmt.Sprintf(
		"%s\n\n[%d bytes total, truncated. Full output at %s. "+
			"Read it with read(%q) or grep it with find.]\n\n%s",
		head, len(r.Model), ref.Path, ref.Path, tail,
	), nil
}
