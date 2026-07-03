package tool

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// FileState is the per-session read-before-write map. read populates
// it, edit and write consult and refresh it. It is the session-level
// invariant that no mutation touches an unseen file (D8, doc 04
// section 10). Safe for the loop's concurrent access.
type FileState struct {
	mu    sync.Mutex
	files map[string]FileEntry // absolute path to entry
}

// FileEntry is what the map remembers about one file.
type FileEntry struct {
	Hash  string    // content hash at last read or write
	Mtime time.Time // filesystem mtime at last read or write
	Lines int       // line count, for the unchanged-read stub
}

// NewFileState builds an empty map.
func NewFileState() *FileState {
	return &FileState{files: make(map[string]FileEntry)}
}

// Entry returns the recorded entry for a path.
func (f *FileState) Entry(path string) (FileEntry, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	e, ok := f.files[path]
	return e, ok
}

// Hash returns the recorded content hash for a path, empty if never
// read or written.
func (f *FileState) Hash(path string) string {
	e, _ := f.Entry(path)
	return e.Hash
}

// Set records a read or write: the path, the content hash, the mtime,
// and the line count.
func (f *FileState) Set(path, hash string, mtime time.Time, lines int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[path] = FileEntry{Hash: hash, Mtime: mtime, Lines: lines}
}

// Apply folds a tool's FileStateEffect into the map. The loop calls it
// after every successful call that returned one.
func (f *FileState) Apply(e *FileStateEffect) {
	if e == nil {
		return
	}
	f.Set(e.Path, e.Hash, e.Mtime, e.Lines)
}

// Fresh reports whether the recorded hash still matches the content on
// disk. A missing entry means never-read; a mismatch means
// changed-since-read. The hash comparison is the whole check, no mtime,
// because cloud sync and antivirus bump mtimes without changing content
// (doc 04 section 10.2).
func (f *FileState) Fresh(path, onDiskHash string) bool {
	e, ok := f.Entry(path)
	return ok && e.Hash == onDiskHash
}

// HashBytes is the content hash the map keys freshness on, shared by
// every tool that arms or checks the gate.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
