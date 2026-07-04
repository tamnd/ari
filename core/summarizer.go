package core

import (
	"context"
	"strings"

	"github.com/tamnd/ari/kernel/ledger"
	"github.com/tamnd/ari/provider"
)

// consolidatorAnt names the fold's work in the ledger and on requests, so a
// fold's spend rolls up on its own and never lands under a worker's account.
const consolidatorAnt = "consolidator"

// cheapSummarizer is the fold's window onto the model. It resolves the cheap
// tier (D17) and streams one turn per call, so the consolidator spends the
// least capable model that can do the job and the ledger tags every fold turn
// with that tier. The fold never learns a vendor or a model name; it asks for
// a summary and gets text back.
type cheapSummarizer struct {
	registry *provider.Registry
	ledger   *ledger.Ledger
	tier     string
	maxOut   int
}

// newCheapSummarizer wires the fold's summarizer to the cheap tier of a
// registry, recording every turn against the ledger so the folding-cost gate
// can read what a fold spent.
func newCheapSummarizer(reg *provider.Registry, led *ledger.Ledger) *cheapSummarizer {
	return &cheapSummarizer{registry: reg, ledger: led, tier: "cheap", maxOut: 512}
}

// textSink collects a turn's text and drops the rest; the fold wants only the
// summary the model wrote, not its thinking or tool calls.
type textSink struct {
	b strings.Builder
}

func (s *textSink) OnText(delta string)          { s.b.WriteString(delta) }
func (s *textSink) OnThinking(string)            {}
func (s *textSink) OnToolCall(provider.ToolCall) {}
func (s *textSink) OnUsage(provider.Usage)       {}

// Summarize runs one cheap-tier turn over the prompt and returns the text and
// the output tokens it spent. It walks the tier's failover chain, trying the
// next target when one errors, and records the turn it lands on against the
// ledger tagged with the cheap tier. A summarize failure returns the last
// error so a fold surfaces a dead cheap tier rather than writing on nothing.
func (s *cheapSummarizer) Summarize(ctx context.Context, prompt string) (string, int, error) {
	targets, err := s.registry.Resolve(s.tier)
	if err != nil {
		return "", 0, err
	}
	var lastErr error
	for _, tgt := range targets {
		req := provider.Request{
			Model: tgt.Model,
			Messages: []provider.Message{{
				Role:   "user",
				Blocks: []provider.MsgBlock{{Kind: "text", Text: prompt}},
			}},
			MaxOut: s.maxOut,
			Effort: "low",
			Think:  provider.ThinkOff,
			Meta:   provider.RequestMeta{Ant: consolidatorAnt, Tier: s.tier},
		}
		sink := &textSink{}
		res, err := tgt.Provider.Stream(ctx, req, sink)
		if err != nil {
			lastErr = err
			continue
		}
		model := res.Model
		if model == "" {
			model = tgt.Model
		}
		s.ledger.Record(ledger.Row{
			Ant:        consolidatorAnt,
			Tier:       s.tier,
			Provider:   tgt.Provider.Name(),
			Model:      model,
			Usage:      res.Usage,
			Wall:       res.Wall,
			StopReason: res.StopReason,
		})
		return s.trim(sink.b.String()), res.Usage.Output, nil
	}
	return "", 0, lastErr
}

func (s *cheapSummarizer) trim(text string) string {
	return strings.TrimSpace(text)
}
