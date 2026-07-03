// Package agent is the engine that turns one awake ant into work: the
// explicit state-machine loop that drives model turns, schedules tool
// calls, compacts the context when the window fills, and recovers from
// provider errors (doc 03).
//
// The loop is one for-loop over one State struct, and every retry,
// compaction, recovery, and fallback is a named transition that
// reassigns the state and continues the loop, never a recursion (D6).
// Given the same model responses and the same tool outputs, the same
// transitions fire in the same order, so a recorded session replays
// byte-identically for the eval harness (D23).
package agent

import "time"

// TermReason is the single typed reason a loop run ends with. Every Run
// returns exactly one; there is no untyped exit (doc 03 section 2).
type TermReason string

const (
	// TermCompleted is the model finishing with no tool calls. The only
	// success reason.
	TermCompleted TermReason = "completed"

	// TermMaxTurns is the per-run turn ceiling. Foreground runs set a
	// high one; worker ants get a tight one from their card (M3).
	TermMaxTurns TermReason = "max_turns"

	// TermBudgetExhausted is the ledger refusing another model call
	// because the budget is spent (D5). A between-turn gate, never a
	// mid-turn kill.
	TermBudgetExhausted TermReason = "budget_exhausted"

	// TermCanceled is cancellation arriving between turns: nothing was
	// running, nothing was lost.
	TermCanceled TermReason = "canceled"

	// TermToolsCanceled is cancellation arriving while tools were in
	// flight; kept distinct so the transcript can show which results
	// landed and which were abandoned.
	TermToolsCanceled TermReason = "tools_canceled"

	// TermPromptTooLong is a prompt the full compaction ladder could not
	// shrink under the blocking limit.
	TermPromptTooLong TermReason = "prompt_too_long"

	// TermModelError is an unrecoverable provider error after retries
	// and fallback were exhausted.
	TermModelError TermReason = "model_error"

	// TermCompactionFailed is the circuit breaker opening after
	// consecutive compaction failures. Separate from TermPromptTooLong
	// so the journal shows the breaker, not the symptom.
	TermCompactionFailed TermReason = "compaction_failed"
)

// transition names one step of the loop. Handlers set State.next to the
// transition that should run after them; the for-loop dispatches on it.
// This is the whole control graph, a closed set on purpose (D6).
type transition int

const (
	transStart         transition = iota // one-time setup, then assemble
	transAssemble                        // gates: turns, budget, compaction need
	transCallModel                       // stream one assistant response
	transRunTools                        // schedule and execute the turn's calls
	transDrainQueue                      // fold queued user prompts in
	transStopHooks                       // stop hooks when the model wants to finish
	transCompact                         // the compaction ladder, cheapest rung first
	transRetryModel                      // re-issue after a transient error
	transRecoverOutput                   // recover from a max-output truncation
	transFallbackModel                   // switch to the fallback model and retry
	transTerminate                       // produce the terminal reason and return
)

// Recovery ceilings and compaction constants (doc 03 sections 11 and
// 12). The compaction numbers are the Claude Code values from research
// A.7, adopted because they were tuned against real traffic.
const (
	maxModelRetries           = 4      // transient retries before fallback
	maxOutputRetries          = 3      // truncation recoveries before giving up
	maxConsecutiveCompactFail = 3      // circuit breaker threshold
	escalatedMaxOutput        = 64_000 // output cap after the first truncation

	reservedOutputCap = 20_000 // output reservation ceiling
	autocompactBuffer = 13_000 // auto-compaction fires this early
	blockingReserve   = 3_000  // sliver kept below the hard limit

	maxRestoreFiles         = 5      // working-set restore after summarize
	maxRestoreTokensPerFile = 5_000  //
	maxRestoreTokensTotal   = 50_000 //
)

// Limits is the per-run configuration: ceilings, budgets, and the
// injected clock and sleep for deterministic tests.
type Limits struct {
	// MaxTurns is the model-turn ceiling. Zero means the foreground
	// default of 100.
	MaxTurns int

	// MaxToolConcurrency bounds a parallel batch. Zero means 10, the
	// Claude Code default (doc 03 section 7).
	MaxToolConcurrency int

	// Window is the model's context size in tokens. Zero means 200000.
	Window int

	// MaxOut is the output-token cap sent with each request. Zero lets
	// the provider default stand.
	MaxOut int

	// KeepToolResults is how many recent tool results rung one of the
	// compaction ladder preserves. Zero means 5 (doc 03 section 18
	// leaves the exact count to tuning; this is the seam).
	KeepToolResults int

	// AutoCompactAt and BlockingAt override the derived thresholds when
	// nonzero, so a test triggers compaction without a huge transcript.
	AutoCompactAt int
	BlockingAt    int

	// Sleep is the retry backoff sleeper. Nil means time.Sleep; tests
	// inject a recorder.
	Sleep func(time.Duration)
}

func (l Limits) maxTurns() int {
	if l.MaxTurns > 0 {
		return l.MaxTurns
	}
	return 100
}

func (l Limits) concurrency() int {
	if l.MaxToolConcurrency > 0 {
		return l.MaxToolConcurrency
	}
	return 10
}

func (l Limits) keepToolResults() int {
	if l.KeepToolResults > 0 {
		return l.KeepToolResults
	}
	return 5
}

func (l Limits) sleep(d time.Duration) {
	if l.Sleep != nil {
		l.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Thresholds are the absolute-token trigger points, derived from the
// window, never percentages: a fixed buffer in tokens is what actually
// protects the output budget (doc 03 section 11).
type Thresholds struct {
	AutoCompact int // auto-compaction fires when live tokens cross this
	Blocking    int // crossing this with recovery spent is TermPromptTooLong
}

func (l Limits) thresholds() Thresholds {
	window := l.Window
	if window == 0 {
		window = 200_000
	}
	effective := window - reservedOutputCap
	t := Thresholds{
		AutoCompact: effective - autocompactBuffer,
		Blocking:    effective - blockingReserve,
	}
	if l.AutoCompactAt > 0 {
		t.AutoCompact = l.AutoCompactAt
	}
	if l.BlockingAt > 0 {
		t.Blocking = l.BlockingAt
	}
	return t
}

// backoff is the capped exponential wait before a transient retry.
// Deliberately jitter-free so replays are deterministic; doc 10 owns
// the production schedule and may replace this (doc 03 section 18).
func backoff(attempt int) time.Duration {
	d := 250 * time.Millisecond << (attempt - 1)
	if d > 4*time.Second {
		d = 4 * time.Second
	}
	return d
}

// Outcome is what a run hands back: the terminal reason and how many
// model turns it took. Tokens live in the ledger, not here.
type Outcome struct {
	Reason TermReason
	Turns  int
}
