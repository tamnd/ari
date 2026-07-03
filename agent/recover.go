package agent

import (
	"errors"
	"fmt"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
)

// resumeAfterTruncation steers the model past a repeated output
// truncation once the escalated cap was not enough.
const resumeAfterTruncation = "Your previous response was cut off at the output limit. " +
	"Continue exactly where it stopped. Do not repeat what you already produced."

// classifyModelError routes one provider error to the transition that
// can fix it. The error is parked in pendingErr, never published: only
// finish surfaces it, and only if nothing fixed it (doc 03 sections 12
// and 13).
func (l *Loop) classifyModelError(st *State, err error) {
	var pe *provider.Error
	if !errors.As(err, &pe) {
		pe = &provider.Error{Class: provider.ClassFatal, Message: err.Error()}
	}
	st.pendingErr = pe

	switch pe.Class {
	case provider.ClassPromptTooLong:
		if st.reactiveCompacted {
			// The ladder already ran for a 413 and the prompt still does
			// not fit: that is the symptom's terminal reason.
			st.term = TermPromptTooLong
			st.next = transTerminate
			return
		}
		st.reactiveCompacted = true
		// A proactive pass this turn clearly under-estimated; let the
		// ladder run again for the reactive trigger.
		st.compactedThisTurn = false
		st.compactTrigger = "reactive"
		st.next = transCompact
	case provider.ClassOverloaded, provider.ClassTransient:
		if st.modelRetries < maxModelRetries {
			st.next = transRetryModel
			return
		}
		st.next = transFallbackModel
	default:
		st.term = TermModelError
		st.next = transTerminate
	}
}

// retryModel waits out the backoff and re-issues the same request.
func (l *Loop) retryModel(st *State) {
	st.modelRetries++
	l.emit(event.TypeLog, event.Log{
		Level: "debug",
		Text:  fmt.Sprintf("transient provider error, retry %d of %d", st.modelRetries, maxModelRetries),
	})
	l.Limits.sleep(backoff(st.modelRetries))
	st.next = transCallModel
}

// fallbackModel switches to the configured fallback once per run.
// Exhausting it is the end of the line for provider trouble.
func (l *Loop) fallbackModel(st *State) {
	if st.fellBack || l.Fallback == "" || l.Fallback == st.model {
		st.term = TermModelError
		st.next = transTerminate
		return
	}
	st.fellBack = true
	st.model = l.Fallback
	st.modelRetries = 0
	l.emit(event.TypeLog, event.Log{
		Level: "warn",
		Text:  fmt.Sprintf("provider retries exhausted, falling back to %s", st.model),
	})
	st.next = transCallModel
}

// recoverOutput handles a max_tokens truncation. The first recovery
// silently re-issues with an escalated cap; later ones keep the partial
// response and steer the model to resume (doc 03 section 12).
func (l *Loop) recoverOutput(st *State) {
	st.outputRetries++
	if st.outputRetries > maxOutputRetries {
		st.pendingErr = &provider.Error{
			Class:   provider.ClassFatal,
			Message: fmt.Sprintf("output truncated %d times in a row", maxOutputRetries+1),
		}
		st.term = TermModelError
		st.next = transTerminate
		return
	}
	if st.outputRetries == 1 {
		if st.maxOut < escalatedMaxOutput {
			st.maxOut = escalatedMaxOutput
		}
		l.emit(event.TypeLog, event.Log{
			Level: "debug",
			Text:  fmt.Sprintf("output truncated, re-issuing with a %d-token cap", st.maxOut),
		})
	} else {
		st.msgs = append(st.msgs, provider.Message{
			Role:   "user",
			Blocks: []provider.MsgBlock{{Kind: "text", Text: resumeAfterTruncation}},
		})
		l.appendEntry(session.EntryUser, map[string]string{"text": resumeAfterTruncation})
		l.emit(event.TypeLog, event.Log{
			Level: "debug",
			Text:  fmt.Sprintf("output truncated again, steering a resume (%d of %d)", st.outputRetries, maxOutputRetries),
		})
	}
	st.next = transCallModel
}

// openCircuit is the compaction circuit breaker: consecutive summarize
// failures end the run instead of retrying forever (doc 03 section 11).
func (l *Loop) openCircuit(st *State) {
	st.term = TermCompactionFailed
	st.next = transTerminate
}
