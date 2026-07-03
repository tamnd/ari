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
type PermissionRequested struct {
	ID          string      `json:"id"`
	Call        string      `json:"call"`
	Tool        string      `json:"tool"`
	Consequence Consequence `json:"consequence"`
	Suggestions []string    `json:"suggestions,omitempty"`
}

// PermissionResolved records how a permission decision landed and why.
type PermissionResolved struct {
	ID       string `json:"id"`
	Behavior string `json:"behavior"` // allow, deny
	Stage    string `json:"stage"`
	Rule     string `json:"rule,omitempty"`
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
// model-correctable mistake is a tool result, never one of these.
type ErrorInfo struct {
	Code string `json:"code"`
	Text string `json:"text"`
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

// MemoryFolded is defined for the wire schema and unused until M2.
type MemoryFolded struct {
	Namespace string `json:"namespace"`
	Merged    int    `json:"merged"`
	Archived  int    `json:"archived"`
}
