package agent

import (
	"strings"
	"testing"
	"time"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/provider/scripted"
)

// TestTransientRetrySucceeds retries through two transient errors with
// the bounded backoff and completes silently: no error event.
func TestTransientRetrySucceeds(t *testing.T) {
	fail := scripted.Response{Fail: &provider.Error{Class: provider.ClassTransient, Message: "flaky network"}}
	p := scripted.New(fail, fail,
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	h := newHarness(p, registry(t))
	out := run(t, h, "flaky")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if got, want := *h.sleeps, []time.Duration{250 * time.Millisecond, 500 * time.Millisecond}; len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("backoff = %v, want %v", got, want)
	}
	for _, e := range h.events.events {
		if e.Type == event.TypeError {
			t.Fatal("a recovered error must never surface")
		}
	}
}

// TestRetriesExhaustedFallsBack burns the retry budget on the primary,
// switches to the fallback once, and completes there.
func TestRetriesExhaustedFallsBack(t *testing.T) {
	fail := scripted.Response{Fail: &provider.Error{Class: provider.ClassOverloaded, Message: "529"}}
	p := scripted.New(fail, fail, fail, fail, fail,
		scripted.Response{Text: "done", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	h := newHarness(p, registry(t))
	h.loop.Fallback = "backup"
	out := run(t, h, "overloaded")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if len(*h.sleeps) != maxModelRetries {
		t.Fatalf("retries = %d, want %d", len(*h.sleeps), maxModelRetries)
	}
	if (*h.rows)[0].Model != "backup" {
		t.Fatalf("completed turn metered on %q, want backup", (*h.rows)[0].Model)
	}
}

// TestFallbackExhaustedTerminates ends the line: no fallback left means
// TermModelError and exactly one error event carrying the cause.
func TestFallbackExhaustedTerminates(t *testing.T) {
	fail := scripted.Response{Fail: &provider.Error{Class: provider.ClassTransient, Message: "still down"}}
	responses := make([]scripted.Response, 10)
	for i := range responses {
		responses[i] = fail
	}
	h := newHarness(scripted.New(responses...), registry(t))
	out := run(t, h, "doomed")
	if out.Reason != TermModelError {
		t.Fatalf("reason = %s, want model_error", out.Reason)
	}
	var infos []event.ErrorInfo
	for _, e := range h.events.events {
		if e.Type == event.TypeError {
			var info event.ErrorInfo
			if err := e.Decode(&info); err != nil {
				t.Fatal(err)
			}
			infos = append(infos, info)
		}
	}
	if len(infos) != 1 {
		t.Fatalf("error events = %d, want exactly 1", len(infos))
	}
	if infos[0].Cause != "still down" {
		t.Fatalf("cause = %q", infos[0].Cause)
	}
}

// TestFatalErrorTerminatesImmediately never retries a fatal class.
func TestFatalErrorTerminatesImmediately(t *testing.T) {
	p := scripted.New(scripted.Response{Fail: &provider.Error{Class: provider.ClassFatal, Message: "bad api key", Status: 401}})
	h := newHarness(p, registry(t))
	out := run(t, h, "unauthorized")
	if out.Reason != TermModelError {
		t.Fatalf("reason = %s", out.Reason)
	}
	if len(*h.sleeps) != 0 {
		t.Fatal("a fatal error must not be retried")
	}
}

// TestOutputTruncationFirstRecovery silently re-issues with the
// escalated cap and drops the partial, so the transcript has exactly
// one assistant message.
func TestOutputTruncationFirstRecovery(t *testing.T) {
	p := scripted.New(
		scripted.Response{Text: "partial answ", Stop: "max_tokens", Usage: provider.Usage{Input: 1, Output: 1}},
		scripted.Response{Text: "the full answer", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t))
	h.loop.Limits.MaxOut = 8000
	out := run(t, h, "long answer")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	if rec.requests[1].MaxOut != escalatedMaxOutput {
		t.Fatalf("second request MaxOut = %d, want %d", rec.requests[1].MaxOut, escalatedMaxOutput)
	}
	// The re-issued request must not contain the dropped partial.
	for _, m := range rec.requests[1].Messages {
		for _, b := range m.Blocks {
			if strings.Contains(b.Text, "partial answ") {
				t.Fatal("the dropped partial leaked into the retry")
			}
		}
	}
}

// TestOutputTruncationSecondRecovery keeps the partial and injects the
// resume steering message.
func TestOutputTruncationSecondRecovery(t *testing.T) {
	truncated := scripted.Response{Text: "part", Stop: "max_tokens", Usage: provider.Usage{Input: 1, Output: 1}}
	p := scripted.New(truncated, truncated,
		scripted.Response{Text: "rest of it", Usage: provider.Usage{Input: 1, Output: 1}},
	)
	rec := &recordingProvider{inner: p}
	h := newHarness(rec, registry(t))
	out := run(t, h, "very long answer")
	if out.Reason != TermCompleted {
		t.Fatalf("reason = %s", out.Reason)
	}
	third := rec.requests[2].Messages
	last := third[len(third)-1]
	if last.Role != "user" || !strings.Contains(last.Blocks[0].Text, "cut off at the output limit") {
		t.Fatalf("resume message missing, last = %+v", last)
	}
	// The second truncation's partial is kept this time.
	var sawPartial bool
	for _, m := range third {
		if m.Role == "assistant" {
			for _, b := range m.Blocks {
				if b.Text == "part" {
					sawPartial = true
				}
			}
		}
	}
	if !sawPartial {
		t.Fatal("the partial before a steered resume must stay in the transcript")
	}
}

// TestOutputTruncationExhausted gives up after the ceiling.
func TestOutputTruncationExhausted(t *testing.T) {
	truncated := scripted.Response{Text: "part", Stop: "max_tokens", Usage: provider.Usage{Input: 1, Output: 1}}
	p := scripted.New(truncated, truncated, truncated, truncated, truncated)
	h := newHarness(p, registry(t))
	out := run(t, h, "never fits")
	if out.Reason != TermModelError {
		t.Fatalf("reason = %s, want model_error", out.Reason)
	}
}

// TestBackoffSchedule pins the capped exponential.
func TestBackoffSchedule(t *testing.T) {
	want := []time.Duration{
		250 * time.Millisecond, 500 * time.Millisecond,
		1 * time.Second, 2 * time.Second, 4 * time.Second, 4 * time.Second,
	}
	for i, w := range want {
		if got := backoff(i + 1); got != w {
			t.Fatalf("backoff(%d) = %v, want %v", i+1, got, w)
		}
	}
}
