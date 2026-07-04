package colony

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

// The five handoff types are the only currency between ants. An ant that
// wants to communicate must reduce what it learned to one of these typed,
// bounded, auditable shapes; reducing is exactly the work that keeps a
// colony from drowning in each other's context (doc 09 section 3.2). There
// is no type here, and no function anywhere in the package, that carries one
// ant's transcript to another. An architecture test enforces that absence.

// Kind discriminates the five handoff types.
type Kind string

const (
	KindTaskBrief Kind = "task_brief"
	KindFinding   Kind = "finding"
	KindPatch     Kind = "patch"
	KindVerdict   Kind = "verdict"
	KindQuestion  Kind = "question"
)

// budgets is the hard per-kind token ceiling, enforced in Validate before a
// handoff can reach the board (doc 09 section 3.3). A brief that needs more
// than this should pass refs, not bodies; a finding that wants more is two
// findings; a diff that overflows spills to a file and the Patch carries the
// path.
var budgets = map[Kind]int{
	KindTaskBrief: 1000,
	KindFinding:   1200,
	KindPatch:     12000,
	KindVerdict:   400,
	KindQuestion:  300,
}

// Labels are the content-trust labels that ride every handoff (doc 09
// section 12, doc 14). Propagation is a monotonic union: once a worker's
// context held labeled content, every handoff it emits carries the label,
// and a Finding synthesized from one labeled source stays labeled.
type Labels []string

// Union merges two label sets into a sorted, deduplicated set, the monotonic
// join that makes a label impossible to drop by combining handoffs.
func (l Labels) Union(other Labels) Labels {
	seen := map[string]bool{}
	var out Labels
	for _, s := range append(append(Labels{}, l...), other...) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	slices.Sort(out)
	return out
}

// Header is embedded in every handoff: its identity, its kind, who sent it,
// which task graph node it belongs to, and its trust labels.
type Header struct {
	ID        string    `json:"id"`
	Kind      Kind      `json:"kind"`
	From      string    `json:"from"` // sending ant id, "queen" for the router
	TaskID    string    `json:"task_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Labels    Labels    `json:"labels,omitempty"`
	CreatedAt time.Time `json:"created_at,omitzero"`
}

// Hdr returns the embedded header, satisfying part of the Handoff interface.
func (h Header) Hdr() Header { return h }

// Handoff is what the board stores and the wire carries. Validate checks the
// required fields and the per-kind token budget; a handoff that fails it
// never reaches the board.
type Handoff interface {
	Hdr() Header
	Validate() error
}

// ContextRef points at context instead of inlining it, so a worker reads
// lazily with its own tools and the brief stays small.
type ContextRef struct {
	Path   string `json:"path,omitempty"`
	Lines  [2]int `json:"lines,omitempty"`
	Symbol string `json:"symbol,omitempty"`
	Memory string `json:"memory,omitempty"`
	Board  string `json:"board,omitempty"`
}

// Budget caps a task before it starts.
type Budget struct {
	Tokens    int           `json:"tokens"`
	Deadline  time.Duration `json:"deadline"`
	ToolCalls int           `json:"tool_calls"`
}

// TaskBrief tells a worker what to do without telling it how. It is the work
// order, written by the queen or by a worker decomposing within its ceiling.
// Intake (doc 06 section 3) adds the routing-facing fields to the same type:
// the origin and parent that place it in the task graph, the coarse class and
// the embedding the queen routes on, and the anchors three later steps read.
type TaskBrief struct {
	Header
	Goal        string       `json:"goal"`
	Context     []ContextRef `json:"context,omitempty"`
	Constraints []string     `json:"constraints,omitempty"`
	Deliverable Kind         `json:"deliverable"`
	DirectedTo  string       `json:"directed_to,omitempty"`
	Budget      Budget       `json:"budget"`
	DependsOn   []string     `json:"depends_on,omitempty"`

	Origin     Origin    `json:"origin,omitempty"`
	Parent     string    `json:"parent,omitempty"`
	Class      TaskClass `json:"class,omitempty"`
	Anchors    []Anchor  `json:"anchors,omitempty"`
	Embed      []float32 `json:"embed,omitempty"`
	EmbedModel string    `json:"embed_model,omitempty"`
	Deadline   time.Time `json:"deadline,omitzero"`
}

// Validate checks a brief carries a goal and a deliverable the colony
// produces, and that it fits the brief token budget.
func (b TaskBrief) Validate() error {
	if err := requireHeader(b.Header, KindTaskBrief); err != nil {
		return err
	}
	if b.Goal == "" {
		return fmt.Errorf("task_brief %s: a brief needs a goal", b.ID)
	}
	if b.Deliverable != KindFinding && b.Deliverable != KindPatch {
		return fmt.Errorf("task_brief %s: deliverable must be a finding or a patch, got %q", b.ID, b.Deliverable)
	}
	// The embedding is routing metadata the queen reads, not context the
	// worker reads, so it does not count against the brief's context budget:
	// a few hundred floats would blow the ceiling that bounds what crosses
	// between ants, and the worker never looks at it.
	wire := b
	wire.Embed = nil
	return checkBudget(wire)
}

// Citation anchors a claim to the repo at a moment in time.
type Citation struct {
	Path   string `json:"path"`
	Lines  [2]int `json:"lines,omitempty"`
	Commit string `json:"commit,omitempty"`
	Quote  string `json:"quote,omitempty"`
}

// Finding is a condensed, citation-carrying result: a survey answer, a
// triage verdict, a located bug.
type Finding struct {
	Header
	Summary    string     `json:"summary"`
	Evidence   []Citation `json:"evidence"`
	Confidence float64    `json:"confidence"`
	Leads      []string   `json:"leads,omitempty"`
}

// Validate demands a summary and at least one cited anchor, because a
// Finding without provenance is the same ungrounded claim the memory store
// refuses (D11), and every cited path must be named.
func (f Finding) Validate() error {
	if err := requireHeader(f.Header, KindFinding); err != nil {
		return err
	}
	if f.Summary == "" {
		return fmt.Errorf("finding %s: a finding needs a summary", f.ID)
	}
	if len(f.Evidence) == 0 {
		return fmt.Errorf("finding %s: a finding needs at least one citation; an unsourced claim is refused (D11)", f.ID)
	}
	for i, c := range f.Evidence {
		if c.Path == "" {
			return fmt.Errorf("finding %s: citation %d names no path", f.ID, i)
		}
	}
	return checkBudget(f)
}

// Patch is a proposed change produced in the worker's own worktree, never
// applied by the worker to the shared branch.
type Patch struct {
	Header
	Worktree string   `json:"worktree"`
	BaseRef  string   `json:"base_ref"`
	Diff     string   `json:"diff"`
	Tests    []string `json:"tests,omitempty"`
	Verified bool     `json:"verified"`
	Notes    string   `json:"notes,omitempty"`
}

// Validate demands the diff and the base commit it was cut from, so reconcile
// can apply and order it without re-reading the worker's tree.
func (p Patch) Validate() error {
	if err := requireHeader(p.Header, KindPatch); err != nil {
		return err
	}
	if p.Diff == "" {
		return fmt.Errorf("patch %s: a patch needs a diff", p.ID)
	}
	if p.BaseRef == "" {
		return fmt.Errorf("patch %s: a patch needs the base ref it was cut from", p.ID)
	}
	return checkBudget(p)
}

// Stakes says what a Verdict gates, which sizes the arbitration quorum when
// two Verdicts disagree (doc 09 section 7).
type Stakes string

const (
	StakesLow    Stakes = "low"
	StakesNormal Stakes = "normal"
	StakesHigh   Stakes = "high"
)

// Verdict judges a Finding or a Patch. It is what turns a worker's "done"
// into something the colony can trust.
type Verdict struct {
	Header
	Subject string   `json:"subject"`
	Pass    bool     `json:"pass"`
	Reasons []string `json:"reasons"`
	Stakes  Stakes   `json:"stakes"`
}

// Validate demands the subject handoff id being judged and a reason for the
// judgment.
func (v Verdict) Validate() error {
	if err := requireHeader(v.Header, KindVerdict); err != nil {
		return err
	}
	if v.Subject == "" {
		return fmt.Errorf("verdict %s: a verdict must name the handoff it judges", v.ID)
	}
	if len(v.Reasons) == 0 {
		return fmt.Errorf("verdict %s: a verdict needs at least one reason", v.ID)
	}
	return checkBudget(v)
}

// Question is the stuck signal: a worker that cannot proceed posts one
// instead of guessing and instead of prompting the user, which it is
// forbidden to do (slice 15). The answer flows back as a Finding, so no
// sixth type is needed.
type Question struct {
	Header
	Ask      string   `json:"ask"`
	Options  []string `json:"options,omitempty"`
	Blocking bool     `json:"blocking"`
}

// Validate demands the question itself.
func (q Question) Validate() error {
	if err := requireHeader(q.Header, KindQuestion); err != nil {
		return err
	}
	if q.Ask == "" {
		return fmt.Errorf("question %s: a question needs something to ask", q.ID)
	}
	return checkBudget(q)
}

// requireHeader checks the fields every handoff shares: an id, and a kind
// that matches the concrete type.
func requireHeader(h Header, want Kind) error {
	if h.ID == "" {
		return fmt.Errorf("%s: a handoff needs an id", want)
	}
	if h.Kind != want {
		return fmt.Errorf("handoff %s: kind is %q, want %q", h.ID, h.Kind, want)
	}
	return nil
}

// checkBudget enforces the per-kind token ceiling over the JSON-encoded
// handoff, the same conservative four-bytes-per-token estimate the loop uses
// so the budget is enforced with margin rather than false precision.
func checkBudget(h Handoff) error {
	kind := h.Hdr().Kind
	limit, ok := budgets[kind]
	if !ok {
		return fmt.Errorf("handoff %s: unknown kind %q has no budget", h.Hdr().ID, kind)
	}
	if n := handoffTokens(h); n > limit {
		return fmt.Errorf("%s %s: %d tokens over the %d budget; pass refs, not bodies", kind, h.Hdr().ID, n, limit)
	}
	return nil
}

// handoffTokens estimates the token size of a handoff's JSON encoding.
func handoffTokens(h Handoff) int {
	b, err := json.Marshal(h)
	if err != nil {
		return 0
	}
	return (len(b) + 3) / 4
}
