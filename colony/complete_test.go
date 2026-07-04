package colony

import (
	"context"
	"database/sql"
	"testing"

	memsqlite "github.com/tamnd/ari/memory/sqlite"
)

// rowState reads one blackboard row's state directly, so a completion test can
// prove the goal flipped, not merely that a result appeared.
func rowState(t *testing.T, db *memsqlite.Store, id string) string {
	t.Helper()
	var state string
	err := db.Read(context.Background(), func(sqldb *sql.DB) error {
		return sqldb.QueryRow(`SELECT state FROM blackboard WHERE id = ?`, id).Scan(&state)
	})
	if err != nil {
		t.Fatalf("read state of %s: %v", id, err)
	}
	return state
}

// rowLabels reads one row's trust labels directly.
func rowLabels(t *testing.T, db *memsqlite.Store, id string) string {
	t.Helper()
	var labels string
	err := db.Read(context.Background(), func(sqldb *sql.DB) error {
		return sqldb.QueryRow(`SELECT labels FROM blackboard WHERE id = ?`, id).Scan(&labels)
	})
	if err != nil {
		t.Fatalf("read labels of %s: %v", id, err)
	}
	return labels
}

// TestCompleteStoresResultAndFlipsGoal is the DoD that a worker's clean exit
// posts its result and marks its claim done in one write: the finding is
// readable on the task and the goal row is done, never a done result whose goal
// is still claimed.
func TestCompleteStoresResultAndFlipsGoal(t *testing.T) {
	bb, db := openBoard(t)
	ctx := context.Background()

	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: brief("b1", "t1", nil)}); err != nil {
		t.Fatalf("post goal: %v", err)
	}
	claimed, ok, err := bb.Claim(ctx, "surveyor-1", ClaimFilter{SessionID: "s1"})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}

	resultID, err := bb.Complete(ctx, claimed.ID, finding("f1", "t1"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if resultID == "" {
		t.Fatal("complete returned no result row id")
	}
	if s := rowState(t, db, claimed.ID); s != string(StateDone) {
		t.Errorf("goal state after completion = %q, want done", s)
	}

	fs, err := bb.Findings(ctx, "t1")
	if err != nil {
		t.Fatalf("findings: %v", err)
	}
	if len(fs) != 1 || fs[0].Summary != "the thing was found here" {
		t.Errorf("findings = %+v, want the one completed finding", fs)
	}
}

// TestCompleteRejectsUnclaimedGoal proves Complete only closes a claimed goal:
// an open goal nobody took, and an id that is no goal at all, are both refused,
// so a result can never attach to work no worker was doing.
func TestCompleteRejectsUnclaimedGoal(t *testing.T) {
	bb, _ := openBoard(t)
	ctx := context.Background()

	goalID, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: brief("b1", "t1", nil)})
	if err != nil {
		t.Fatalf("post goal: %v", err)
	}
	if _, err := bb.Complete(ctx, goalID, finding("f1", "t1")); err == nil {
		t.Error("expected an error completing an open, unclaimed goal")
	}
	if _, err := bb.Complete(ctx, "no-such-row", finding("f2", "t1")); err == nil {
		t.Error("expected an error completing an id that is no goal")
	}
}

// TestCompletedResultInheritsTrust is the D12.2 laundering guard: a result
// posted onto a labeled goal carries the goal's label, so a worker cannot shed
// a trust label by finishing its task.
func TestCompletedResultInheritsTrust(t *testing.T) {
	bb, db := openBoard(t)
	ctx := context.Background()

	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: brief("b1", "t1", Labels{"untrusted"})}); err != nil {
		t.Fatalf("post goal: %v", err)
	}
	claimed, ok, err := bb.Claim(ctx, "surveyor-1", ClaimFilter{SessionID: "s1"})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	resultID, err := bb.Complete(ctx, claimed.ID, finding("f1", "t1"))
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	if got := rowLabels(t, db, resultID); got != `["untrusted"]` {
		t.Errorf("result labels = %s, want the inherited [\"untrusted\"]", got)
	}
}

// TestFailMarksClaimIncomplete is the DoD that a stopped worker leaves a
// well-formed failed row, and that failing is only valid on a claimed goal.
func TestFailMarksClaimIncomplete(t *testing.T) {
	bb, db := openBoard(t)
	ctx := context.Background()

	if _, err := bb.Post(ctx, Entry{SessionID: "s1", TaskID: "t1", Payload: brief("b1", "t1", nil)}); err != nil {
		t.Fatalf("post goal: %v", err)
	}
	claimed, ok, err := bb.Claim(ctx, "surveyor-1", ClaimFilter{SessionID: "s1"})
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := bb.Fail(ctx, claimed.ID); err != nil {
		t.Fatalf("fail: %v", err)
	}
	if s := rowState(t, db, claimed.ID); s != string(StateFailed) {
		t.Errorf("goal state after failure = %q, want failed", s)
	}
	// A second failure finds nothing claimed to fail.
	if err := bb.Fail(ctx, claimed.ID); err == nil {
		t.Error("expected an error failing a goal that is no longer claimed")
	}
}
