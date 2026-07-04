package event

// Payload types for the M0 event set. Every field is JSON-tagged and the
// golden schema test marshals one instance of each type and diffs the key
// set, so a rename cannot land silently.

// Hello is the first event on any subscription. It carries the schema
// version and the resume cursor so a client knows where the stream starts.
type Hello struct {
	Schema int    `json:"schema"`
	Cursor uint64 `json:"cursor"`
	Server string `json:"server"`
}

// SessionCreated announces a new session.
type SessionCreated struct {
	ID     string `json:"id"`
	Title  string `json:"title,omitempty"`
	Parent string `json:"parent,omitempty"`
	Root   string `json:"root"`
}

// SessionUpdated carries a changed session attribute, such as a title.
type SessionUpdated struct {
	ID    string `json:"id"`
	Title string `json:"title,omitempty"`
}

// SessionForked announces a child session split from a parent at a turn.
type SessionForked struct {
	ID     string `json:"id"`
	Parent string `json:"parent"`
	AtTurn string `json:"at_turn"`
}

// TurnStarted marks the start of a turn and names the ant running it.
type TurnStarted struct {
	ID     string `json:"id"`
	Ant    string `json:"ant"`
	Prompt string `json:"prompt"`
}

// TurnFinished marks the end of a turn with its terminal reason.
type TurnFinished struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
	Error  string `json:"error,omitempty"`
}

// TextDelta streams assistant text for one part.
type TextDelta struct {
	Part int    `json:"part"`
	Text string `json:"text"`
}

// TextEnd closes a streaming text part.
type TextEnd struct {
	Part int `json:"part"`
}

// ThinkingDelta streams reasoning text for one part.
type ThinkingDelta struct {
	Part int    `json:"part"`
	Text string `json:"text"`
}

// ThinkingEnd closes a reasoning part and records its wall time.
type ThinkingEnd struct {
	Part       int   `json:"part"`
	DurationMS int64 `json:"duration_ms"`
}

// ToolStart announces a tool call the model asked for.
type ToolStart struct {
	Part  int    `json:"part"`
	Call  string `json:"call"`
	Tool  string `json:"tool"`
	Input string `json:"input"`
}

// ToolProgress streams incremental tool output, such as sh stdout.
type ToolProgress struct {
	Part int    `json:"part"`
	Call string `json:"call"`
	Text string `json:"text"`
}

// ToolEnd carries a finished tool result. Display is the human-facing
// render; the model-facing serialization never appears in an event.
type ToolEnd struct {
	Part    int    `json:"part"`
	Call    string `json:"call"`
	Tool    string `json:"tool"`
	OK      bool   `json:"ok"`
	Display string `json:"display"`
	Spilled string `json:"spilled,omitempty"`
}

// Consequence is the core-rendered preview of what a tool call would do.
// Every client shows these same bytes; no client re-derives them.
type Consequence struct {
	Kind    string `json:"kind"` // diff, command, url, json
	Content string `json:"content"`
}

// PermissionRequested asks a client to resolve a permission decision.
// Mode is the session mode in effect, so the client's full-auto
// indicator and the core can never disagree; Reason is the prose why,
// naming the offending subcommand for a compound sh call.
type PermissionRequested struct {
	ID          string      `json:"id"`
	Call        string      `json:"call"`
	Tool        string      `json:"tool"`
	Consequence Consequence `json:"consequence"`
	Suggestions []string    `json:"suggestions,omitempty"`
	Mode        string      `json:"mode,omitempty"`
	Reason      string      `json:"reason,omitempty"`
}

// PermissionResolved records how a permission decision landed and why.
// Kind names the machinery that decided (rule, safety, headless, ...),
// so a client can tell a headless auto-deny from a user's no without
// parsing the prose.
type PermissionResolved struct {
	ID       string `json:"id"`
	Behavior string `json:"behavior"` // allow, deny
	Stage    string `json:"stage"`
	Rule     string `json:"rule,omitempty"`
	Kind     string `json:"kind,omitempty"`
	Reason   string `json:"reason,omitempty"`
}

// LedgerTurn is the per-turn meter: tokens, dollars, and cache hit rate.
type LedgerTurn struct {
	Turn       string  `json:"turn"`
	Model      string  `json:"model"`
	Input      int64   `json:"input"`
	Output     int64   `json:"output"`
	CacheRead  int64   `json:"cache_read"`
	CacheWrite int64   `json:"cache_write"`
	CostUSD    float64 `json:"cost_usd"`
	CacheRate  float64 `json:"cache_rate"`
}

// Log is a diagnostic line a client may show in a debug surface.
type Log struct {
	Level string `json:"level"`
	Text  string `json:"text"`
}

// ErrorInfo is a client-facing failure with a taxonomy code. A
// model-correctable mistake is a tool result, never one of these. Code is
// core's ErrorKind on the wire; Retryable tells a client whether a retry
// could help; Cause is an unwrapped underlying detail (doc 01 section 10).
type ErrorInfo struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	Ant       string `json:"ant,omitempty"`
	Cause     string `json:"cause,omitempty"`
}

// AntSpawned is defined for the wire schema and unused until M3.
type AntSpawned struct {
	ID   string `json:"id"`
	Card string `json:"card"`
}

// RouteDecided is defined for the wire schema and unused until M3.
type RouteDecided struct {
	Task string `json:"task"`
	Ant  string `json:"ant"`
	Why  string `json:"why"`
}

// MemoryFolded reports one consolidation over one namespace: the net effect
// on live memory, not the fold's internal accounting. Merged is the live rows
// the fold wrote and Reflections how many of those were lessons; Archived is
// the rows it retired for staleness; Candidates is the pending proposals it
// weighed. A client tails these to show what folding did without a recall.
type MemoryFolded struct {
	Namespace   string `json:"namespace"`
	Merged      int    `json:"merged"`
	Reflections int    `json:"reflections"`
	Archived    int    `json:"archived"`
	Candidates  int    `json:"candidates"`
}

// FanOutApproved is the loud half of D5: when the queen splits a task it
// publishes exactly why, so a human can audit a colony that woke and see
// which of the three tests let it through. The fields are the rendered
// FanOutArg the gate produced.
type FanOutApproved struct {
	Task           string `json:"task"`
	Subtasks       int    `json:"subtasks"`
	IndependenceBy string `json:"independence_by"`
	Workload       string `json:"workload"`
	Specialist     string `json:"specialist,omitempty"`
	Projected      int64  `json:"projected"`
	Remaining      int64  `json:"remaining"`
}

// FanOutRefused is the quiet half of D5: the queen names the failing test
// and stays single-ant. It rides the debug lane, not the normal stream, so
// a session is not cluttered with decisions not taken.
type FanOutRefused struct {
	Task   string `json:"task"`
	Failed string `json:"failed"` // independence, workload, budget
	Reason string `json:"reason,omitempty"`
}

// ColonyThrottle records a ceiling deferring work: a wake refused at
// max_awake, or a fan-out batch refused at max_fanout_session. Tasks are the
// ids deferred. Sourced from the governor through the JournalFunc seam.
type ColonyThrottle struct {
	Reason string   `json:"reason"` // max_awake, max_fanout_session
	Tasks  []string `json:"tasks,omitempty"`
}

// WorkerWoke marks a worker ant starting a subtask. It rides the
// must-deliver lane: the colony view is never wrong about who is alive.
// File is the sidechain the worker writes to, the locator the colony
// drill-in reads back to render the ant's run; it is the card-and-task
// key, not the forager lane, because two lanes on one card share a file.
type WorkerWoke struct {
	Ant  string `json:"ant"`
	Task string `json:"task"`
	Tier string `json:"tier,omitempty"`
	File string `json:"file,omitempty"`
}

// WorkerBlocked marks a worker stopping on a Question it cannot answer. It
// rides the must-deliver lane and carries the ask so the view shows it
// inline for the user to answer from the list.
type WorkerBlocked struct {
	Ant      string `json:"ant"`
	Task     string `json:"task"`
	Question string `json:"question"`
}

// WorkerFinished marks a worker ending its subtask. It rides the
// must-deliver lane so the view always settles a done worker.
type WorkerFinished struct {
	Ant  string `json:"ant"`
	Task string `json:"task"`
	OK   bool   `json:"ok"`
}

// ColonyProgress is a worker's climbing token count or a short status note.
// It rides the lossy lane (D18): a dropped tick is not a correctness
// problem, and the next tick supersedes it.
type ColonyProgress struct {
	Ant    string `json:"ant"`
	Task   string `json:"task"`
	Tokens int64  `json:"tokens,omitempty"`
	Note   string `json:"note,omitempty"`
}

// QuestionUnresolved records a blocking Question left open when its task
// graph closed: a worker that stopped waiting on a human, marked blocked
// rather than swept to expired. Sourced through the JournalFunc seam.
type QuestionUnresolved struct {
	Tasks []string `json:"tasks,omitempty"`
}

// WorktreeConflict records a reconcile that could not land cleanly, naming
// the task ids that collided. The clean prefix stays landed; the conflicted
// patch surfaces to the foreground. Sourced through the JournalFunc seam.
type WorktreeConflict struct {
	Tasks []string `json:"tasks,omitempty"`
}

// ArbitrationOpened marks a quorum convened over disagreeing Verdicts. Stakes
// names the quorum size the disagreement earned.
type ArbitrationOpened struct {
	Subject string   `json:"subject"`
	Members []string `json:"members,omitempty"`
	Stakes  string   `json:"stakes"`
}

// ArbitrationClosed records how an arbitration landed: the decision, the vote
// split, and who decided, so a human can audit a judgment the colony made.
type ArbitrationClosed struct {
	Subject   string `json:"subject"`
	Decision  string `json:"decision"`
	For       int    `json:"for"`
	Against   int    `json:"against"`
	DecidedBy string `json:"decided_by"`
}
