package colony

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	memsqlite "github.com/tamnd/ari/memory/sqlite"
)

// openBoard brings up a migrated colony.db and a blackboard over it with a
// pinned clock, both torn down with the test.
func openBoard(t *testing.T) (Blackboard, *memsqlite.Store) {
	t.Helper()
	ctx := context.Background()
	db, err := memsqlite.Open(filepath.Join(t.TempDir(), "colony.db"))
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
	base := time.Unix(1_700_000_000, 0)
	return NewBlackboard(db, func() time.Time { return base }), db
}

// brief builds a valid TaskBrief for a task with the given labels.
func brief(id, task string, labels Labels) TaskBrief {
	return TaskBrief{
		Header:      Header{ID: id, Kind: KindTaskBrief, From: "queen", TaskID: task, SessionID: "s1", Labels: labels},
		Goal:        "do the thing",
		Deliverable: KindFinding,
	}
}

// finding builds a valid Finding carrying one citation.
func finding(id, task string) Finding {
	return Finding{
		Header:   Header{ID: id, Kind: KindFinding, From: "surveyor-1", TaskID: task, SessionID: "s1"},
		Summary:  "the thing was found here",
		Evidence: []Citation{{Path: "colony/board.go", Lines: [2]int{1, 10}}},
	}
}

// question builds a valid Question.
func question(id, task string) Question {
	return Question{
		Header:   Header{ID: id, Kind: KindQuestion, From: "worker-1", TaskID: task, SessionID: "s1"},
		Ask:      "which base ref should I cut from?",
		Blocking: true,
	}
}

// labelsFor reads the trust labels a board row stored, straight from the db.
func labelsFor(t *testing.T, db *memsqlite.Store, rowID string) string {
	t.Helper()
	var labels string
	err := db.Read(context.Background(), func(d *sql.DB) error {
		return d.QueryRow(`SELECT labels FROM blackboard WHERE id = ?`, rowID).Scan(&labels)
	})
	if err != nil {
		t.Fatalf("read labels: %v", err)
	}
	return labels
}

// stateFor reads a board row's lifecycle state, straight from the db.
func stateFor(t *testing.T, db *memsqlite.Store, rowID string) string {
	t.Helper()
	var state string
	err := db.Read(context.Background(), func(d *sql.DB) error {
		return d.QueryRow(`SELECT state FROM blackboard WHERE id = ?`, rowID).Scan(&state)
	})
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	return state
}

// TestPostSubtaskSetsOriginAndParent is the first DoD line: posting a subtask
// creates a row with Origin = blackboard and Parent set, and a root task
// posted without a parent is foreground.
func TestPostSubtaskSetsOriginAndParent(t *testing.T) {
	ctx := context.Background()
	bb, db := openBoard(t)

	rootID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "parent", Payload: brief("b-root", "parent", nil)})
	if err != nil {
		t.Fatalf("post root: %v", err)
	}
	childID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "child", Parent: "parent", Payload: brief("b-child", "child", nil)})
	if err != nil {
		t.Fatalf("post child: %v", err)
	}

	var rootOrigin, rootParent, childOrigin, childParent string
	err = db.Read(ctx, func(d *sql.DB) error {
		if e := d.QueryRow(`SELECT origin, parent FROM blackboard WHERE id = ?`, rootID).Scan(&rootOrigin, &rootParent); e != nil {
			return e
		}
		return d.QueryRow(`SELECT origin, parent FROM blackboard WHERE id = ?`, childID).Scan(&childOrigin, &childParent)
	})
	if err != nil {
		t.Fatalf("read rows: %v", err)
	}
	if rootOrigin != string(OriginForeground) || rootParent != "" {
		t.Errorf("root row origin/parent = %s/%q, want foreground/empty", rootOrigin, rootParent)
	}
	if childOrigin != string(OriginBlackboard) || childParent != "parent" {
		t.Errorf("child row origin/parent = %s/%q, want blackboard/parent", childOrigin, childParent)
	}
}

// TestClaimIsAtomic is the DoD that two ants cannot claim the same row: one
// open goal, claimed twice, the second claim comes back empty.
func TestClaimIsAtomic(t *testing.T) {
	ctx := context.Background()
	bb, _ := openBoard(t)
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: brief("b1", "t1", nil)}); err != nil {
		t.Fatalf("post: %v", err)
	}

	first, ok, err := bb.Claim(ctx, "ant-a", ClaimFilter{SessionID: "s1"})
	if err != nil || !ok {
		t.Fatalf("first claim: ok=%v err=%v", ok, err)
	}
	if first.Agent != "ant-a" || first.State != StateClaimed {
		t.Errorf("claimed entry agent/state = %s/%s, want ant-a/claimed", first.Agent, first.State)
	}
	if _, ok, err := bb.Claim(ctx, "ant-b", ClaimFilter{SessionID: "s1"}); err != nil || ok {
		t.Errorf("second claim must find nothing: ok=%v err=%v", ok, err)
	}
}

// TestClaimRaceGivesEachRowOnce runs many ants at one pool of goals and
// checks the single writer never hands the same row to two ants.
func TestClaimRaceGivesEachRowOnce(t *testing.T) {
	ctx := context.Background()
	bb, _ := openBoard(t)
	const goals = 5
	for i := range goals {
		id := string(rune('a' + i))
		if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t-" + id, Payload: brief("b-"+id, "t-"+id, nil)}); err != nil {
			t.Fatalf("post: %v", err)
		}
	}

	var mu sync.Mutex
	claimed := map[string]bool{}
	var wins int64
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			e, ok, err := bb.Claim(ctx, "ant-"+string(rune('A'+n)), ClaimFilter{SessionID: "s1"})
			if err != nil {
				t.Errorf("claim: %v", err)
				return
			}
			if !ok {
				return
			}
			atomic.AddInt64(&wins, 1)
			mu.Lock()
			if claimed[e.ID] {
				t.Errorf("row %s claimed twice", e.ID)
			}
			claimed[e.ID] = true
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	if wins != goals {
		t.Errorf("claims won = %d, want %d", wins, goals)
	}
}

// TestCloseSweepsChildren is the DoD that closing a parent task sweeps its
// children: the parent and every row parenting to it end expired.
func TestCloseSweepsChildren(t *testing.T) {
	ctx := context.Background()
	bb, db := openBoard(t)
	parentID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "parent", Payload: brief("b-root", "parent", nil)})
	if err != nil {
		t.Fatalf("post parent: %v", err)
	}
	childID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "child", Parent: "parent", Payload: brief("b-child", "child", nil)})
	if err != nil {
		t.Fatalf("post child: %v", err)
	}

	if err := bb.Close(ctx, "parent", nil); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s := stateFor(t, db, parentID); s != string(StateExpired) {
		t.Errorf("parent state after close = %s, want expired", s)
	}
	if s := stateFor(t, db, childID); s != string(StateExpired) {
		t.Errorf("child state after close = %s, want expired", s)
	}
}

// TestCloseBlocksUnansweredQuestion is the slice-15 DoD that a Question left
// open when the graph closes is not swept silently: it is journaled unresolved
// and its subtask is marked blocked, never expired like it simply timed out and
// never left claimed like a phantom in flight. A non-blocking advisory question
// expires with the rest, because the worker did not stop on it.
func TestCloseBlocksUnansweredQuestion(t *testing.T) {
	ctx := context.Background()
	bb, db := openBoard(t)

	subGoalID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "sub", Payload: brief("b-sub", "sub", nil)})
	if err != nil {
		t.Fatalf("post subtask goal: %v", err)
	}
	if _, ok, cerr := bb.Claim(ctx, "worker-1", ClaimFilter{SessionID: "s1"}); cerr != nil || !ok {
		t.Fatalf("claim subtask: ok=%v err=%v", ok, cerr)
	}

	blockingID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "sub", Payload: question("q-block", "sub")})
	if err != nil {
		t.Fatalf("post blocking question: %v", err)
	}
	advisory := question("q-advice", "sub")
	advisory.Blocking = false
	advisoryID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "sub", Payload: advisory})
	if err != nil {
		t.Fatalf("post advisory question: %v", err)
	}

	var unresolved [][]string
	journal := func(name string, ids []string) {
		if name == EventQuestionUnresolved {
			unresolved = append(unresolved, ids)
		}
	}
	if err := bb.Close(ctx, "sub", journal); err != nil {
		t.Fatalf("close: %v", err)
	}

	if s := stateFor(t, db, blockingID); s != string(StateBlocked) {
		t.Errorf("blocking question state = %s, want blocked", s)
	}
	if s := stateFor(t, db, subGoalID); s != string(StateBlocked) {
		t.Errorf("subtask state = %s, want blocked, not a phantom completion or a plain expiry", s)
	}
	if s := stateFor(t, db, advisoryID); s != string(StateExpired) {
		t.Errorf("advisory question state = %s, want expired; only a blocking question blocks the subtask", s)
	}
	if len(unresolved) != 1 || len(unresolved[0]) != 1 || unresolved[0][0] != blockingID {
		t.Errorf("unresolved journal = %v, want one event carrying the blocking question id %s", unresolved, blockingID)
	}
}

// TestCloseWithoutQuestionsStillSweeps proves the block path does not change the
// ordinary close: with no open questions, a nil journal is fine and every child
// still ends expired.
func TestCloseWithoutQuestionsStillSweeps(t *testing.T) {
	ctx := context.Background()
	bb, db := openBoard(t)
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "parent", Payload: brief("b-root", "parent", nil)}); err != nil {
		t.Fatalf("post parent: %v", err)
	}
	childID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "child", Parent: "parent", Payload: brief("b-child", "child", nil)})
	if err != nil {
		t.Fatalf("post child: %v", err)
	}
	if err := bb.Close(ctx, "parent", nil); err != nil {
		t.Fatalf("close: %v", err)
	}
	if s := stateFor(t, db, childID); s != string(StateExpired) {
		t.Errorf("child state after close = %s, want expired", s)
	}
}

// TestSubtaskInheritsParentTrust is the DoD that a subtask cannot shed its
// parent's trust: a child of an untrusted parent carries the untrusted label
// even though its own payload has no labels.
func TestSubtaskInheritsParentTrust(t *testing.T) {
	ctx := context.Background()
	bb, db := openBoard(t)
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "parent", Payload: brief("b-root", "parent", Labels{"untrusted"})}); err != nil {
		t.Fatalf("post parent: %v", err)
	}
	childID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "child", Parent: "parent", Payload: brief("b-child", "child", nil)})
	if err != nil {
		t.Fatalf("post child: %v", err)
	}
	if got := labelsFor(t, db, childID); got != `["untrusted"]` {
		t.Errorf("child labels = %s, want [\"untrusted\"]; a subtask cannot shed parent trust", got)
	}
}

// TestPostRejectsMissingPayload is the no-raw-transcript rule at the door: a
// row with no typed handoff is refused, so nothing but a handoff can land.
func TestPostRejectsMissingPayload(t *testing.T) {
	ctx := context.Background()
	bb, _ := openBoard(t)
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1"}); err == nil {
		t.Error("Post with no payload must be refused")
	}
	// A payload that fails its own validation is refused too.
	bad := Finding{Header: Header{ID: "f1", Kind: KindFinding, From: "x", SessionID: "s1"}}
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: bad}); err == nil {
		t.Error("Post of an invalid finding must be refused")
	}
}

// TestFindingsAndQuestionAnswer covers the read side: partial findings come
// back for a task, an open question is visible until answered, and the answer
// lands as a finding while the question goes done.
func TestFindingsAndQuestionAnswer(t *testing.T) {
	ctx := context.Background()
	bb, _ := openBoard(t)
	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: finding("f1", "t1")}); err != nil {
		t.Fatalf("post finding: %v", err)
	}
	qID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: question("q1", "t1")})
	if err != nil {
		t.Fatalf("post question: %v", err)
	}

	got, err := bb.Findings(ctx, "t1")
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(got) != 1 || got[0].Summary != "the thing was found here" {
		t.Fatalf("findings = %+v, want the one posted", got)
	}

	qs, err := bb.Questions(ctx, "t1")
	if err != nil {
		t.Fatalf("questions: %v", err)
	}
	if len(qs) != 1 || qs[0].Ask == "" {
		t.Fatalf("open questions = %+v, want the one posted", qs)
	}

	ans := finding("a1", "t1")
	ans.Summary = "cut from origin/main"
	if err := bb.Answer(ctx, qID, ans); err != nil {
		t.Fatalf("answer: %v", err)
	}
	qs, err = bb.Questions(ctx, "t1")
	if err != nil {
		t.Fatalf("questions after answer: %v", err)
	}
	if len(qs) != 0 {
		t.Errorf("answered question still open: %+v", qs)
	}
	got, err = bb.Findings(ctx, "t1")
	if err != nil {
		t.Fatalf("findings after answer: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("findings after answer = %d, want 2 (original plus answer)", len(got))
	}
}
