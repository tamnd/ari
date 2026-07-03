// Package session persists transcripts. The store is an interface because
// opencode rewrote its storage from JSON-per-file to SQLite in production;
// the interface turns that rewrite into a backend swap (D9). The JSONL
// backend in session/jsonl ships first and the kernel never learns the
// difference.
package session

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"time"
)

// ID identifies one session within a project.
type ID string

// NewID returns a fresh session or entry id: 16 hex chars of entropy.
func NewID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("session: crypto/rand failed: " + err.Error())
	}
	return hex.EncodeToString(b[:])
}

// EntryType names what one transcript line is. Entries are heterogeneous
// and append-only; new types are additive (doc 01 section 7.5).
type EntryType string

const (
	EntryUser    EntryType = "user"    // a user message
	EntryAnt     EntryType = "ant"     // an assistant message with its parts
	EntryTool    EntryType = "tool"    // a tool result
	EntryMode    EntryType = "mode"    // a permission mode change
	EntryCompact EntryType = "compact" // a compaction boundary (doc 03)
	EntryTitle   EntryType = "title"   // an async title fill
	EntryMeta    EntryType = "meta"    // the first line of every file
)

// Entry is one JSONL line: a small envelope plus a typed body. The body is
// opaque here; the loop and the renderer own the shapes per type. The stamp
// fields (Session, CWD, Branch, Version) follow the body so a resume or a
// fork can re-stamp cleanly (doc 01 section 7.5).
type Entry struct {
	ID     string          `json:"id"`
	Parent string          `json:"parent,omitempty"` // previous entry id; the resume walk
	Type   EntryType       `json:"type"`
	Time   time.Time       `json:"time"`
	Body   json.RawMessage `json:"body,omitempty"`

	Session ID     `json:"session_id"`
	CWD     string `json:"cwd,omitempty"`
	Branch  string `json:"git_branch,omitempty"`
	Version string `json:"version,omitempty"`
}

// Meta is the body of the EntryMeta line that opens every session file.
type Meta struct {
	Title   string    `json:"title,omitempty"`
	Parent  ID        `json:"parent,omitempty"`   // forked from
	AtEntry string    `json:"at_entry,omitempty"` // fork point in the parent
	Created time.Time `json:"created"`
}

// SessionMeta is what a caller supplies at Create time.
type SessionMeta struct {
	Title   string
	AtEntry string // fork point; "" forks at the parent's leaf
}

// Summary is one row of a session list, newest first.
type Summary struct {
	ID      ID        `json:"id"`
	Title   string    `json:"title,omitempty"`
	Parent  ID        `json:"parent,omitempty"`
	Created time.Time `json:"created"`
	Entries int       `json:"entries"` // meta line excluded
}

// Transcript is a session rebuilt for resume: entries in conversation
// order after the parent walk, plus the meta that opened the file.
type Transcript struct {
	Meta    Meta
	Entries []Entry
}

// Store persists transcripts (D9, doc 01 section 7.4).
type Store interface {
	// Create makes a new empty session, optionally forked from parent.
	Create(ctx context.Context, parent ID, meta SessionMeta) (ID, error)

	// Append writes one entry to a session's log. It is the only mutation;
	// there is no update in place.
	Append(ctx context.Context, s ID, e Entry) error

	// AppendSidechain writes to an ant's sub-transcript under the session,
	// so a fan-out member never bloats the main resume (doc 01 section 7.3).
	AppendSidechain(ctx context.Context, s ID, ant string, e Entry) error

	// Load rebuilds the message chain for a session: walk parent pointers
	// backward from the leaf with a cycle guard, reverse, and re-attach
	// parallel tool results the single-parent walk missed.
	Load(ctx context.Context, s ID) (Transcript, error)

	// List returns session summaries for the project, newest first.
	List(ctx context.Context) ([]Summary, error)
}
