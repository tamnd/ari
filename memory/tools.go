package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tamnd/ari/memory/sqlite"
	"github.com/tamnd/ari/tool"
)

// ToolStore is the slice of the memory store the three memory tools reach. The
// concrete *sqlite.Store satisfies it; a test drives the tools with a fake. The
// interface is narrow on purpose: the tools are the ant's hands on its own
// memory and they do exactly three things, propose, recall, and retire.
type ToolStore interface {
	// InsertCandidate queues a proposed memory for the next fold (D12).
	InsertCandidate(ctx context.Context, id string, c sqlite.Candidate) error
	// Recall runs hybrid recall and bumps access stats (slice 4).
	Recall(ctx context.Context, ns, query string, vec []float32, budget int) ([]sqlite.Memory, error)
	// MemoryLabel reads a live row's label for the forget consequence.
	MemoryLabel(ctx context.Context, ns, id string) (string, bool, error)
	// ArchiveMemory retires a row (sets archived_at); it never deletes.
	ArchiveMemory(ctx context.Context, ns, id string) (string, bool, error)
}

const (
	recallDefaultLimit = 8
	recallMaxLimit     = 20
	maxImportance      = 10
)

// anchorArg is one anchor the ant names on a remember call: a file, symbol, or
// command the memory is about, with the file's content hash when it has one so
// the staleness pass can tell later which anchors a commit invalidated.
type anchorArg struct {
	Kind string `json:"kind"`
	Ref  string `json:"ref"`
	Hash string `json:"hash,omitempty"`
}

// --- remember ---

type rememberArgs struct {
	Body       string      `json:"body"`
	Importance int         `json:"importance"`
	Kind       string      `json:"kind,omitempty"`
	Anchors    []anchorArg `json:"anchors"`
	Evidence   []string    `json:"evidence,omitempty"`
}

// rememberTool queues a candidate for the next fold. It never writes live memory
// (D12): a worker proposes, the consolidator disposes. It returns "queued for
// consolidation", not "stored", because the memory is not recallable until a
// fold has weighed it.
type rememberTool struct {
	tool.Base
	store ToolStore
	now   func() time.Time
}

// NewRemember builds the remember tool over a memory store.
func NewRemember(store ToolStore) tool.Tool {
	return rememberTool{store: store, now: time.Now}
}

func (rememberTool) Name() string { return "remember" }

func (rememberTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "remember",
		Description: "Propose a memory for consolidation. It is queued, not stored; the next fold decides whether it becomes recallable.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"body": {"type": "string", "description": "The memory, one clear statement."},
				"importance": {"type": "integer", "description": "How much this matters, 1 (trivia) to 10 (load-bearing)."},
				"kind": {"type": "string", "enum": ["observation", "reflection"], "description": "observation is a fact; reflection is a lesson drawn from observations and needs evidence."},
				"anchors": {"type": "array", "description": "What the memory is about: at least one file, symbol, or command.", "items": {"type": "object", "properties": {"kind": {"type": "string", "enum": ["file", "symbol", "command"]}, "ref": {"type": "string"}, "hash": {"type": "string"}}, "required": ["kind", "ref"]}},
				"evidence": {"type": "array", "description": "For a reflection: ids of the memories it rests on.", "items": {"type": "string"}}
			},
			"required": ["body", "importance", "anchors"]
		}`),
	}
}

func (rememberTool) ValidateInput(_ context.Context, raw json.RawMessage, tc *tool.ToolContext) error {
	var a rememberArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if strings.TrimSpace(a.Body) == "" {
		return fmt.Errorf("body is required: say what to remember")
	}
	if a.Importance < 1 || a.Importance > maxImportance {
		return fmt.Errorf("importance must be 1 to %d, got %d", maxImportance, a.Importance)
	}
	if len(a.Anchors) == 0 {
		return fmt.Errorf("name at least one anchor: the file, symbol, or command this memory is about, so it can be found and invalidated")
	}
	for i, an := range a.Anchors {
		if an.Kind != "file" && an.Kind != "symbol" && an.Kind != "command" {
			return fmt.Errorf("anchor %d kind must be file, symbol, or command, got %q", i, an.Kind)
		}
		if strings.TrimSpace(an.Ref) == "" {
			return fmt.Errorf("anchor %d needs a ref", i)
		}
	}
	if kindOf(a.Kind) == sqlite.KindReflection && len(a.Evidence) == 0 {
		return fmt.Errorf("a reflection needs evidence: name the ids of the observations it rests on, or record it as an observation")
	}
	if tc.Namespace == "" {
		return fmt.Errorf("no memory namespace is bound to this session")
	}
	return nil
}

func (t rememberTool) Call(ctx context.Context, raw json.RawMessage, tc *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
	var a rememberArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	anchors := make([]sqlite.Anchor, len(a.Anchors))
	for i, an := range a.Anchors {
		anchors[i] = sqlite.Anchor{Kind: an.Kind, Ref: an.Ref, FileHash: an.Hash}
	}
	cand := sqlite.Candidate{
		Namespace:  tc.Namespace,
		Kind:       kindOf(a.Kind),
		Body:       strings.TrimSpace(a.Body),
		Importance: a.Importance,
		Anchors:    anchors,
		Evidence:   a.Evidence,
		Source:     sqlite.Source{Ant: string(tc.Ant)},
	}
	id := sqlite.NewID(t.clock())
	if err := t.store.InsertCandidate(ctx, id, cand); err != nil {
		return nil, fmt.Errorf("remember failed: %v", err)
	}
	return &tool.Result{
		Model: "queued for consolidation. It is not recallable until the next fold weighs it against what is already known.",
	}, nil
}

func (t rememberTool) clock() time.Time {
	if t.now != nil {
		return t.now()
	}
	return time.Now()
}

// --- recall ---

type recallArgs struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

// recallTool runs the hybrid search and returns the ranked rows, shaped for the
// model, not the screen. It is the retrieval and the pheromone reinforcement in
// one: recalling a row bumps its access stats so it stays fresh. It is read-only
// and concurrency-safe.
type recallTool struct {
	tool.Base
	store ToolStore
	emb   Embedder
}

// NewRecall builds the recall tool. The embedder is optional: with none, recall
// runs FTS-only, which is strong for the identifier-heavy queries a coding
// memory answers (D10).
func NewRecall(store ToolStore, emb Embedder) tool.Tool {
	return recallTool{store: store, emb: emb}
}

func (recallTool) Name() string { return "recall" }

func (recallTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "recall",
		Description: "Search the colony's memory for what bears on a query. Lean on the pinned index first; recall for what it does not carry.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"query": {"type": "string", "description": "What you are trying to remember, in a few words."},
				"limit": {"type": "integer", "description": "Most rows to return, default 8."}
			},
			"required": ["query"]
		}`),
	}
}

func (recallTool) IsReadOnly(json.RawMessage) bool        { return true }
func (recallTool) IsConcurrencySafe(json.RawMessage) bool { return true }

func (recallTool) ValidateInput(_ context.Context, raw json.RawMessage, tc *tool.ToolContext) error {
	var a recallArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return fmt.Errorf("query is required: say what to recall")
	}
	if tc.Namespace == "" {
		return fmt.Errorf("no memory namespace is bound to this session")
	}
	return nil
}

func (t recallTool) Call(ctx context.Context, raw json.RawMessage, tc *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
	var a recallArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	limit := a.Limit
	if limit <= 0 {
		limit = recallDefaultLimit
	}
	if limit > recallMaxLimit {
		limit = recallMaxLimit
	}

	var vec []float32
	if t.emb != nil && t.emb.Configured() {
		v, err := t.emb.Embed(ctx, a.Query)
		if err != nil {
			return nil, fmt.Errorf("recall could not embed the query: %v", err)
		}
		vec = v
	}

	rows, err := t.store.Recall(ctx, tc.Namespace, a.Query, vec, limit)
	if err != nil {
		return nil, fmt.Errorf("recall failed: %v", err)
	}
	return &tool.Result{Model: renderRecall(a.Query, rows)}, nil
}

// renderRecall shapes the ranked rows for the model: each row as its body, its
// anchors, and a one-word freshness marker, so the model can weigh a stale
// memory differently from a fresh one without a second tool call. The list is
// small on purpose: recall feeds the task tail, which the model pays full input
// price for, so a recall that dumps rows defeats the cheap pinned index.
func renderRecall(query string, rows []sqlite.Memory) string {
	if len(rows) == 0 {
		return fmt.Sprintf("No memory bears on %q.", query)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d memories bear on %q, most relevant first:\n", len(rows), query)
	for _, m := range rows {
		b.WriteString("\n- ")
		b.WriteString(strings.TrimSpace(m.Body))
		b.WriteString(" [")
		b.WriteString(freshness(m))
		b.WriteByte(']')
	}
	return b.String()
}

// freshness is the one-word marker recall tags each row with. A stale row was
// demoted by the invalidation pass because a file under it changed; a fresh row
// still holds. Archived rows never reach here, recall filters them out.
func freshness(m sqlite.Memory) string {
	if m.Stale {
		return "stale"
	}
	return "fresh"
}

// --- forget ---

type forgetArgs struct {
	ID string `json:"id"`
}

// forgetTool archives a row so it drops out of recall and the pinned index but
// stays in the file, because nothing in ari is ever deleted, retirement is
// archival (D11, D13). It is deliberately not destructive: an archive is
// reversible, so the permission renderer does not escalate it as an irreversible
// act, but it does render the row it would archive so the developer sees what
// leaves.
type forgetTool struct {
	tool.Base
	store ToolStore
}

// NewForget builds the forget tool over a memory store.
func NewForget(store ToolStore) tool.Tool {
	return forgetTool{store: store}
}

func (forgetTool) Name() string { return "forget" }

func (forgetTool) Schema() tool.Schema {
	return tool.Schema{
		Name:        "forget",
		Description: "Archive a memory by id so it leaves recall and the pinned index. It is not deleted; it stays in the file.",
		Params: json.RawMessage(`{
			"type": "object",
			"properties": {
				"id": {"type": "string", "description": "The id of the memory to archive, as recall reports it."}
			},
			"required": ["id"]
		}`),
	}
}

func (forgetTool) ValidateInput(_ context.Context, raw json.RawMessage, tc *tool.ToolContext) error {
	var a forgetArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return fmt.Errorf("arguments did not decode: %v", err)
	}
	if strings.TrimSpace(a.ID) == "" {
		return fmt.Errorf("id is required: name the memory to archive")
	}
	if tc.Namespace == "" {
		return fmt.Errorf("no memory namespace is bound to this session")
	}
	return nil
}

// CheckPermissions renders the consequence as the row it would archive, so
// archiving a memory is a visible act a developer approves, not a silent one. A
// row that does not exist defers to the pipeline; Call reports the miss.
func (t forgetTool) CheckPermissions(ctx context.Context, raw json.RawMessage, tc *tool.ToolContext) tool.PermissionResult {
	var a forgetArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tool.Passthrough()
	}
	label, found, err := t.store.MemoryLabel(ctx, tc.Namespace, a.ID)
	if err != nil || !found {
		return tool.Passthrough()
	}
	return tool.AskResult(fmt.Sprintf("forget will archive memory %s: %q. It leaves recall and the pinned index but stays in the file.", a.ID, label))
}

func (t forgetTool) Call(ctx context.Context, raw json.RawMessage, tc *tool.ToolContext, _ tool.ProgressFunc) (*tool.Result, error) {
	var a forgetArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	label, archived, err := t.store.ArchiveMemory(ctx, tc.Namespace, a.ID)
	if err != nil {
		return nil, fmt.Errorf("forget failed: %v", err)
	}
	if !archived {
		return nil, fmt.Errorf("no live memory %s in this namespace; nothing to archive", a.ID)
	}
	return &tool.Result{
		Model:   fmt.Sprintf("Archived memory %s: %q. It is out of recall and the pinned index, still in the file.", a.ID, label),
		Display: ForgetDisplay{ID: a.ID, Label: label},
	}, nil
}

// ForgetDisplay is the typed data the UI renders for a forget: the row that was
// archived, never sent to the model.
type ForgetDisplay struct {
	ID    string
	Label string
}

// kindOf maps the tool's kind string to the store's kind, defaulting to
// observation, so an omitted kind records a fact rather than refusing.
func kindOf(s string) sqlite.Kind {
	if s == "reflection" {
		return sqlite.KindReflection
	}
	return sqlite.KindObservation
}
