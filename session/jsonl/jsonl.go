// Package jsonl is the first session.Store: one JSON object per line, one
// file per session, append-only, under the project's sessions/ directory in
// the global nest (doc 01 sections 7.3 and 7.5). Transcripts stay diffable,
// crash-safe, and inspectable with tail -f.
package jsonl

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/ari/session"
)

// Store keeps one <id>.jsonl per session plus <id>/ants/<ant>.jsonl
// sidechains. A single mutex serializes appends; session traffic is human
// paced and correctness beats throughput here.
type Store struct {
	dir string
	mu  sync.Mutex
}

// New opens a store rooted at dir, creating it if needed.
func New(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

func (st *Store) path(s session.ID) string {
	return filepath.Join(st.dir, string(s)+".jsonl")
}

func (st *Store) sidechainPath(s session.ID, ant string) string {
	return filepath.Join(st.dir, string(s), "ants", ant+".jsonl")
}

// Create makes a new empty session. With a parent it forks: the child file
// opens with a meta line pointing at the parent and the fork entry, then
// carries a copy of the parent's entries up to that point, so the child
// resumes alone and appending to it never touches the parent (D9).
func (st *Store) Create(ctx context.Context, parent session.ID, meta session.SessionMeta) (session.ID, error) {
	id := session.ID(session.NewID())
	m := session.Meta{Title: meta.Title, Parent: parent, AtEntry: meta.AtEntry, Created: time.Now().UTC()}

	var inherited []session.Entry
	if parent != "" {
		t, err := st.Load(ctx, parent)
		if err != nil {
			return "", fmt.Errorf("fork %s: %w", parent, err)
		}
		cut := len(t.Entries)
		if meta.AtEntry != "" {
			cut = 0
			for i, e := range t.Entries {
				if e.ID == meta.AtEntry {
					cut = i + 1
					break
				}
			}
			if cut == 0 {
				return "", fmt.Errorf("fork %s: entry %s not found", parent, meta.AtEntry)
			}
		}
		inherited = t.Entries[:cut]
	}

	body, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	lines := []session.Entry{{
		ID:      session.NewID(),
		Type:    session.EntryMeta,
		Time:    m.Created,
		Body:    body,
		Session: id,
	}}
	for _, e := range inherited {
		e.Session = id // re-stamp; body and ids stay the parent's
		lines = append(lines, e)
	}

	st.mu.Lock()
	defer st.mu.Unlock()
	if _, err := os.Stat(st.path(id)); err == nil {
		return "", fmt.Errorf("session %s already exists", id)
	}
	f, err := os.OpenFile(st.path(id), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	w := bufio.NewWriter(f)
	for _, e := range lines {
		if err := writeLine(w, e); err != nil {
			_ = f.Close()
			return "", err
		}
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return id, nil
}

// Append writes one entry to the session's log.
func (st *Store) Append(ctx context.Context, s session.ID, e session.Entry) error {
	e.Session = s
	return st.appendTo(st.path(s), e, false)
}

// AppendSidechain writes to an ant's sub-transcript under the session.
func (st *Store) AppendSidechain(ctx context.Context, s session.ID, ant string, e session.Entry) error {
	if strings.ContainsAny(ant, "/\\") {
		return fmt.Errorf("ant name %q may not contain a path separator", ant)
	}
	e.Session = s
	return st.appendTo(st.sidechainPath(s, ant), e, true)
}

func (st *Store) appendTo(path string, e session.Entry, mkdir bool) error {
	if e.ID == "" {
		return errors.New("entry has no id")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	if mkdir {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if mkdir && errors.Is(err, os.ErrNotExist) {
		f, err = os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	}
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	w := bufio.NewWriter(f)
	if err := writeLine(w, e); err != nil {
		return err
	}
	return w.Flush()
}

func writeLine(w *bufio.Writer, e session.Entry) error {
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.WriteByte('\n')
}

// Load rebuilds the session for resume: read every line into a map, walk
// parent pointers backward from the leaf with a cycle guard, reverse, then
// re-attach entries the single-parent walk missed (parallel tool results
// that share one parent), in file order (doc 01 section 7.5).
func (st *Store) Load(ctx context.Context, s session.ID) (session.Transcript, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := os.Open(st.path(s))
	if err != nil {
		return session.Transcript{}, fmt.Errorf("session %s: %w", s, err)
	}
	defer func() { _ = f.Close() }()

	var t session.Transcript
	var order []session.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e session.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return session.Transcript{}, fmt.Errorf("session %s: bad line: %w", s, err)
		}
		if e.Type == session.EntryMeta {
			if err := json.Unmarshal(e.Body, &t.Meta); err != nil {
				return session.Transcript{}, fmt.Errorf("session %s: bad meta: %w", s, err)
			}
			continue
		}
		order = append(order, e)
	}
	if err := sc.Err(); err != nil {
		return session.Transcript{}, err
	}
	if len(order) == 0 {
		return t, nil
	}

	byID := make(map[string]session.Entry, len(order))
	for _, e := range order {
		byID[e.ID] = e
	}

	// Walk backward from the leaf, guarding against a cycle.
	var chain []session.Entry
	seen := make(map[string]bool, len(order))
	for cur := order[len(order)-1]; ; {
		if seen[cur.ID] {
			return session.Transcript{}, fmt.Errorf("session %s: parent cycle at %s", s, cur.ID)
		}
		seen[cur.ID] = true
		chain = append(chain, cur)
		if cur.Parent == "" {
			break
		}
		p, ok := byID[cur.Parent]
		if !ok {
			break // a trimmed or foreign parent ends the walk, not the resume
		}
		cur = p
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}

	// Re-attach orphaned parallel results: entries off the chain whose
	// parent is on it. The file is append-only so file order is
	// chronological; emitting the kept set in file order puts every
	// parallel sibling right where it happened.
	keep := make(map[string]bool, len(chain))
	for _, e := range chain {
		keep[e.ID] = true
	}
	for _, e := range order {
		if !keep[e.ID] && keep[e.Parent] {
			keep[e.ID] = true
		}
	}
	final := make([]session.Entry, 0, len(chain))
	for _, e := range order {
		if keep[e.ID] {
			final = append(final, e)
		}
	}

	t.Entries = final
	return t, nil
}

// LoadSidechain reads one ant's sub-transcript in file order. A sidechain is
// append-only and linear, so there is no parent walk: the meta line opens the
// file and every other line is one transcript entry in the order it happened. A
// worker that never opened its file is not an error, it is an empty transcript,
// which is what the colony drill-in shows for an ant that has not spoken yet.
func (st *Store) LoadSidechain(ctx context.Context, s session.ID, ant string) (session.Transcript, error) {
	if strings.ContainsAny(ant, "/\\") {
		return session.Transcript{}, fmt.Errorf("ant name %q may not contain a path separator", ant)
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	f, err := os.Open(st.sidechainPath(s, ant))
	if err != nil {
		if os.IsNotExist(err) {
			return session.Transcript{}, nil
		}
		return session.Transcript{}, fmt.Errorf("sidechain %s/%s: %w", s, ant, err)
	}
	defer func() { _ = f.Close() }()

	var t session.Transcript
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e session.Entry
		if err := json.Unmarshal(line, &e); err != nil {
			return session.Transcript{}, fmt.Errorf("sidechain %s/%s: bad line: %w", s, ant, err)
		}
		if e.Type == session.EntryMeta {
			if err := json.Unmarshal(e.Body, &t.Meta); err != nil {
				return session.Transcript{}, fmt.Errorf("sidechain %s/%s: bad meta: %w", s, ant, err)
			}
			continue
		}
		t.Entries = append(t.Entries, e)
	}
	if err := sc.Err(); err != nil {
		return session.Transcript{}, err
	}
	return t, nil
}

// List returns session summaries, newest first.
func (st *Store) List(ctx context.Context) ([]session.Summary, error) {
	st.mu.Lock()
	defer st.mu.Unlock()
	entries, err := os.ReadDir(st.dir)
	if err != nil {
		return nil, err
	}
	var out []session.Summary
	for _, de := range entries {
		name := de.Name()
		if de.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := session.ID(strings.TrimSuffix(name, ".jsonl"))
		sum, err := st.summarize(id)
		if err != nil {
			return nil, err
		}
		out = append(out, sum)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
	return out, nil
}

func (st *Store) summarize(id session.ID) (session.Summary, error) {
	f, err := os.Open(st.path(id))
	if err != nil {
		return session.Summary{}, err
	}
	defer func() { _ = f.Close() }()
	sum := session.Summary{ID: id}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e session.Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			return session.Summary{}, fmt.Errorf("session %s: bad line: %w", id, err)
		}
		switch e.Type {
		case session.EntryMeta:
			var m session.Meta
			if err := json.Unmarshal(e.Body, &m); err != nil {
				return session.Summary{}, err
			}
			sum.Title, sum.Parent, sum.Created = m.Title, m.Parent, m.Created
		case session.EntryTitle:
			var m session.Meta
			if err := json.Unmarshal(e.Body, &m); err == nil && m.Title != "" {
				sum.Title = m.Title
			}
			sum.Entries++
		default:
			sum.Entries++
		}
	}
	return sum, sc.Err()
}
