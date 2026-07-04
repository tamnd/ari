package jsonl

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/tamnd/ari/kernel/eval"
	"github.com/tamnd/ari/session"
)

func TestMain(m *testing.M) { eval.Main(m) }

func entry(id, parent, text string) session.Entry {
	body, _ := json.Marshal(map[string]string{"text": text})
	return session.Entry{
		ID:     id,
		Parent: parent,
		Type:   session.EntryUser,
		Time:   time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
		Body:   body,
	}
}

func TestResumeIsByteIdentical(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	id, err := st.Create(ctx, "", session.SessionMeta{Title: "demo"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		parent := ""
		if i > 0 {
			parent = fmt.Sprintf("e%d", i-1)
		}
		if err := st.Append(ctx, id, entry(fmt.Sprintf("e%d", i), parent, fmt.Sprintf("turn %d", i))); err != nil {
			t.Fatal(err)
		}
	}
	first, err := st.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}

	// A new store over the same directory is the closed-and-reopened
	// colony; the resumed transcript must match byte for byte.
	st2, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st2.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first)
	b, _ := json.Marshal(second)
	if string(a) != string(b) {
		t.Errorf("resume drifted:\nfirst:  %s\nsecond: %s", a, b)
	}
	if len(second.Entries) != 5 || second.Meta.Title != "demo" {
		t.Errorf("resume lost data: %d entries, title %q", len(second.Entries), second.Meta.Title)
	}
	for i, e := range second.Entries {
		if e.ID != fmt.Sprintf("e%d", i) {
			t.Errorf("entry %d out of order: %s", i, e.ID)
		}
	}
}

func TestForkCarriesPrefixAndNeverMutatesParent(t *testing.T) {
	ctx := context.Background()
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent, err := st.Create(ctx, "", session.SessionMeta{Title: "parent"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		p := ""
		if i > 0 {
			p = fmt.Sprintf("e%d", i-1)
		}
		if err := st.Append(ctx, parent, entry(fmt.Sprintf("e%d", i), p, "x")); err != nil {
			t.Fatal(err)
		}
	}
	parentFile, err := os.ReadFile(st.path(parent))
	if err != nil {
		t.Fatal(err)
	}

	// Fork at turn K=3 (entry e2 is the third).
	child, err := st.Create(ctx, parent, session.SessionMeta{Title: "child", AtEntry: "e2"})
	if err != nil {
		t.Fatal(err)
	}
	ct, err := st.Load(ctx, child)
	if err != nil {
		t.Fatal(err)
	}
	if ct.Meta.Parent != parent || ct.Meta.AtEntry != "e2" {
		t.Errorf("child meta lost the fork point: %+v", ct.Meta)
	}
	if len(ct.Entries) != 3 {
		t.Fatalf("child carries %d entries, want the first 3", len(ct.Entries))
	}
	for i, e := range ct.Entries {
		if e.ID != fmt.Sprintf("e%d", i) {
			t.Errorf("child entry %d = %s", i, e.ID)
		}
		if e.Session != child {
			t.Errorf("child entry %d still stamped %s", i, e.Session)
		}
	}

	// Appending to the child must never touch the parent file.
	if err := st.Append(ctx, child, entry("c0", "e2", "diverge")); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(st.path(parent))
	if err != nil {
		t.Fatal(err)
	}
	if string(parentFile) != string(after) {
		t.Error("appending to the fork mutated the parent file")
	}
	pt, err := st.Load(ctx, parent)
	if err != nil {
		t.Fatal(err)
	}
	if len(pt.Entries) != 6 {
		t.Errorf("parent transcript changed: %d entries", len(pt.Entries))
	}
}

func TestForkAtLeafByDefault(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	parent, _ := st.Create(ctx, "", session.SessionMeta{})
	if err := st.Append(ctx, parent, entry("e0", "", "x")); err != nil {
		t.Fatal(err)
	}
	child, err := st.Create(ctx, parent, session.SessionMeta{})
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := st.Load(ctx, child)
	if len(ct.Entries) != 1 {
		t.Errorf("leaf fork carries %d entries, want 1", len(ct.Entries))
	}
}

func TestLoadReattachesOrphanedParallelResults(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	id, _ := st.Create(ctx, "", session.SessionMeta{})
	// e0 <- t1, t2, t3 (parallel tool results sharing one parent), then the
	// leaf continues from t3. The single-parent walk from the leaf finds
	// e0, t3, leaf; t1 and t2 are the orphans to re-attach in file order.
	for _, e := range []session.Entry{
		entry("e0", "", "call three tools"),
		entry("t1", "e0", "result one"),
		entry("t2", "e0", "result two"),
		entry("t3", "e0", "result three"),
		entry("leaf", "t3", "continue"),
	} {
		if err := st.Append(ctx, id, e); err != nil {
			t.Fatal(err)
		}
	}
	tr, err := st.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range tr.Entries {
		got = append(got, e.ID)
	}
	want := []string{"e0", "t1", "t2", "t3", "leaf"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("order = %v, want %v", got, want)
	}
}

func TestLoadGuardsAgainstParentCycle(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	id, _ := st.Create(ctx, "", session.SessionMeta{})
	if err := st.Append(ctx, id, entry("a", "b", "x")); err != nil {
		t.Fatal(err)
	}
	if err := st.Append(ctx, id, entry("b", "a", "y")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Load(ctx, id); err == nil {
		t.Fatal("a parent cycle must be an error, not a hang")
	}
}

func TestSidechainStaysOutOfMainResume(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	id, _ := st.Create(ctx, "", session.SessionMeta{})
	if err := st.Append(ctx, id, entry("e0", "", "main")); err != nil {
		t.Fatal(err)
	}
	if err := st.AppendSidechain(ctx, id, "scout-1", entry("s0", "", "sub work")); err != nil {
		t.Fatal(err)
	}
	tr, err := st.Load(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr.Entries) != 1 {
		t.Errorf("sidechain leaked into the main resume: %d entries", len(tr.Entries))
	}
	if _, err := os.Stat(st.sidechainPath(id, "scout-1")); err != nil {
		t.Errorf("sidechain file missing: %v", err)
	}
	if err := st.AppendSidechain(ctx, id, "../evil", entry("s1", "", "escape")); err == nil {
		t.Error("a path separator in an ant name must be rejected")
	}
}

func TestLoadSidechainReadsInFileOrder(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	id, _ := st.Create(ctx, "", session.SessionMeta{})

	// A worker that never opened its file drills in to nothing, not an error.
	tr, err := st.LoadSidechain(ctx, id, "forager-0")
	if err != nil {
		t.Fatalf("empty sidechain must not error: %v", err)
	}
	if len(tr.Entries) != 0 {
		t.Errorf("an unopened sidechain has no entries, got %d", len(tr.Entries))
	}

	// A meta line opens the file; the rest are transcript lines in order.
	meta, _ := json.Marshal(session.Meta{Title: "worker s1 on task t1", Parent: id})
	if err := st.AppendSidechain(ctx, id, "forager-0", session.Entry{ID: "m0", Type: session.EntryMeta, Body: meta}); err != nil {
		t.Fatal(err)
	}
	for _, txt := range []string{"first", "second", "third"} {
		if err := st.AppendSidechain(ctx, id, "forager-0", entry(txt, "", txt)); err != nil {
			t.Fatal(err)
		}
	}

	tr, err = st.LoadSidechain(ctx, id, "forager-0")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Meta.Title != "worker s1 on task t1" {
		t.Errorf("meta not parsed: title %q", tr.Meta.Title)
	}
	if len(tr.Entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(tr.Entries))
	}
	for i, want := range []string{"first", "second", "third"} {
		if tr.Entries[i].ID != want {
			t.Errorf("entry %d out of file order: got %q want %q", i, tr.Entries[i].ID, want)
		}
	}

	// One worker's file never bleeds into another's.
	if other, _ := st.LoadSidechain(ctx, id, "forager-1"); len(other.Entries) != 0 {
		t.Errorf("forager-1 saw forager-0's entries: %d", len(other.Entries))
	}
	if _, err := st.LoadSidechain(ctx, id, "../evil"); err == nil {
		t.Error("a path separator in an ant name must be rejected")
	}
}

func TestListNewestFirst(t *testing.T) {
	ctx := context.Background()
	st, _ := New(t.TempDir())
	// Create ordering is by meta Created time; force distinct stamps by
	// rewriting metas is overkill, so just verify the sort holds for the
	// stamps Create assigned.
	var ids []session.ID
	for i := range 3 {
		id, err := st.Create(ctx, "", session.SessionMeta{Title: fmt.Sprintf("s%d", i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
		time.Sleep(2 * time.Millisecond)
	}
	if err := st.Append(ctx, ids[0], entry("e0", "", "x")); err != nil {
		t.Fatal(err)
	}
	list, err := st.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("listed %d sessions", len(list))
	}
	for i := 1; i < len(list); i++ {
		if list[i].Created.After(list[i-1].Created) {
			t.Error("list is not newest first")
		}
	}
	for _, s := range list {
		if s.ID == ids[0] && s.Entries != 1 {
			t.Errorf("entry count = %d, want 1 (meta excluded)", s.Entries)
		}
	}
}
