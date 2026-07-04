package colony

import "testing"

// TestNewQuestionCarriesTheAskAndConsequence is the slice-15 DoD that an
// auto-denied ask-tier call becomes a blocking Question carrying the prompt, the
// rendered consequence the human judges, the options, and refs to the rest, all
// placed in the task graph so the foreground can answer without the worker's
// transcript.
func TestNewQuestionCarriesTheAskAndConsequence(t *testing.T) {
	q := NewQuestion(BlockedAsk{
		ID:          "perm7",
		Worker:      "surveyor-2",
		TaskID:      "t1",
		SessionID:   "s1",
		Ask:         "run the generator to resolve the re-exported symbol?",
		Consequence: "sh: go generate ./api/...",
		Options:     []string{"allow once", "deny"},
		Context:     []ContextRef{{Path: "api/gen.go", Lines: [2]int{1, 40}}},
		Labels:      Labels{"untrusted"},
	})

	if !q.Blocking {
		t.Error("a worker that hit an ask-tier wall must block, not proceed")
	}
	if q.ID != "perm7" || q.From != "surveyor-2" || q.TaskID != "t1" || q.SessionID != "s1" {
		t.Errorf("question header = %+v, want the block's identity", q.Header)
	}
	if q.Kind != KindQuestion {
		t.Errorf("kind = %q, want a question", q.Kind)
	}
	if q.Consequence != "sh: go generate ./api/..." {
		t.Errorf("consequence = %q, want the rendered command the human judges", q.Consequence)
	}
	if len(q.Context) != 1 || q.Context[0].Path != "api/gen.go" {
		t.Errorf("context = %+v, want the ref to the rest", q.Context)
	}
	if len(q.Options) != 2 {
		t.Errorf("options = %+v, want both offered choices", q.Options)
	}
	if len(q.Labels) != 1 || q.Labels[0] != "untrusted" {
		t.Errorf("labels = %+v, want the worker's trust labels carried onto the ask", q.Labels)
	}
	if err := q.Validate(); err != nil {
		t.Errorf("the built question does not validate: %v", err)
	}
}

// TestNewQuestionSpillsAnOversizeConsequence proves the token budget still binds
// a Question: a consequence too big to inline (a whole diff) overflows the
// budget, which is the signal to the wiring to spill it to a file and carry a
// ContextRef instead of stuffing the board row with a transcript.
func TestNewQuestionSpillsAnOversizeConsequence(t *testing.T) {
	big := make([]byte, 4000)
	for i := range big {
		big[i] = 'x'
	}
	q := NewQuestion(BlockedAsk{
		ID:          "perm8",
		Worker:      "surveyor-2",
		TaskID:      "t1",
		SessionID:   "s1",
		Ask:         "apply this edit?",
		Consequence: string(big),
	})
	if err := q.Validate(); err == nil {
		t.Error("an oversize consequence validated; the budget must force a spill, not inline a diff")
	}
}
