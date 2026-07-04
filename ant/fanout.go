package ant

import (
	"context"
	"fmt"
	"strings"

	"github.com/tamnd/ari/colony"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
)

// decompose splits a read-only foreground brief into one surveyor subtask per
// file it names, so a survey or research request that references several files
// can run its reads in parallel. Every subtask it proposes is a surveyor
// producing a finding, so the split is independent and read-heavy by
// construction; whether it is worth the tokens is the gate's call, not this
// function's. A brief that is not a survey or research task, or that names
// fewer than two files, has nothing to split and yields nil, so the turn stays
// single-ant (doc 09 section 5, D5).
func (r *Runner) decompose(brief colony.TaskBrief) []colony.TaskBrief {
	if brief.Class != colony.ClassSurvey && brief.Class != colony.ClassResearch {
		return nil
	}
	var files []string
	for _, a := range brief.Anchors {
		if a.Kind == colony.AnchorFile && a.Value != "" {
			files = append(files, a.Value)
		}
	}
	if len(files) < 2 {
		return nil
	}
	per := brief.Budget.Tokens / len(files)
	subs := make([]colony.TaskBrief, 0, len(files))
	for i, f := range files {
		id := fmt.Sprintf("%s-fanout-%d", brief.TaskID, i)
		subs = append(subs, colony.TaskBrief{
			Header: colony.Header{
				ID:        "brief-" + id,
				Kind:      colony.KindTaskBrief,
				From:      "queen",
				TaskID:    id,
				SessionID: brief.SessionID,
				Labels:    brief.Labels,
			},
			Goal:        fmt.Sprintf("%s (focus: %s)", brief.Goal, f),
			Deliverable: colony.KindFinding,
			Class:       brief.Class,
			DirectedTo:  "surveyor",
			Budget:      colony.Budget{Tokens: per},
			Anchors:     []colony.Anchor{{Kind: colony.AnchorFile, Value: f}},
			Embed:       brief.Embed,
		})
	}
	return subs
}

// fanOut runs the D5 gate on a decomposed foreground brief and, on approval,
// runs the surveys and returns a preface for the foreground turn to synthesize.
// A refusal returns an empty preface, so the turn proceeds single-ant exactly
// as it did before, which is D5's cheap-and-quiet rule: only an approval is
// loud on the normal stream, and it carries the full argument the gate weighed;
// a refusal only whispers the failing test onto the debug lane. The
// gate reads the class cost history the trails hold, so a colony with no survey
// history yet refuses until a foreground survey has folded its cost (doc 06
// section 5).
func (r *Runner) fanOut(ctx context.Context, t *core.TurnHandle, brief colony.TaskBrief) string {
	subs := r.decompose(brief)
	if len(subs) < 2 {
		return ""
	}
	plan, refusal := r.queen.FanOutDecide(ctx, subs, int64(brief.Budget.Tokens))
	if plan == nil {
		// A refusal stays off the normal stream, but the debug lane names the
		// failing test so a human auditing the journal sees why the colony did
		// not wake, not a bare nil (doc 06 section 5, plan 6.2).
		_ = r.emit(event.TypeFanOutRefused, string(t.Session), string(t.Turn), event.FanOutRefused{
			Task:   brief.TaskID,
			Failed: refusal.Failed,
			Reason: refusal.Reason,
		})
		return ""
	}
	_ = r.emit(event.TypeFanOutApproved, string(t.Session), string(t.Turn), event.FanOutApproved{
		Task:           brief.TaskID,
		Subtasks:       plan.Arg.Subtasks,
		IndependenceBy: plan.Arg.IndependenceBy,
		Workload:       plan.Arg.Workload,
		Specialist:     plan.Arg.Specialist,
		Projected:      plan.Arg.Projected,
		Remaining:      plan.Arg.Remaining,
	})
	res, err := r.dispatch(ctx, t.Store, t.Session, brief, plan)
	if err != nil {
		r.logDebug(t, "fan-out dispatch: "+err.Error())
	}
	if len(res.Findings) == 0 {
		return ""
	}
	return renderFindingsPreface(res.Findings)
}

// renderFindingsPreface turns the surveys' findings into the block-three
// preface the foreground worker reads before the human's request. It rides the
// task tail, so blocks one and two stay byte-identical and the D14 cache holds;
// only this turn's tail grew (doc 03 section 8, D14).
func renderFindingsPreface(findings []colony.Finding) string {
	var b strings.Builder
	b.WriteString("Parallel surveys of the files you referenced finished. Ground your answer in these findings:\n\n")
	for _, f := range findings {
		fmt.Fprintf(&b, "- %s\n", f.Summary)
		for _, c := range f.Evidence {
			loc := c.Path
			if c.Lines[0] > 0 {
				loc = fmt.Sprintf("%s:%d", c.Path, c.Lines[0])
			}
			if c.Quote != "" {
				fmt.Fprintf(&b, "  %s: %s\n", loc, c.Quote)
			} else {
				fmt.Fprintf(&b, "  %s\n", loc)
			}
		}
	}
	b.WriteString("\n")
	return b.String()
}
