package ant

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tamnd/ari/colony"
	memsqlite "github.com/tamnd/ari/memory/sqlite"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/session/jsonl"
)

// surveyorCard is a read-only worker card: it can read and find, nothing more,
// so the allowlist test can prove a mutating tool is absent by construction.
func surveyorCard() colony.Card {
	return colony.Card{
		ID:    "surveyor",
		Tier:  colony.TierMid,
		Tools: []string{"read", "find"},
		State: colony.StateSpec{Namespace: "surveyor/main"},
	}
}

// detachHarness stands up the pieces a colony worker needs: a session store, a
// blackboard over a migrated colony.db, a parent session, and one claimed goal
// carrying a finding brief that points at a file to read.
type detachHarness struct {
	root    string
	store   session.Store
	board   colony.Blackboard
	parent  session.ID
	claimID string
	task    string
}

func newDetachHarness(t *testing.T, deliver colony.Kind, ctxPath string) detachHarness {
	t.Helper()
	ctx := context.Background()
	root := t.TempDir()

	store, err := jsonl.New(filepath.Join(root, ".ari", "sessions"))
	if err != nil {
		t.Fatalf("session store: %v", err)
	}
	parent, err := store.Create(ctx, "", session.SessionMeta{Title: "foreground"})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}

	db, err := memsqlite.Open(filepath.Join(root, "colony.db"))
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Start(ctx); err != nil {
		t.Fatalf("start db: %v", err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	board := colony.NewBlackboard(db, nil)

	brief := colony.TaskBrief{
		Header:      colony.Header{ID: "b1", Kind: colony.KindTaskBrief, From: "queen", TaskID: "t1", SessionID: string(parent)},
		Goal:        "survey the target and report what the marker says",
		Deliverable: deliver,
		Context:     []colony.ContextRef{{Path: ctxPath}},
	}
	if _, err := board.Post(ctx, colony.Entry{SessionID: string(parent), TaskID: "t1", Payload: brief}); err != nil {
		t.Fatalf("post goal: %v", err)
	}
	claimed, ok, err := board.Claim(ctx, "surveyor", colony.ClaimFilter{SessionID: string(parent)})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	return detachHarness{root: root, store: store, board: board, parent: parent, claimID: claimed.ID, task: "t1"}
}

// readThenReport scripts a worker that reads the briefed file, then finishes
// with a one-line answer that becomes the finding's summary.
func readThenReport(t *testing.T, path, answer string) provider.Provider {
	t.Helper()
	return scripted.New(
		scripted.Response{
			Calls: []provider.ToolCall{{ID: "c1", Name: "read", Input: `{"path":` + mustQuote(path) + `}`}},
			Usage: provider.Usage{Input: 20, Output: 5},
			Stop:  "tool_use",
		},
		scripted.Response{
			Text:  answer,
			Usage: provider.Usage{Input: 30, Output: 8},
			Stop:  "end_turn",
		},
	)
}

func mustQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// sidechainLines reads every line of a worker's sidechain, failing if any line
// is not well-formed JSON, and returns the decoded entries.
func sidechainLines(t *testing.T, path string) []session.Entry {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open sidechain %s: %v", path, err)
	}
	defer func() { _ = f.Close() }()
	var out []session.Entry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var e session.Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			t.Fatalf("sidechain line is not valid JSON: %v\nline: %s", err, sc.Bytes())
		}
		out = append(out, e)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan sidechain: %v", err)
	}
	return out
}

// TestDetachmentPostsFindingToSidechain is the slice's core DoD: a worker runs
// off its brief, writes its transcript to its own sidechain under the parent
// session, and its final act is a typed Finding on the blackboard, with nothing
// written to the foreground session.
func TestDetachmentPostsFindingToSidechain(t *testing.T) {
	h := newDetachHarness(t, colony.KindFinding, "notes.txt")
	target := filepath.Join(h.root, "notes.txt")
	if err := os.WriteFile(target, []byte("the marker reads OK\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d, err := NewDetachment(DetachConfig{
		Card:     surveyorCard(),
		Brief:    briefFor(t, h),
		ClaimID:  h.claimID,
		Parent:   h.parent,
		Board:    h.board,
		Store:    h.store,
		Provider: readThenReport(t, target, "the marker reads OK"),
		Model:    "fable-test",
		Cwd:      h.root,
	})
	if err != nil {
		t.Fatalf("new detachment: %v", err)
	}

	if err := d.Run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	// The worker's final act is a Finding on the board, citing the briefed file.
	fs, err := h.board.Findings(context.Background(), h.task)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(fs) != 1 {
		t.Fatalf("board has %d findings, want 1", len(fs))
	}
	if fs[0].Summary != "the marker reads OK" {
		t.Errorf("finding summary = %q, want the worker's answer", fs[0].Summary)
	}
	if fs[0].From != "surveyor" {
		t.Errorf("finding from = %q, want surveyor", fs[0].From)
	}
	if len(fs[0].Evidence) != 1 || fs[0].Evidence[0].Path != "notes.txt" {
		t.Errorf("finding evidence = %+v, want a citation of notes.txt", fs[0].Evidence)
	}

	// The transcript is in the worker's own sidechain, opening with a meta line
	// that points back at the spawning session.
	side := filepath.Join(h.root, ".ari", "sessions", string(h.parent), "ants", "surveyor.t1.jsonl")
	lines := sidechainLines(t, side)
	if len(lines) == 0 || lines[0].Type != session.EntryMeta {
		t.Fatal("sidechain does not open with a meta line")
	}
	var meta session.Meta
	if err := json.Unmarshal(lines[0].Body, &meta); err != nil {
		t.Fatalf("sidechain meta: %v", err)
	}
	if meta.Parent != h.parent {
		t.Errorf("sidechain parent = %q, want the spawning session %q", meta.Parent, h.parent)
	}

	// The foreground session paid nothing for the worker: its own file carries
	// no worker entries (D9 keeps the main resume small).
	fg, err := h.store.Load(context.Background(), h.parent)
	if err != nil {
		t.Fatalf("load foreground: %v", err)
	}
	if len(fg.Entries) != 0 {
		t.Errorf("foreground session has %d entries, want 0; worker chatter leaked into the main resume", len(fg.Entries))
	}
}

// TestDetachmentAllowlistExcludesUnlistedTool proves the restriction is
// structural: a read-only card's worker never registers write or edit, so the
// model cannot call them, enforced before any tool could run.
func TestDetachmentAllowlistExcludesUnlistedTool(t *testing.T) {
	h := newDetachHarness(t, colony.KindFinding, "notes.txt")
	d, err := NewDetachment(DetachConfig{
		Card:     surveyorCard(),
		Brief:    briefFor(t, h),
		ClaimID:  h.claimID,
		Parent:   h.parent,
		Board:    h.board,
		Store:    h.store,
		Provider: scripted.New(scripted.Response{Text: "ok", Usage: provider.Usage{Input: 1, Output: 1}, Stop: "end_turn"}),
		Model:    "fable-test",
		Cwd:      h.root,
	})
	if err != nil {
		t.Fatalf("new detachment: %v", err)
	}
	for _, banned := range []string{"write", "edit", "sh"} {
		if _, ok := d.loop.Tools.Resolve(banned); ok {
			t.Errorf("read-only worker can resolve %q; the allowlist did not exclude it", banned)
		}
	}
	if _, ok := d.loop.Tools.Resolve("read"); !ok {
		t.Error("read-only worker cannot resolve read, which its card allows")
	}
}

// TestDetachmentCancelLeavesNoResult is the crash DoD from the worker's side: a
// cancelled worker posts no finding and leaves a well-formed sidechain, never a
// corrupt file or a half-written result.
func TestDetachmentCancelLeavesNoResult(t *testing.T) {
	h := newDetachHarness(t, colony.KindFinding, "notes.txt")
	d, err := NewDetachment(DetachConfig{
		Card:     surveyorCard(),
		Brief:    briefFor(t, h),
		ClaimID:  h.claimID,
		Parent:   h.parent,
		Board:    h.board,
		Store:    h.store,
		Provider: scripted.New(scripted.Response{Text: "ok", Usage: provider.Usage{Input: 1, Output: 1}, Stop: "end_turn"}),
		Model:    "fable-test",
		Cwd:      h.root,
	})
	if err != nil {
		t.Fatalf("new detachment: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := d.Run(ctx); err != nil {
		t.Fatalf("run after cancel should not error out of the worker: %v", err)
	}

	fs, err := h.board.Findings(context.Background(), h.task)
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(fs) != 0 {
		t.Errorf("cancelled worker posted %d findings, want 0", len(fs))
	}
	// The sidechain, if it was opened, is still parseable line by line.
	side := filepath.Join(h.root, ".ari", "sessions", string(h.parent), "ants", "surveyor.t1.jsonl")
	if _, err := os.Stat(side); err == nil {
		sidechainLines(t, side)
	}
}

// briefFor rebuilds the brief the harness posted so the detachment carries the
// same goal, deliverable, and context the board holds.
func briefFor(t *testing.T, h detachHarness) colony.TaskBrief {
	t.Helper()
	return colony.TaskBrief{
		Header:      colony.Header{ID: "b1", Kind: colony.KindTaskBrief, From: "queen", TaskID: h.task, SessionID: string(h.parent)},
		Goal:        "survey the target and report what the marker says",
		Deliverable: colony.KindFinding,
		Context:     []colony.ContextRef{{Path: "notes.txt"}},
	}
}

// runGit runs a git command in dir with a fixed identity and fails the test on
// a nonzero exit, so a repo can be staged without a global git config.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	full := append([]string{"-C", dir, "-c", "user.email=ant@test", "-c", "user.name=ant"}, args...)
	out, err := exec.Command("git", full...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return string(out)
}

// patchWorktree makes a temp git repo with one committed base file, the shape a
// patch worker runs against.
func patchWorktree(t *testing.T) string {
	t.Helper()
	repo := t.TempDir()
	runGit(t, repo, "init", "-q")
	if err := os.WriteFile(filepath.Join(repo, "base.txt"), []byte("base\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, repo, "add", "-A")
	runGit(t, repo, "commit", "-q", "-m", "base")
	return repo
}

// patchDetachment builds a patch-deliverable worker over the given worktree.
func patchDetachment(t *testing.T, repo string) *Detachment {
	t.Helper()
	d, err := NewDetachment(DetachConfig{
		Card: colony.WorkerCard(),
		Brief: colony.TaskBrief{
			Header:      colony.Header{ID: "b1", Kind: colony.KindTaskBrief, From: "queen", TaskID: "t1"},
			Goal:        "add a file",
			Deliverable: colony.KindPatch,
		},
		Provider: scripted.New(),
		Model:    "fable-test",
		Cwd:      repo,
		BaseRef:  "HEAD",
	})
	if err != nil {
		t.Fatalf("new detachment: %v", err)
	}
	return d
}

// TestDetachmentHarvestsPatch closes the slice-12 gap: a worker that left
// changes in its worktree harvests as a colony.Patch whose diff is the full
// change against the base ref, new files included.
func TestDetachmentHarvestsPatch(t *testing.T) {
	repo := patchWorktree(t)
	// The worker's edit: a new file the base commit did not have.
	if err := os.WriteFile(filepath.Join(repo, "new.txt"), []byte("hello world\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	d := patchDetachment(t, repo)
	h, err := d.harvest()
	if err != nil {
		t.Fatalf("harvest: %v", err)
	}
	patch, ok := h.(colony.Patch)
	if !ok {
		t.Fatalf("harvest returned %T, want colony.Patch", h)
	}
	if patch.BaseRef != "HEAD" {
		t.Errorf("base ref = %q, want HEAD", patch.BaseRef)
	}
	if patch.Worktree != repo {
		t.Errorf("worktree = %q, want %q", patch.Worktree, repo)
	}
	if !strings.Contains(patch.Diff, "new.txt") || !strings.Contains(patch.Diff, "hello world") {
		t.Errorf("diff does not carry the new file:\n%s", patch.Diff)
	}
	if patch.Kind != colony.KindPatch || patch.TaskID != "t1" {
		t.Errorf("patch header = %+v, want kind patch task t1", patch.Header)
	}
}

// TestDetachmentPatchNoChangesFails proves an empty diff is a failure, not a
// no-op patch: a worker briefed to change something that touched nothing did
// not do its job, so the claim should fail rather than reconcile nothing.
func TestDetachmentPatchNoChangesFails(t *testing.T) {
	d := patchDetachment(t, patchWorktree(t))
	if _, err := d.harvest(); err == nil {
		t.Fatal("harvest of an unchanged worktree should fail")
	}
}

// TestDetachmentPatchNeedsWorktree proves a patch brief with no cwd fails
// cleanly rather than diffing the process working directory.
func TestDetachmentPatchNeedsWorktree(t *testing.T) {
	d, err := NewDetachment(DetachConfig{
		Card: colony.WorkerCard(),
		Brief: colony.TaskBrief{
			Header:      colony.Header{ID: "b1", Kind: colony.KindTaskBrief, From: "queen", TaskID: "t1"},
			Goal:        "add a file",
			Deliverable: colony.KindPatch,
		},
		Provider: scripted.New(),
		Model:    "fable-test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.harvest(); err == nil {
		t.Fatal("patch harvest with no cwd should fail")
	}
}
