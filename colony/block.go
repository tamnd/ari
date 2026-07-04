package colony

// A colony worker has no human at its console, so the safe answer to any
// ask-tier permission prompt is not to guess and not to proceed but to deny the
// call and post a Question the foreground can answer (doc 09 section 8, D15).
// This is the colony's version of the crush grace-period lesson: in-flight
// input must never approve a prompt, and a worker with nobody watching has less
// standing to approve than a headless run, not more. The worst case of a worker
// meeting a permission wall is a paused subtask and a question in the queue,
// never an unapproved mutation.
//
// The asymmetry that makes this safe lives upstream in the permission pipeline,
// not here: a safety-check deny (a nest file, a VCS internal, a shell config) is
// a Deny decision the pipeline returns at its bypass-immune floor, so it never
// reaches a Resolver and is never turned into a Question. NewQuestion is only
// ever reached for the ask-tier decisions a human legitimately owns. Those
// denials are final by design, not a decision to punt to the user, because they
// are exactly the ones that must survive a user who would rubber-stamp anything.

// EventQuestionUnresolved is the journal event a Question left open when its
// task graph closes emits, so an unanswered block is a visible record and not a
// silent completion (doc 09 section 8). The kernel names it; the wiring writes
// it.
const EventQuestionUnresolved = "colony.question.unresolved"

// BlockedAsk is what the worker's permission resolver hands the colony when it
// auto-denies an ask-tier call: the rendered prompt and consequence the
// pipeline already built, plus the identity that places the block in the task
// graph. The wiring pulls these fields off a permission Request; colony imports
// no permission package, so the shape crosses the seam as plain data.
type BlockedAsk struct {
	ID          string       // reuse the permission request id so a replay lines up
	Worker      string       // the ant that hit the wall
	TaskID      string       // the subtask it was working
	SessionID   string       // the session the subtask belongs to
	Ask         string       // the prompt the pipeline would have put to a human
	Consequence string       // the rendered diff or command the human judges
	Options     []string     // the choices the prompt offered, if any
	Context     []ContextRef // refs to the rest, so the foreground reads lazily
	Labels      Labels       // the trust labels the worker's context carried
}

// NewQuestion builds the blocking Question a worker posts in place of guessing.
// It is always Blocking: a worker that hit an ask-tier wall stops until the
// user answers, so the subtask pauses rather than proceeding on an unreviewed
// call. The answer flows back as a Finding through the board's Answer, which
// unblocks or redirects the worker.
func NewQuestion(a BlockedAsk) Question {
	return Question{
		Header: Header{
			ID:        a.ID,
			Kind:      KindQuestion,
			From:      a.Worker,
			TaskID:    a.TaskID,
			SessionID: a.SessionID,
			Labels:    a.Labels,
		},
		Ask:         a.Ask,
		Consequence: a.Consequence,
		Options:     a.Options,
		Context:     a.Context,
		Blocking:    true,
	}
}
