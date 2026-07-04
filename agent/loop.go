package agent

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
	"github.com/tamnd/ari/version"
)

// Verdict is the permission seam's answer for one tool call. The ant
// (slice 9) adapts the doc 05 pipeline to this; the loop only needs to
// know whether to run the call and with what input.
type Verdict struct {
	Allow        bool
	UpdatedInput json.RawMessage // non-nil replaces the call's input
	Reason       string          // model-facing sentence when not allowed
}

// DecideFunc is the permission pipeline seam. Nil means every call is
// allowed, which only the tests use; the ant always wires the pipeline.
type DecideFunc func(ctx context.Context, t tool.Tool, input json.RawMessage, callID string) Verdict

// Hooks is the loop's seam to the tool-adjacent hooks. The ant wires it to
// the trusted hook dispatcher; nil means no hooks, which is the case for an
// untrusted workspace and for tests. The loop stays free of the hook package:
// it knows only these two calls and the small result they return.
type Hooks interface {
	// PreTool runs the pre-tool hooks before the permission decision. A
	// blocked result stops the call and its Message is fed back to the model.
	PreTool(ctx context.Context, tool string, input json.RawMessage) HookResult
	// PostTool runs the post-tool hooks after the tool returns. A blocked
	// result feeds Message back to the model; otherwise Context is appended to
	// the tool result. isErr routes to the post-tool-failure event.
	PostTool(ctx context.Context, tool string, input json.RawMessage, result string, isErr bool) HookResult
}

// HookResult is the loop-facing outcome of a hook fire.
type HookResult struct {
	Block   bool   // a hook blocked (exit 2)
	Message string // block or warning text, fed to the model on a block
	Context string // additional context to append to the result
}

// Loop drives one ant through model turns to a terminal reason. The
// fields are the run's fixed dependencies; everything that changes
// across iterations lives in State.
type Loop struct {
	Provider provider.Provider
	Model    string // resolved model id
	Fallback string // fallback model id, "" means none
	System   []provider.Block

	// Prefix is block two of the cache-aligned prompt (D14): the pinned
	// index and project memory as one synthetic system-reminder user
	// message, prepended before the task tail on every request. The ant
	// builds it once per session so it stays byte-stable across turns;
	// its last block carries the second cache breakpoint.
	Prefix []provider.Message

	Tools  *tool.Registry
	TC     *tool.ToolContext
	Decide DecideFunc

	// Hooks is the tool-adjacent hook seam. Nil means no hooks, which is the
	// case for an untrusted workspace and for tests; the ant wires it to the
	// trusted dispatcher only when a workspace has hooks that pass the trust
	// gate (doc 05 section 12).
	Hooks Hooks

	// Emit publishes one event on the stream; the colony's TurnHandle
	// satisfies it. Nil drops events, for isolated transition tests.
	Emit func(t event.Type, payload any) error

	// Append writes one transcript entry. Nil drops them.
	Append func(e session.Entry) error

	// Record accounts one completed model turn. Nil skips the meter.
	Record func(r ledger.Row)

	// OverBudget is the between-turn budget gate (D5). Nil means no
	// budget. Checked after each ledger flush, never mid-turn.
	OverBudget func() bool

	Session string
	Turn    string // the user turn id events are stamped with
	Tier    string // for the ledger row
	Limits  Limits

	// Now is injected for deterministic tests. Nil means time.Now.
	Now func() time.Time

	mu sync.Mutex
	st *State // published while Run is active, for Submit
}

// State is the entire cross-iteration state of one run. Handlers mutate
// it in place and set next; nothing important lives in a handler-local
// variable, because a local is invisible to the journal and dies at the
// continue (doc 03 section 4).
type State struct {
	// the conversation
	msgs        []provider.Message
	boundaryIdx int // model only ever sees msgs[boundaryIdx:]

	// the current turn in flight
	next       transition
	turn       int // completed model turns this run
	part       int // event part counter, monotonic across the run
	model      string
	maxOut     int
	toolCalls  []provider.ToolCall
	turnUsage  provider.Usage
	stopReason string
	turnStart  time.Time

	// queued human input, drained after tool results (doc 03 section 9)
	queue []string

	// recovery guards and counters, the spiral prevention
	compactions       int
	compactedThisTurn bool
	compactTrigger    string // "auto" or "reactive", for the session boundary entry
	consecCompactFail int
	outputRetries     int
	modelRetries      int
	fellBack          bool
	reactiveCompacted bool

	// pendingErr parks a recoverable provider error while the loop
	// decides whether a transition can fix it; it is surfaced only when
	// nothing can (doc 03 section 13).
	pendingErr *provider.Error

	// recentReads orders paths for the post-compaction working-set
	// restore, most recent last.
	recentReads []string

	term TermReason
}

func (l *Loop) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Loop) emit(t event.Type, payload any) {
	if l.Emit != nil {
		_ = l.Emit(t, payload)
	}
}

func (l *Loop) appendEntry(t session.EntryType, body any) {
	if l.Append == nil {
		return
	}
	b, err := json.Marshal(body)
	if err != nil {
		return
	}
	_ = l.Append(session.Entry{
		ID:      session.NewID(),
		Type:    t,
		Time:    l.now().UTC(),
		Body:    b,
		Session: session.ID(l.Session),
		Version: version.Version,
	})
}

// AntBody is the session body of one assistant message.
type AntBody struct {
	Text     string              `json:"text,omitempty"`
	Thinking string              `json:"thinking,omitempty"`
	Calls    []provider.ToolCall `json:"calls,omitempty"`
}

// ToolBody is the session body of one tool result.
type ToolBody struct {
	Call    string `json:"call"`
	Tool    string `json:"tool"`
	Content string `json:"content"`
	IsErr   bool   `json:"is_err,omitempty"`
}

// CompactBody is the session body of a compaction boundary (D9).
type CompactBody struct {
	Trigger    string `json:"trigger"`
	PreTokens  int    `json:"pre_tokens"`
	Summarized int    `json:"summarized"`
}

// Submit queues a human prompt to be folded in at the next drain point.
// Safe to call while a turn runs; the prompt joins the transcript after
// the current turn's tool results, never in the middle of them.
func (l *Loop) Submit(text string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.st != nil {
		l.st.queue = append(l.st.queue, text)
	}
}

// Run drives the loop from one user prompt to a terminal reason. It
// never recurses and never returns without a TermReason (D6). A non-nil
// error is a genuine bug, never a turn that ended badly; every model
// and tool failure is a reason, not an error (doc 03 section 13).
//
// The conversation survives Run: a second Run on the same Loop appends
// its prompt to the transcript the first one built, because the ant owns
// one context window for the whole session (doc 01 section 2.2). Per-run
// counters and recovery guards reset; msgs, the boundary, the part
// counter, and the compaction count carry over.
func (l *Loop) Run(ctx context.Context, prompt string) (Outcome, error) {
	l.mu.Lock()
	st := l.st
	if st == nil {
		st = &State{}
		l.st = st
	}
	st.next = transStart
	st.model = l.Model
	st.maxOut = l.Limits.MaxOut
	st.term = ""
	st.stopReason = ""
	st.toolCalls = nil
	st.pendingErr = nil
	st.modelRetries, st.outputRetries = 0, 0
	st.fellBack, st.reactiveCompacted, st.compactedThisTurn = false, false, false
	st.msgs = append(st.msgs, provider.Message{
		Role:   "user",
		Blocks: []provider.MsgBlock{{Kind: "text", Text: prompt}},
	})
	l.mu.Unlock()

	for {
		// Cooperative cancellation, checked once per iteration before
		// any expensive step. In-turn cancellation is handled inside the
		// model and tool handlers via ctx (doc 03 section 10).
		if err := ctx.Err(); err != nil && st.next != transTerminate {
			st.setCanceled()
			st.next = transTerminate
		}

		switch st.next {
		case transStart:
			l.start(st)
		case transAssemble:
			l.assemble(st)
		case transCallModel:
			l.callModel(ctx, st)
		case transRunTools:
			l.runTools(ctx, st)
		case transDrainQueue:
			l.drainQueue(st)
		case transStopHooks:
			l.stopHooks(st)
		case transCompact:
			l.compact(ctx, st)
		case transRetryModel:
			l.retryModel(st)
		case transRecoverOutput:
			l.recoverOutput(st)
		case transFallbackModel:
			l.fallbackModel(st)
		case transTerminate:
			return l.finish(st), nil
		}
	}
}

func (l *Loop) start(st *State) {
	st.turn = 0
	if l.TC != nil && l.TC.Files == nil {
		l.TC.Files = tool.NewFileState()
	}
	st.next = transAssemble
}

// assemble is the between-turn gate: the turn ceiling and the proactive
// compaction check run here, before any money is spent.
func (l *Loop) assemble(st *State) {
	if st.turn >= l.Limits.maxTurns() {
		st.term = TermMaxTurns
		st.next = transTerminate
		return
	}
	if l.liveTokens(st) >= l.Limits.thresholds().AutoCompact && !st.compactedThisTurn {
		st.next = transCompact
		return
	}
	st.next = transCallModel
}

// setCanceled distinguishes tools-in-flight from a clean between-turn
// cancel, so the transcript is honest about what was abandoned.
func (st *State) setCanceled() {
	if st.next == transRunTools {
		st.term = TermToolsCanceled
		return
	}
	st.term = TermCanceled
}

// stopHooks is where Stop hooks will run when the model wants to
// finish (their milestone); in M0 the model finishing is the run
// finishing.
func (l *Loop) stopHooks(st *State) {
	st.term = TermCompleted
	st.next = transTerminate
}

// drainQueue folds queued human prompts into the transcript in
// submission order, at the one well-defined point determinism allows:
// after tool results, before the next assembly (doc 03 section 9).
func (l *Loop) drainQueue(st *State) {
	l.mu.Lock()
	queued := st.queue
	st.queue = nil
	l.mu.Unlock()

	for _, text := range queued {
		st.msgs = append(st.msgs, provider.Message{
			Role:   "user",
			Blocks: []provider.MsgBlock{{Kind: "text", Text: text}},
		})
		l.appendEntry(session.EntryUser, map[string]string{"text": text})
		l.emit(event.TypeLog, event.Log{Level: "debug", Text: "queued prompt drained into the transcript"})
	}
	st.next = transAssemble
}

// buildRequest renders the three-block cache-aligned prompt (D14):
// system and tools are block one and render identically every turn, the
// prefix is block two and changes only at folding boundaries, and the
// task tail is the only per-turn variance.
func (l *Loop) buildRequest(st *State) provider.Request {
	tail := st.msgs[st.boundaryIdx:]
	msgs := make([]provider.Message, 0, len(l.Prefix)+len(tail))
	msgs = append(msgs, l.Prefix...)
	msgs = append(msgs, tail...)
	return provider.Request{
		Model:    st.model,
		System:   l.System,
		Tools:    l.toolDefs(),
		Messages: msgs,
		MaxOut:   st.maxOut,
		Meta: provider.RequestMeta{
			Ant:     "worker",
			Session: l.Session,
			Tier:    l.Tier,
		},
	}
}

func (l *Loop) toolDefs() []provider.ToolDef {
	if l.Tools == nil {
		return nil
	}
	names := l.Tools.Names()
	defs := make([]provider.ToolDef, 0, len(names))
	for _, name := range names {
		t, ok := l.Tools.Resolve(name)
		if !ok {
			continue
		}
		s := t.Schema()
		var params map[string]any
		_ = json.Unmarshal(s.Params, &params)
		defs = append(defs, provider.ToolDef{Name: s.Name, Description: s.Description, Schema: params})
	}
	if len(defs) > 0 {
		defs[len(defs)-1].Cache = true // breakpoint 1 lands after the tools (D14)
	}
	return defs
}

// liveTokens estimates the tokens the next request will carry: the
// visible tail plus the stable prefix. An estimate is enough because
// the thresholds carry thousands of tokens of buffer by construction.
func (l *Loop) liveTokens(st *State) int {
	n := 0
	for _, b := range l.System {
		n += estimateTokens(b.Text)
	}
	for _, m := range l.Prefix {
		for _, b := range m.Blocks {
			n += estimateTokens(b.Text)
		}
	}
	for _, m := range st.msgs[st.boundaryIdx:] {
		for _, b := range m.Blocks {
			n += estimateTokens(b.Text)
			if b.Call != nil {
				n += estimateTokens(b.Call.Input)
			}
		}
	}
	return n
}

func estimateTokens(s string) int {
	return (len(s) + 3) / 4
}

// streamSink folds provider deltas into a typed draft while publishing
// them live: what the UI renders and what the transcript stores project
// from the same accumulation, so they can never diverge (doc 03
// section 6).
type streamSink struct {
	l         *Loop
	st        *State
	textPart  int
	thinkPart int
	text      strings.Builder
	thinking  strings.Builder
	calls     []provider.ToolCall
	started   time.Time
}

func (s *streamSink) OnText(delta string) {
	if s.textPart < 0 {
		s.textPart = s.st.part
		s.st.part++
	}
	s.text.WriteString(delta)
	s.l.emit(event.TypeTextDelta, event.TextDelta{Part: s.textPart, Text: delta})
}

func (s *streamSink) OnThinking(delta string) {
	if s.thinkPart < 0 {
		s.thinkPart = s.st.part
		s.st.part++
	}
	s.thinking.WriteString(delta)
	s.l.emit(event.TypeThinkingDelta, event.ThinkingDelta{Part: s.thinkPart, Text: delta})
}

func (s *streamSink) OnToolCall(call provider.ToolCall) {
	s.calls = append(s.calls, call)
}

func (s *streamSink) OnUsage(u provider.Usage) {
	s.st.turnUsage = u
}

// close emits the part-end events for whatever streamed.
func (s *streamSink) close() {
	if s.thinkPart >= 0 {
		s.l.emit(event.TypeThinkingEnd, event.ThinkingEnd{
			Part:       s.thinkPart,
			DurationMS: s.l.now().Sub(s.started).Milliseconds(),
		})
	}
	if s.textPart >= 0 {
		s.l.emit(event.TypeTextEnd, event.TextEnd{Part: s.textPart})
	}
}

// finalize appends the assistant message to the live transcript and the
// session. Thinking rides the session entry but never re-enters the
// model's context, which is also why compaction rung two is a no-op in
// M0 (doc 03 section 11).
func (s *streamSink) finalize(st *State) {
	var blocks []provider.MsgBlock
	if s.text.Len() > 0 {
		blocks = append(blocks, provider.MsgBlock{Kind: "text", Text: s.text.String()})
	}
	for i := range s.calls {
		blocks = append(blocks, provider.MsgBlock{Kind: "tool_call", Call: &s.calls[i]})
	}
	st.msgs = append(st.msgs, provider.Message{Role: "assistant", Blocks: blocks})
	s.l.appendEntry(session.EntryAnt, AntBody{
		Text:     s.text.String(),
		Thinking: s.thinking.String(),
		Calls:    s.calls,
	})
}

func (l *Loop) callModel(ctx context.Context, st *State) {
	st.turnStart = l.now()
	req := l.buildRequest(st)
	sink := &streamSink{l: l, st: st, textPart: -1, thinkPart: -1, started: st.turnStart}

	res, err := l.Provider.Stream(ctx, req, sink)
	sink.close()
	if err != nil {
		if ctx.Err() != nil {
			st.setCanceled()
			st.next = transTerminate
			return
		}
		l.classifyModelError(st, err)
		return
	}

	st.stopReason = res.StopReason
	if res.StopReason == "max_tokens" {
		// A truncated turn. The first recovery silently re-issues with a
		// bigger cap, so the partial draft is dropped rather than
		// persisted twice; later recoveries keep the partial and steer
		// the model to resume (doc 03 section 12).
		if st.outputRetries > 0 {
			sink.finalize(st)
		}
		st.next = transRecoverOutput
		return
	}

	sink.finalize(st)
	st.modelRetries = 0
	st.pendingErr = nil
	st.compactedThisTurn = false
	st.toolCalls = sink.calls
	if len(st.toolCalls) == 0 {
		// The tool-less final turn is still a metered turn.
		st.turn++
		l.flushLedger(st)
		if st.term != "" {
			st.next = transTerminate
			return
		}
		st.next = transStopHooks
		return
	}
	st.next = transRunTools
}

// flushLedger records one completed model turn and applies the
// between-turn budget gate (D5): a spent budget refuses the next turn,
// never kills the one in flight.
func (l *Loop) flushLedger(st *State) {
	if l.Record != nil {
		l.Record(ledger.Row{
			Ant:        "worker",
			Session:    l.Session,
			Turn:       l.Turn,
			Provider:   l.Provider.Name(),
			Model:      st.model,
			Tier:       l.Tier,
			Usage:      st.turnUsage,
			Wall:       l.now().Sub(st.turnStart),
			Estimated:  st.turnUsage.Estimated,
			StopReason: st.stopReason,
		})
	}
	if l.OverBudget != nil && l.OverBudget() {
		st.term = TermBudgetExhausted
	}
}

// finish produces the terminal reason. Recoverable errors were resolved
// silently along the way; only a reason that means failure publishes an
// error event, exactly once (doc 03 section 13).
func (l *Loop) finish(st *State) Outcome {
	switch st.term {
	case TermModelError, TermPromptTooLong, TermCompactionFailed:
		info := event.ErrorInfo{
			Code:    "model",
			Message: userFacingError(st.term),
			Ant:     "worker",
		}
		if st.pendingErr != nil {
			info.Cause = st.pendingErr.Message
			info.Retryable = st.pendingErr.Retryable()
		}
		l.emit(event.TypeError, info)
	case TermBudgetExhausted:
		l.emit(event.TypeError, event.ErrorInfo{
			Code:    "budget",
			Message: "the token budget for this session is spent; raise it or start a new session",
			Ant:     "worker",
		})
	}
	return Outcome{Reason: st.term, Turns: st.turn}
}

func userFacingError(term TermReason) string {
	switch term {
	case TermPromptTooLong:
		return "the conversation no longer fits the model's context window, even after compaction"
	case TermCompactionFailed:
		return "compaction failed repeatedly, so the run was stopped instead of retrying forever"
	default:
		return "the model provider failed and retries were exhausted"
	}
}
