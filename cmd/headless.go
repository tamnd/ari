package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/event"
	"github.com/tamnd/ari/permission"
)

// runHeadless is ari -p: one turn against the same core the TUI runs on,
// output to stdout, exit code from the terminal reason (doc 01 section
// 10.2). Passing "-" as the prompt reads it from stdin, so a pipe works.
func runHeadless(c *cobra.Command, prompt string) error {
	ctx, stop := signal.NotifyContext(c.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	prompt, err := readPrompt(prompt, os.Stdin)
	if err != nil {
		return err
	}

	jsonOut, _ := c.Flags().GetBool("json")
	mode, _ := c.Flags().GetString("mode")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	return oneShot(ctx, shot{Dir: cwd, Prompt: prompt, Mode: mode, JSON: jsonOut, Out: os.Stdout})
}

// readPrompt resolves the prompt argument: "-" reads stdin, so ari -p -
// composes in a shell pipeline (doc 01 section 5.2).
func readPrompt(arg string, in io.Reader) (string, error) {
	if arg == "-" {
		data, err := io.ReadAll(in)
		if err != nil {
			return "", core.Wrap(core.ErrConfig, err, "reading the prompt from stdin")
		}
		arg = strings.TrimSpace(string(data))
	}
	if arg == "" {
		return "", core.Errf(core.ErrConfig, "the prompt is empty; pass text after -p or pipe it to -p -")
	}
	return arg, nil
}

// shot is one headless turn's inputs, a struct so tests drive oneShot
// without a cobra command or a network provider; Opts is where a test
// injects its scripted registry and config.
type shot struct {
	Dir    string
	Prompt string
	Mode   string
	JSON   bool
	Out    io.Writer
	Opts   []core.Option
}

// oneShot opens the colony headless, runs the one turn, and drains the
// stream until that turn finishes. The runner goes headless before
// Start, so an Ask is claimed by the auto-deny resolver and never hangs
// (doc 05 section 11).
func oneShot(ctx context.Context, s shot) error {
	runner := ant.NewRunner()
	opts := append([]core.Option{
		core.WithRunner(runner),
		core.WithFlags(config.FlagOverrides{Mode: s.Mode}),
	}, s.Opts...)
	colony, err := core.Open(ctx, s.Dir, opts...)
	if err != nil {
		return err
	}
	defer func() { _ = colony.Close() }()
	runner.Bind(colony)
	runner.Headless()

	// Subscribe before Start so the hello is the first line out.
	sub, err := colony.Events(ctx, core.EventFilter{})
	if err != nil {
		return err
	}
	defer sub.Cancel()
	if err := colony.Start(ctx); err != nil {
		return err
	}

	sid, err := colony.NewSession(ctx, core.NewSessionRequest{})
	if err != nil {
		return err
	}
	turn, err := colony.Submit(ctx, core.SubmitRequest{Session: sid, Text: s.Prompt})
	if err != nil {
		return err
	}
	return consume(ctx, sub, s, string(turn))
}

// consume drains the stream until our turn finishes. JSON mode prints
// every envelope as one line in journal order, nothing unwrapped or
// reordered, so a downstream tool can reconstruct the whole turn from
// the stream alone. Plain mode prints the model's final message: the
// text after the last tool call.
func consume(ctx context.Context, sub *core.Subscription, s shot, turn string) error {
	enc := json.NewEncoder(s.Out)
	var text strings.Builder
	denied := false
	var lastErr *event.ErrorInfo

	for {
		select {
		case e := <-sub.C:
			if s.JSON {
				if err := enc.Encode(e); err != nil {
					return err
				}
			}
			if e.Turn != turn {
				continue
			}
			switch e.Type {
			case event.TypeToolStart:
				// A tool call means the text so far was preamble, not
				// the final message.
				text.Reset()
			case event.TypeTextDelta:
				var d event.TextDelta
				if err := e.Decode(&d); err == nil {
					text.WriteString(d.Text)
				}
			case event.TypePermissionResolved:
				var pr event.PermissionResolved
				if err := e.Decode(&pr); err == nil && pr.Kind == string(permission.KindHeadless) {
					denied = true
				}
			case event.TypeError:
				var info event.ErrorInfo
				if err := e.Decode(&info); err == nil {
					lastErr = &info
				}
			case event.TypeTurnFinished:
				var fin event.TurnFinished
				if err := e.Decode(&fin); err != nil {
					return core.Wrap(core.ErrInternal, err, "decoding turn.finished")
				}
				if !s.JSON && text.Len() > 0 {
					if _, werr := fmt.Fprintln(s.Out, strings.TrimRight(text.String(), "\n")); werr != nil {
						return werr
					}
				}
				return outcome(fin, denied, lastErr)
			}
		case <-sub.Done():
			return core.Errf(core.ErrInternal, "the event stream ended before the turn finished")
		case <-ctx.Done():
			return core.Errf(core.ErrCanceled, "interrupted before the turn finished")
		}
	}
}

// outcome maps the terminal reason to the process error exitCode turns
// into the documented codes. A headless auto-deny outranks a clean
// finish: the turn only "completed" because nobody could approve the
// call it wanted, and a script must see that as a permission stop.
func outcome(fin event.TurnFinished, denied bool, lastErr *event.ErrorInfo) error {
	switch fin.Reason {
	case "completed", "done":
		if denied {
			return core.Errf(core.ErrPermission,
				"a tool call needed approval and a headless run auto-denies; add an allow rule or rerun with --mode full-auto")
		}
		return nil
	case "canceled", "tools_canceled":
		return core.Errf(core.ErrCanceled, "the turn was canceled")
	case "budget_exhausted":
		return core.Errf(core.ErrBudget, "the turn stopped at its token budget")
	case "max_turns":
		return core.Errf(core.ErrInternal, "the turn hit its iteration ceiling without finishing")
	case "model_error", "prompt_too_long", "compaction_failed":
		return core.Errf(core.ErrProvider, "%s", orElse(fin.Error, "the model provider failed"))
	case "error":
		if lastErr != nil {
			return core.Errf(core.ErrorKind(lastErr.Code), "%s", lastErr.Message)
		}
		return core.Errf(core.ErrInternal, "%s", orElse(fin.Error, "the turn failed"))
	default:
		return core.Errf(core.ErrInternal, "the turn ended with an unrecognized reason %q", fin.Reason)
	}
}

func orElse(s, fallback string) string {
	if s != "" {
		return s
	}
	return fallback
}
