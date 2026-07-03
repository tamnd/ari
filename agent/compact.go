package agent

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
	"github.com/tamnd/ari/session"
	"github.com/tamnd/ari/tool"
)

// clearedResult is what an old tool_result block is replaced with. The
// tool_use_id keys the placeholder to the call, so the transcript stays
// well formed for every provider dialect (research A.7).
const clearedResult = "[old tool result cleared to save context; re-run the tool if you need it again]"

const summarySystem = "You compress agent transcripts. Write a dense summary that lets " +
	"the same agent resume the task: the user's goal, every decision made, files touched " +
	"with paths, current state of the work, and the immediate next step. No preamble."

const summaryAsk = "Summarize the conversation above per your instructions. " +
	"Reply with only the summary."

// compact is the ladder, cheapest rung first (doc 03 section 11).
// Rung one clears old tool results with no model call; rung two (old
// thinking) is a no-op in M0 because thinking never re-enters the
// context; rung three summarizes and moves the boundary.
func (l *Loop) compact(ctx context.Context, st *State) {
	pre := l.liveTokens(st)
	if l.clearOldToolResults(st) && l.liveTokens(st) < l.Limits.thresholds().AutoCompact {
		st.compactedThisTurn = true
		l.emit(event.TypeLog, event.Log{
			Level: "debug",
			Text:  fmt.Sprintf("compaction rung one: cleared old tool results, %d tokens to %d", pre, l.liveTokens(st)),
		})
		st.next = transAssemble
		return
	}
	l.summarize(ctx, st, pre)
}

// clearOldToolResults replaces every tool_result but the most recent
// few with a placeholder. Reports whether anything was cleared.
func (l *Loop) clearOldToolResults(st *State) bool {
	type pos struct{ msg, block int }
	var spots []pos
	for mi := st.boundaryIdx; mi < len(st.msgs); mi++ {
		for bi := range st.msgs[mi].Blocks {
			b := &st.msgs[mi].Blocks[bi]
			if b.Kind == "tool_result" && b.Text != clearedResult {
				spots = append(spots, pos{mi, bi})
			}
		}
	}
	keep := l.Limits.keepToolResults()
	if len(spots) <= keep {
		return false
	}
	for _, p := range spots[:len(spots)-keep] {
		st.msgs[p.msg].Blocks[p.block].Text = clearedResult
	}
	return true
}

// summarySink accumulates the compaction model call's text.
type summarySink struct {
	text  strings.Builder
	usage provider.Usage
}

func (s *summarySink) OnText(delta string)          { s.text.WriteString(delta) }
func (s *summarySink) OnThinking(string)            {}
func (s *summarySink) OnToolCall(provider.ToolCall) {}
func (s *summarySink) OnUsage(u provider.Usage)     { s.usage = u }

// summarize is rung three: one model call over the visible tail, then
// the boundary moves so the model never sees the raw history again.
// The live transcript keeps every message; only boundaryIdx moves (D9).
func (l *Loop) summarize(ctx context.Context, st *State, pre int) {
	st.compactedThisTurn = true
	tail := st.msgs[st.boundaryIdx:]

	req := provider.Request{
		Model:  st.model,
		System: []provider.Block{{Text: summarySystem}},
		Messages: append(append([]provider.Message{}, tail...), provider.Message{
			Role:   "user",
			Blocks: []provider.MsgBlock{{Kind: "text", Text: summaryAsk}},
		}),
		Meta: provider.RequestMeta{Ant: "worker", Session: l.Session, Tier: l.Tier},
	}
	sink := &summarySink{}
	started := l.now()
	_, err := l.Provider.Stream(ctx, req, sink)
	if l.Record != nil {
		l.Record(ledger.Row{
			Ant: "worker", Session: l.Session, Turn: l.Turn,
			Provider: l.Provider.Name(), Model: st.model, Tier: l.Tier,
			Usage: sink.usage, Wall: l.now().Sub(started),
			Estimated: sink.usage.Estimated, StopReason: "compaction",
		})
	}
	if err != nil || strings.TrimSpace(sink.text.String()) == "" {
		if ctx.Err() != nil {
			st.setCanceled()
			st.next = transTerminate
			return
		}
		st.consecCompactFail++
		if st.consecCompactFail >= maxConsecutiveCompactFail {
			l.openCircuit(st)
			return
		}
		l.emit(event.TypeLog, event.Log{
			Level: "warn",
			Text:  fmt.Sprintf("compaction failed (%d consecutive); continuing uncompacted", st.consecCompactFail),
		})
		// compactedThisTurn stops re-entry until the next model turn.
		st.next = transAssemble
		return
	}

	marker := provider.Message{Role: "system", Blocks: []provider.MsgBlock{{
		Kind: "text",
		Text: fmt.Sprintf("[history compacted: %d messages summarized]", len(tail)),
	}}}
	summary := provider.Message{Role: "user", Blocks: []provider.MsgBlock{{
		Kind: "text",
		Text: "Summary of the conversation so far:\n\n" + sink.text.String(),
	}}}
	st.msgs = append(st.msgs, marker, summary)
	st.boundaryIdx = len(st.msgs) - 2
	st.compactions++
	st.consecCompactFail = 0
	st.pendingErr = nil

	trigger := st.compactTrigger
	if trigger == "" {
		trigger = "auto"
	}
	st.compactTrigger = ""
	l.appendEntry(session.EntryCompact, CompactBody{
		Trigger:    trigger,
		PreTokens:  pre,
		Summarized: len(tail),
	})
	l.emit(event.TypeLog, event.Log{
		Level: "info",
		Text:  fmt.Sprintf("compacted %d messages, %d tokens to %d", len(tail), pre, l.liveTokens(st)),
	})

	l.restoreWorkingSet(st)
	st.next = transAssemble
}

// restoreWorkingSet re-reads the most recently touched files after a
// summarize, because the summary describes them but their bytes are
// gone. It also rebuilds the read-before-write map for exactly the
// files it restored: the ant only remembers reading what it actually
// re-read (D8, doc 03 section 11).
func (l *Loop) restoreWorkingSet(st *State) {
	if len(st.recentReads) == 0 {
		return
	}
	if l.TC != nil && l.TC.Files != nil {
		l.TC.Files.Clear()
	}
	recent := st.recentReads
	if len(recent) > maxRestoreFiles {
		recent = recent[len(recent)-maxRestoreFiles:]
	}
	total := 0
	restored := make([]string, 0, len(recent))
	// Most recent first, so the freshest file wins the total budget.
	for i := len(recent) - 1; i >= 0; i-- {
		path := recent[i]
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		content := string(b)
		if estimateTokens(content) > maxRestoreTokensPerFile {
			content = content[:maxRestoreTokensPerFile*4] + "\n[truncated at the restore cap]"
		}
		if total+estimateTokens(content) > maxRestoreTokensTotal {
			break
		}
		total += estimateTokens(content)
		st.msgs = append(st.msgs, provider.Message{Role: "user", Blocks: []provider.MsgBlock{{
			Kind: "text",
			Text: fmt.Sprintf("Restored after compaction, the current content of %s:\n\n%s", path, content),
		}}})
		l.appendEntry(session.EntryUser, map[string]string{
			"text": fmt.Sprintf("[restored %s after compaction]", path),
		})
		if l.TC != nil && l.TC.Files != nil {
			info, statErr := os.Stat(path)
			if statErr == nil {
				l.TC.Files.Set(path, tool.HashBytes(b), info.ModTime(), strings.Count(string(b), "\n")+1)
			}
		}
		restored = append(restored, path)
	}
	// Recency now only covers what survived; older paths are gone with
	// the summarized history.
	for i, j := 0, len(restored)-1; i < j; i, j = i+1, j-1 {
		restored[i], restored[j] = restored[j], restored[i]
	}
	st.recentReads = restored
}
