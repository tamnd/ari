package colony

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// EntryKind is the blackboard row's kind, the shape of the coordination
// state a row carries. It is distinct from a handoff Kind: a goal row
// carries a TaskBrief, a partial row carries a Finding or a Patch, a verdict
// row carries a Verdict, a question row carries a Question.
type EntryKind string

const (
	EntryGoal     EntryKind = "goal"
	EntryClaim    EntryKind = "claim"
	EntryPartial  EntryKind = "partial"
	EntryQuestion EntryKind = "question"
	EntryVerdict  EntryKind = "verdict"
)

// Origin says where a row was born. A foreground row is the root a session
// opened; a blackboard row is a subtask the queen or a worker posted during
// a fan-out.
type Origin string

const (
	OriginForeground Origin = "foreground"
	OriginBlackboard Origin = "blackboard"
)

// State is a row's lifecycle position.
type State string

const (
	StateOpen    State = "open"
	StateClaimed State = "claimed"
	StateDone    State = "done"
	StateFailed  State = "failed"
	StateExpired State = "expired"
	// StateBlocked is a subtask paused on a Question its worker auto-denied
	// and nobody answered before the graph closed. It is not done and not a
	// plain expiry: a reader can tell a task that ran out of time from one
	// that stopped waiting on a human (doc 09 section 8).
	StateBlocked State = "blocked"
)

// lifetimes give each kind its hours-not-days expiry: goals and claims live
// long enough to be worked, partials and questions a little longer, verdicts
// longest because the reconciliation record should outlive the work slightly
// (doc 09 section 2.5).
var lifetimes = map[EntryKind]time.Duration{
	EntryGoal:     4 * time.Hour,
	EntryClaim:    4 * time.Hour,
	EntryPartial:  8 * time.Hour,
	EntryQuestion: 8 * time.Hour,
	EntryVerdict:  24 * time.Hour,
}

// ErrNoEntry is returned when a row id has no blackboard row.
var ErrNoEntry = errors.New("colony: no blackboard entry with that id")

// Entry is one blackboard row, the envelope around a handoff. It carries the
// coordination metadata (who, what state, which task) around the typed
// payload, which is the only thing an ant actually reads.
type Entry struct {
	ID         string
	SessionID  string
	TaskID     string
	Parent     string
	Origin     Origin
	Kind       EntryKind
	Goal       string
	Payload    Handoff
	Agent      string
	State      State
	ClaimCount int
	Trust      Labels
	CreatedAt  time.Time
	ExpiresAt  time.Time
}

// ClaimFilter narrows what an idle ant will claim: the session it is working
// and, optionally, the entry kinds it is willing to take.
type ClaimFilter struct {
	SessionID string
	Kinds     []EntryKind
}

// Blackboard is the colony's only coordination surface. It stores typed
// handoffs, never raw transcripts, and every write funnels through the
// colony.db writer goroutine (D10).
type Blackboard interface {
	// Post inserts a row, deriving its origin, kind, expiry, and inherited
	// trust, and returns the row id.
	Post(ctx context.Context, e Entry) (string, error)
	// Claim atomically takes one open goal matching the filter for an ant,
	// returning the claimed entry and true, or false when nothing is free.
	Claim(ctx context.Context, antID string, filter ClaimFilter) (Entry, bool, error)
	// Findings returns the Finding payloads posted for a task.
	Findings(ctx context.Context, taskID string) ([]Finding, error)
	// Patches returns the Patch payloads posted for a task, the writer
	// results reconcile composes into one diff.
	Patches(ctx context.Context, taskID string) ([]Patch, error)
	// Questions returns the open Question payloads posted for a task.
	Questions(ctx context.Context, taskID string) ([]Question, error)
	// Answer resolves a question with a Finding and marks the question done.
	Answer(ctx context.Context, questionID string, ans Finding) error
	// Complete ends a worker's claim: it validates the result handoff,
	// stores it parented to the task, and flips the claimed goal to done,
	// all in one write so the transition cannot race (doc 09 section 5.1).
	Complete(ctx context.Context, claimID string, result Handoff) (string, error)
	// Fail marks a claimed goal incomplete, the record a crashed or
	// cancelled worker leaves behind (doc 09 section 11.1).
	Fail(ctx context.Context, claimID string) error
	// Close sweeps a task and every child it parents to expired, the
	// hours-not-days end of a task graph's life. A Question left open when
	// the graph closes is not swept silently: it is journaled unresolved and
	// its subtask is marked blocked, so an unanswered block is a record, not
	// a phantom completion (doc 09 section 8). The journal seam may be nil.
	Close(ctx context.Context, taskID string, journal JournalFunc) error
}

// board is the SQLite-backed Blackboard over colony.db.
type board struct {
	db  substrate
	now func() time.Time
}

// NewBlackboard builds a blackboard over the colony.db handle. The clock is
// injectable so tests pin expiry without sleeping.
func NewBlackboard(db substrate, now func() time.Time) Blackboard {
	if now == nil {
		now = time.Now
	}
	return &board{db: db, now: now}
}

// Post validates the payload is a real handoff, derives the row's origin,
// kind, and expiry, inherits trust from the parent so a subtask can never
// shed its parent's labels, and inserts the row.
func (b *board) Post(ctx context.Context, e Entry) (string, error) {
	if e.Payload == nil {
		return "", fmt.Errorf("blackboard: an entry needs a handoff payload, never a transcript")
	}
	if err := e.Payload.Validate(); err != nil {
		return "", fmt.Errorf("blackboard: payload rejected: %w", err)
	}
	now := b.now()
	b.stamp(&e, now)
	err := b.db.Write(ctx, func(tx *sql.Tx) error {
		return b.insert(ctx, tx, e, now)
	})
	if err != nil {
		return "", err
	}
	return e.ID, nil
}

// stamp fills the derived fields a fresh row needs before insertion: its id,
// kind, origin, opening state, and the created and expiry timestamps. It is
// shared by Post and Complete so a result row and a goal row are stamped the
// same way.
func (b *board) stamp(e *Entry, now time.Time) {
	if e.ID == "" {
		e.ID = newID(now)
	}
	if e.Kind == "" {
		e.Kind = kindFor(e.Payload)
	}
	if e.Origin == "" {
		if e.Parent != "" {
			e.Origin = OriginBlackboard
		} else {
			e.Origin = OriginForeground
		}
	}
	if e.State == "" {
		e.State = openStateFor(e.Kind)
	}
	e.CreatedAt = now
	e.ExpiresAt = now.Add(lifetimes[e.Kind])
}

// insert writes one stamped row, unioning its own labels with any it inherits
// from its parent so a subtask can never shed its parent's trust (doc 09
// section 12.2). It runs inside the caller's write transaction.
func (b *board) insert(ctx context.Context, tx *sql.Tx, e Entry, now time.Time) error {
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return err
	}
	trust := e.Trust.Union(e.Payload.Hdr().Labels)
	if e.Parent != "" {
		inherited, perr := parentTrust(ctx, tx, e.Parent)
		if perr != nil {
			return perr
		}
		trust = trust.Union(inherited)
	}
	labels, merr := json.Marshal(nonNil(trust))
	if merr != nil {
		return merr
	}
	_, ierr := tx.ExecContext(ctx,
		`INSERT INTO blackboard
		   (id, session_id, task_id, parent, origin, kind, goal, payload, agent, state, claim_count, labels, created_at, expires_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		e.ID, e.SessionID, e.TaskID, e.Parent, e.Origin, e.Kind, e.Goal, string(payload),
		e.Agent, e.State, string(labels), now.Unix(), e.ExpiresAt.Unix())
	return ierr
}

// Complete is a worker's clean exit: it stores the result handoff parented to
// the task and flips the claimed goal to done in one write, so a reader never
// sees a done result whose goal is still claimed or the reverse. The result
// inherits the goal's trust through its parent, so a worker cannot launder a
// label off its work by finishing it (doc 09 sections 5.1 and 12.2).
func (b *board) Complete(ctx context.Context, claimID string, result Handoff) (string, error) {
	if result == nil {
		return "", fmt.Errorf("blackboard: a completion needs a result handoff, never a transcript")
	}
	if err := result.Validate(); err != nil {
		return "", fmt.Errorf("blackboard: result rejected: %w", err)
	}
	now := b.now()
	var resultID string
	err := b.db.Write(ctx, func(tx *sql.Tx) error {
		var sessionID, taskID, state string
		row := tx.QueryRowContext(ctx,
			`SELECT session_id, task_id, state FROM blackboard WHERE id = ? AND kind = ?`, claimID, EntryGoal)
		if serr := row.Scan(&sessionID, &taskID, &state); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return ErrNoEntry
			}
			return serr
		}
		if state != string(StateClaimed) {
			return fmt.Errorf("blackboard: goal %s is not claimed (state %s), cannot complete it", claimID, state)
		}
		e := Entry{SessionID: sessionID, TaskID: taskID, Parent: taskID, Payload: result}
		b.stamp(&e, now)
		if ierr := b.insert(ctx, tx, e, now); ierr != nil {
			return ierr
		}
		resultID = e.ID
		res, uerr := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ? WHERE id = ? AND state = ?`, StateDone, claimID, StateClaimed)
		if uerr != nil {
			return uerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("blackboard: goal %s was claimed out from under the completion", claimID)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return resultID, nil
}

// Fail marks a claimed goal incomplete. It is the record a crashed, timed-out,
// or cancelled worker leaves: the goal flips to failed, which is not terminal
// for the graph (orphan recovery may reopen it in a later slice), but is a
// well-formed row a reader can see, never a half-written result (doc 09
// section 11.1).
func (b *board) Fail(ctx context.Context, claimID string) error {
	return b.db.Write(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ? WHERE id = ? AND state = ?`, StateFailed, claimID, StateClaimed)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("blackboard: goal %s is not claimed, cannot fail it", claimID)
		}
		return nil
	})
}

// Claim is compare-and-swap inside the writer goroutine: it takes the oldest
// open goal matching the filter and flips it to claimed for the ant, so two
// ants racing for one goal cannot both win, because there is one writer and
// it applies claims in channel order.
func (b *board) Claim(ctx context.Context, antID string, filter ClaimFilter) (Entry, bool, error) {
	var claimed Entry
	var ok bool
	err := b.db.Write(ctx, func(tx *sql.Tx) error {
		id, ferr := oldestOpen(ctx, tx, filter)
		if errors.Is(ferr, sql.ErrNoRows) {
			return nil
		}
		if ferr != nil {
			return ferr
		}
		res, uerr := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ?, agent = ? WHERE id = ? AND state = ?`,
			StateClaimed, antID, id, StateOpen)
		if uerr != nil {
			return uerr
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			// Lost the race to another claim applied just before this one.
			return nil
		}
		e, gerr := loadEntry(ctx, tx, id)
		if gerr != nil {
			return gerr
		}
		claimed, ok = e, true
		return nil
	})
	return claimed, ok, err
}

// Findings returns the Finding payloads posted for a task, the survey and
// triage results a fan-out produced.
func (b *board) Findings(ctx context.Context, taskID string) ([]Finding, error) {
	var out []Finding
	err := b.db.Read(ctx, func(db *sql.DB) error {
		rows, qerr := db.QueryContext(ctx,
			`SELECT payload FROM blackboard WHERE task_id = ? AND kind = ? ORDER BY created_at`,
			taskID, EntryPartial)
		if qerr != nil {
			return qerr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var payload string
			if serr := rows.Scan(&payload); serr != nil {
				return serr
			}
			var f Finding
			if json.Unmarshal([]byte(payload), &f) == nil && f.Kind == KindFinding {
				out = append(out, f)
			}
		}
		return rows.Err()
	})
	return out, err
}

// Patches returns the Patch payloads posted for a task, the writer results a
// fan-out produced for reconcile to compose. A Finding and a Patch land under
// the same partial kind, so the payload's own header kind is the filter that
// keeps a survey's finding out of a writer's reconcile.
func (b *board) Patches(ctx context.Context, taskID string) ([]Patch, error) {
	var out []Patch
	err := b.db.Read(ctx, func(db *sql.DB) error {
		rows, qerr := db.QueryContext(ctx,
			`SELECT payload FROM blackboard WHERE task_id = ? AND kind = ? ORDER BY created_at`,
			taskID, EntryPartial)
		if qerr != nil {
			return qerr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var payload string
			if serr := rows.Scan(&payload); serr != nil {
				return serr
			}
			var p Patch
			if json.Unmarshal([]byte(payload), &p) == nil && p.Kind == KindPatch {
				out = append(out, p)
			}
		}
		return rows.Err()
	})
	return out, err
}

// Questions returns the open Question payloads posted for a task; an answered
// question is done and is not returned.
func (b *board) Questions(ctx context.Context, taskID string) ([]Question, error) {
	var out []Question
	err := b.db.Read(ctx, func(db *sql.DB) error {
		rows, qerr := db.QueryContext(ctx,
			`SELECT payload FROM blackboard WHERE task_id = ? AND kind = ? AND state = ? ORDER BY created_at`,
			taskID, EntryQuestion, StateOpen)
		if qerr != nil {
			return qerr
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var payload string
			if serr := rows.Scan(&payload); serr != nil {
				return serr
			}
			var q Question
			if json.Unmarshal([]byte(payload), &q) == nil && q.Kind == KindQuestion {
				out = append(out, q)
			}
		}
		return rows.Err()
	})
	return out, err
}

// Answer resolves a question by posting the answer as a Finding on the same
// task and marking the question row done. The answer is a typed handoff back
// to the asker, not a broadcast (doc 09 section 8).
func (b *board) Answer(ctx context.Context, questionID string, ans Finding) error {
	if err := ans.Validate(); err != nil {
		return fmt.Errorf("blackboard: answer rejected: %w", err)
	}
	now := b.now()
	payload, err := json.Marshal(ans)
	if err != nil {
		return err
	}
	return b.db.Write(ctx, func(tx *sql.Tx) error {
		var taskID, sessionID string
		row := tx.QueryRowContext(ctx, `SELECT task_id, session_id FROM blackboard WHERE id = ? AND kind = ?`, questionID, EntryQuestion)
		if serr := row.Scan(&taskID, &sessionID); serr != nil {
			if errors.Is(serr, sql.ErrNoRows) {
				return ErrNoEntry
			}
			return serr
		}
		res, uerr := tx.ExecContext(ctx, `UPDATE blackboard SET state = ? WHERE id = ? AND state = ?`, StateDone, questionID, StateOpen)
		if uerr != nil {
			return uerr
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("blackboard: question %s is not open", questionID)
		}
		_, ierr := tx.ExecContext(ctx,
			`INSERT INTO blackboard
			   (id, session_id, task_id, parent, origin, kind, goal, payload, agent, state, claim_count, labels, created_at, expires_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', ?, 0, '[]', ?, ?)`,
			newID(now), sessionID, taskID, questionID, OriginBlackboard, EntryPartial, "answer", string(payload),
			StateDone, now.Unix(), now.Add(lifetimes[EntryPartial]).Unix())
		return ierr
	})
}

// Close sweeps a task and every child it parents to expired. This is the
// hours-not-days end of a task graph: when the parent is done, the live
// coordination state around it has no reason to persist.
//
// An open blocking Question is the one thing not swept away quietly. Before the
// expiry sweep, each open blocking question under the graph is journaled as
// unresolved and its subtask is marked blocked, so a worker that stopped waiting
// on a human leaves a record a reader can tell from a task that merely ran out
// of time, and the subtask is never mistaken for completed (doc 09 section 8).
func (b *board) Close(ctx context.Context, taskID string, journal JournalFunc) error {
	return b.db.Write(ctx, func(tx *sql.Tx) error {
		if err := b.blockOpenQuestions(ctx, tx, taskID, journal); err != nil {
			return err
		}
		// Expire the rest, leaving done, already-expired, and the just-blocked
		// rows untouched so the block record survives the sweep.
		_, err := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ?
			   WHERE state NOT IN (?, ?, ?) AND (task_id = ? OR parent = ?)`,
			StateExpired, StateExpired, StateDone, StateBlocked, taskID, taskID)
		return err
	})
}

// blockOpenQuestions finds every open blocking Question under the graph, marks
// it and the subtask it paused blocked, and journals each as unresolved. It runs
// inside Close's write transaction so the block records and the expiry sweep
// commit together. It returns the count blocked, for the caller's own record.
func (b *board) blockOpenQuestions(ctx context.Context, tx *sql.Tx, taskID string, journal JournalFunc) error {
	rows, err := tx.QueryContext(ctx,
		`SELECT id, task_id, payload FROM blackboard
		   WHERE kind = ? AND state = ? AND (task_id = ? OR parent = ?)`,
		EntryQuestion, StateOpen, taskID, taskID)
	if err != nil {
		return err
	}
	type openQ struct{ id, task string }
	var open []openQ
	for rows.Next() {
		var id, qtask, payload string
		if serr := rows.Scan(&id, &qtask, &payload); serr != nil {
			_ = rows.Close()
			return serr
		}
		var q Question
		if json.Unmarshal([]byte(payload), &q) != nil || !q.Blocking {
			continue // a non-blocking advisory question just expires with the rest
		}
		open = append(open, openQ{id: id, task: qtask})
	}
	if cerr := rows.Close(); cerr != nil {
		return cerr
	}
	if rerr := rows.Err(); rerr != nil {
		return rerr
	}

	for _, q := range open {
		// The question row itself is blocked, so a reader still finds the ask.
		if _, err := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ? WHERE id = ?`, StateBlocked, q.id); err != nil {
			return err
		}
		// The subtask the worker was on is blocked too, never left claimed or
		// swept to a plain expiry that reads like it simply timed out.
		if _, err := tx.ExecContext(ctx,
			`UPDATE blackboard SET state = ? WHERE task_id = ? AND kind = ? AND state NOT IN (?, ?)`,
			StateBlocked, q.task, EntryGoal, StateDone, StateBlocked); err != nil {
			return err
		}
		if journal != nil {
			journal(EventQuestionUnresolved, []string{q.id})
		}
	}
	return nil
}

// parentTrust reads a parent row's labels so a child can inherit them. A
// missing parent is an error, because a subtask that cannot find its parent
// cannot prove it is not shedding trust.
func parentTrust(ctx context.Context, tx *sql.Tx, parent string) (Labels, error) {
	var labels string
	row := tx.QueryRowContext(ctx, `SELECT labels FROM blackboard WHERE task_id = ? OR id = ? LIMIT 1`, parent, parent)
	if err := row.Scan(&labels); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("blackboard: parent %s not found for trust inheritance", parent)
		}
		return nil, err
	}
	var out Labels
	if err := json.Unmarshal([]byte(labels), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// oldestOpen finds the oldest open goal matching the filter, the entry a
// polling ant should claim first.
func oldestOpen(ctx context.Context, tx *sql.Tx, filter ClaimFilter) (string, error) {
	kinds := filter.Kinds
	if len(kinds) == 0 {
		kinds = []EntryKind{EntryGoal}
	}
	q := `SELECT id FROM blackboard WHERE session_id = ? AND state = ? AND kind IN (` + placeholders(len(kinds)) + `) ORDER BY created_at LIMIT 1`
	args := []any{filter.SessionID, StateOpen}
	for _, k := range kinds {
		args = append(args, k)
	}
	var id string
	err := tx.QueryRowContext(ctx, q, args...).Scan(&id)
	return id, err
}

// loadEntry reads a full row back into an Entry, decoding the payload to its
// concrete handoff type.
func loadEntry(ctx context.Context, tx *sql.Tx, id string) (Entry, error) {
	var e Entry
	var taskID, parent, agent, payload, labels sql.NullString
	var created, expires int64
	row := tx.QueryRowContext(ctx,
		`SELECT id, session_id, task_id, parent, origin, kind, goal, payload, agent, state, claim_count, labels, created_at, expires_at
		   FROM blackboard WHERE id = ?`, id)
	if err := row.Scan(&e.ID, &e.SessionID, &taskID, &parent, &e.Origin, &e.Kind, &e.Goal,
		&payload, &agent, &e.State, &e.ClaimCount, &labels, &created, &expires); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Entry{}, ErrNoEntry
		}
		return Entry{}, err
	}
	e.TaskID, e.Parent, e.Agent = taskID.String, parent.String, agent.String
	e.CreatedAt = time.Unix(created, 0)
	e.ExpiresAt = time.Unix(expires, 0)
	if labels.Valid {
		_ = json.Unmarshal([]byte(labels.String), &e.Trust)
	}
	h, err := decodeHandoff(e.Kind, payload.String)
	if err != nil {
		return Entry{}, err
	}
	e.Payload = h
	return e, nil
}

// decodeHandoff turns a stored payload back into its concrete handoff type,
// chosen by the row's kind.
func decodeHandoff(kind EntryKind, payload string) (Handoff, error) {
	switch kind {
	case EntryGoal, EntryClaim:
		var b TaskBrief
		return b, json.Unmarshal([]byte(payload), &b)
	case EntryPartial:
		var f Finding
		if json.Unmarshal([]byte(payload), &f) == nil && f.Kind == KindFinding {
			return f, nil
		}
		var p Patch
		return p, json.Unmarshal([]byte(payload), &p)
	case EntryVerdict:
		var v Verdict
		return v, json.Unmarshal([]byte(payload), &v)
	case EntryQuestion:
		var q Question
		return q, json.Unmarshal([]byte(payload), &q)
	}
	return nil, fmt.Errorf("blackboard: unknown entry kind %q", kind)
}

// kindFor maps a handoff to the board kind that carries it.
func kindFor(h Handoff) EntryKind {
	switch h.Hdr().Kind {
	case KindTaskBrief:
		return EntryGoal
	case KindVerdict:
		return EntryVerdict
	case KindQuestion:
		return EntryQuestion
	default:
		return EntryPartial
	}
}

// openStateFor gives a fresh row its starting state: a goal is claimable so
// it opens open, a result row lands done because nobody claims it.
func openStateFor(kind EntryKind) State {
	switch kind {
	case EntryGoal, EntryClaim, EntryQuestion:
		return StateOpen
	default:
		return StateDone
	}
}

// placeholders renders n SQL bind placeholders.
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?, ", n), ", ")
}

// nonNil turns a nil label set into an empty one so the labels column always
// stores a JSON array, never null.
func nonNil(l Labels) Labels {
	if l == nil {
		return Labels{}
	}
	return l
}
