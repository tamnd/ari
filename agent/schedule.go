package agent

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
)

// toolOutcome is one finished tool call: what the model will read, what
// the UI showed, and what side effects the loop must fold in.
type toolOutcome struct {
	call    provider.ToolCall
	part    int
	content string // model-facing tool_result text
	isErr   bool
	display string
	spilled string
	effect  *tool.FileStateEffect
	// hardFail is an infrastructure failure (spill store down), the one
	// class that cancels siblings; a tool error the model can correct
	// never does (doc 03 section 7).
	hardFail bool
	canceled bool
}

// batch is one scheduling unit: either a run of consecutive
// concurrency-safe calls that may run in parallel, or a single unsafe
// call that runs alone.
type batch struct {
	calls    []int // indices into the turn's call list
	parallel bool
}

// partition groups the turn's calls into batches while preserving their
// receive order: consecutive safe calls coalesce, an unsafe call cuts a
// batch of its own. Order across batches is exactly model order, so an
// unsafe call acts as a barrier (doc 03 section 7).
func partition(reg *tool.Registry, calls []provider.ToolCall) []batch {
	var out []batch
	for i, c := range calls {
		if safeConcurrent(reg, c) && len(out) > 0 && out[len(out)-1].parallel {
			last := &out[len(out)-1]
			last.calls = append(last.calls, i)
			continue
		}
		out = append(out, batch{calls: []int{i}, parallel: safeConcurrent(reg, c)})
	}
	return out
}

// safeConcurrent answers "may this call share a batch", failing closed:
// an unknown tool, a broken input, or a panicking classifier all mean
// no (D7).
func safeConcurrent(reg *tool.Registry, c provider.ToolCall) (safe bool) {
	defer func() {
		if recover() != nil {
			safe = false
		}
	}()
	if reg == nil {
		return false
	}
	t, ok := reg.Resolve(c.Name)
	if !ok {
		return false
	}
	return t.IsConcurrencySafe(json.RawMessage(c.Input))
}

// runTools executes the turn's calls batch by batch and folds the
// results into one user message, in receive order, never completion
// order: the transcript's determinism outranks any UI nicety (doc 03
// section 7).
func (l *Loop) runTools(ctx context.Context, st *State) {
	results := make([]toolOutcome, len(st.toolCalls))

	// ToolStart events go out in receive order before anything runs, so
	// the UI shows the model's plan even when execution interleaves.
	for i, c := range st.toolCalls {
		results[i].call = c
		results[i].part = st.part
		st.part++
		l.emit(event.TypeToolStart, event.ToolStart{
			Part:  results[i].part,
			Call:  c.ID,
			Tool:  c.Name,
			Input: c.Input,
		})
	}

	hardFailed := false
	for _, b := range partition(l.Tools, st.toolCalls) {
		if hardFailed || ctx.Err() != nil {
			for _, i := range b.calls {
				results[i].canceled = true
				results[i].isErr = true
				results[i].content = "canceled before it ran"
				results[i].display = "canceled"
			}
			continue
		}
		if b.parallel && len(b.calls) > 1 {
			l.runParallel(ctx, b.calls, results)
		} else {
			l.runOne(ctx, b.calls[0], results, true)
		}
		for _, i := range b.calls {
			if results[i].hardFail {
				hardFailed = true
			}
		}
		// Effects apply in receive order after the batch settles, so the
		// file-state map is deterministic no matter how the batch raced.
		for _, i := range b.calls {
			l.applyEffect(st, results[i].effect)
		}
	}

	// One user message carries every tool_result, in receive order.
	blocks := make([]provider.MsgBlock, 0, len(results))
	for i := range results {
		r := &results[i]
		l.emit(event.TypeToolEnd, event.ToolEnd{
			Part:    r.part,
			Call:    r.call.ID,
			Tool:    r.call.Name,
			OK:      !r.isErr,
			Display: r.display,
			Spilled: r.spilled,
		})
		l.appendEntry(session.EntryTool, ToolBody{
			Call:    r.call.ID,
			Tool:    r.call.Name,
			Content: r.content,
			IsErr:   r.isErr,
		})
		blocks = append(blocks, provider.MsgBlock{
			Kind:   "tool_result",
			CallID: r.call.ID,
			Text:   r.content,
			IsErr:  r.isErr,
		})
	}
	st.msgs = append(st.msgs, provider.Message{Role: "user", Blocks: blocks})
	st.toolCalls = nil
	st.turn++
	l.flushLedger(st)

	switch {
	case st.term == TermBudgetExhausted:
		st.next = transTerminate
	case ctx.Err() != nil:
		st.term = TermToolsCanceled
		st.next = transTerminate
	default:
		st.next = transDrainQueue
	}
}

// runParallel runs one safe batch under the bounded pool. Results land
// in their positional slots, so no ordering is ever reconstructed.
func (l *Loop) runParallel(ctx context.Context, idxs []int, results []toolOutcome) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, l.Limits.concurrency())
	done := make(chan int)
	for _, i := range idxs {
		go func(i int) {
			sem <- struct{}{}
			defer func() { <-sem }()
			l.runOne(batchCtx, i, results, false)
			if results[i].hardFail {
				cancel() // siblings stop; their slots read canceled
			}
			done <- i
		}(i)
	}
	for range idxs {
		<-done
	}
}

// runOne executes a single call end to end: resolve, validate, decide,
// run, budget. Every failure before Call becomes a model-facing
// tool_result, because the model can correct its own mistakes but not
// a silent drop (doc 03 section 7). Only the serial path streams
// progress: interleaved progress from a parallel batch would be noise.
func (l *Loop) runOne(ctx context.Context, i int, results []toolOutcome, serial bool) {
	r := &results[i]
	c := r.call
	fail := func(msg string) {
		r.isErr = true
		r.content = msg
		r.display = msg
	}
	// A panicking tool becomes a model-facing error, never a dead pool
	// slot or a crashed loop (D7).
	defer func() {
		if p := recover(); p != nil {
			fail(fmt.Sprintf("tool panicked: %v", p))
		}
	}()

	if err := ctx.Err(); err != nil {
		r.canceled = true
		fail("canceled before it ran")
		return
	}

	t, ok := l.Tools.Resolve(c.Name)
	if !ok {
		fail(fmt.Sprintf("unknown tool %q; use one of the tools you were given", c.Name))
		return
	}
	args := json.RawMessage(c.Input)

	if err := t.ValidateInput(ctx, args, l.TC); err != nil {
		fail(fmt.Sprintf("invalid input: %v", err))
		return
	}

	// preContext holds any note a PreToolUse hook contributed through the
	// permission decision; it is surfaced with the tool result below.
	var preContext string
	if l.Decide != nil {
		v := l.Decide(ctx, t, args, c.ID)
		if !v.Allow {
			reason := v.Reason
			if reason == "" {
				reason = "permission denied"
			}
			fail(reason + "; do not retry the same call, adjust or ask the user")
			return
		}
		if v.UpdatedInput != nil {
			args = v.UpdatedInput
		}
		preContext = v.Context
	}

	var onProgress tool.ProgressFunc
	if serial {
		onProgress = func(chunk string) {
			l.emit(event.TypeToolProgress, event.ToolProgress{
				Part: r.part,
				Call: c.ID,
				Text: chunk,
			})
		}
	}

	res, err := t.Call(ctx, args, l.TC, onProgress)
	if err != nil {
		if ctx.Err() != nil {
			r.canceled = true
			fail("canceled while running")
			return
		}
		// A tool error is model-correctable feedback, not a loop failure.
		fail(err.Error())
		return
	}

	content, ref, err := tool.ApplyResultBudget(res, t, l.TC)
	if err != nil {
		// The spill store failing is infrastructure, not the model's
		// mistake: hard-fail and cancel the siblings.
		r.hardFail = true
		fail(fmt.Sprintf("result too large and spill failed: %v", err))
		return
	}
	r.content = content
	r.spilled = ref.Path
	r.effect = res.StateEffect
	r.display = displayString(res, content)

	if preContext != "" {
		r.content += "\n\n" + preContext
		content = r.content
	}

	if l.Hooks != nil {
		hr := l.Hooks.PostTool(ctx, c.Name, args, content, false)
		switch {
		case hr.Block:
			// A post-tool block turns the successful call into an error the
			// model must reckon with; the file effect still stands because the
			// tool already ran.
			r.isErr = true
			if hr.Message != "" {
				r.content = content + "\n\n" + hr.Message
			}
		case hr.Context != "":
			r.content = content + "\n\n" + hr.Context
		}
	}
}

// applyEffect folds a file-state effect in and tracks read recency for
// the post-compaction working-set restore (doc 03 section 11).
func (l *Loop) applyEffect(st *State, e *tool.FileStateEffect) {
	if e == nil {
		return
	}
	if l.TC != nil && l.TC.Files != nil {
		l.TC.Files.Apply(e)
	}
	for i, p := range st.recentReads {
		if p == e.Path {
			st.recentReads = append(st.recentReads[:i], st.recentReads[i+1:]...)
			break
		}
	}
	st.recentReads = append(st.recentReads, e.Path)
}

// displayString picks the human-facing render: a string Display verbatim,
// anything typed left to the client, defaulting to the model text.
func displayString(res *tool.Result, fallback string) string {
	if s, ok := res.Display.(string); ok && s != "" {
		return s
	}
	return fallback
}
