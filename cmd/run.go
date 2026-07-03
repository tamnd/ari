package cmd

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	btea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"

	"github.com/tamnd/ari/ant"
	"github.com/tamnd/ari/config"
	"github.com/tamnd/ari/core"
	"github.com/tamnd/ari/nest"
	"github.com/tamnd/ari/ui"
	"github.com/tamnd/ari/ui/bus"
	"github.com/tamnd/ari/ui/input"
	"github.com/tamnd/ari/ui/keys"
	"github.com/tamnd/ari/ui/splash"
	"github.com/tamnd/ari/ui/theme"
)

// contextWindow mirrors the loop's default model window (agent.Limits);
// the sidebar's fill figure is honest against what the loop enforces.
const contextWindow = 200_000

// runTUI opens the terminal shell: the colony headless underneath, the
// ui.Model on top, and the bus pump carrying events between them.
func runTUI(c *cobra.Command) error {
	ctx := c.Context()
	mode, _ := c.Flags().GetString("mode")
	resume, _ := c.Flags().GetBool("resume")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	runner := ant.NewRunner()
	colony, err := core.Open(ctx, cwd,
		core.WithRunner(runner),
		core.WithFlags(config.FlagOverrides{Mode: mode}),
	)
	if err != nil {
		return err
	}
	defer func() { _ = colony.Close() }()
	runner.Bind(colony)

	// Subscribe before Start so the hello is the first thing seen.
	sub, err := colony.Events(ctx, core.EventFilter{})
	if err != nil {
		return err
	}
	defer sub.Cancel()
	if err := colony.Start(ctx); err != nil {
		return err
	}

	cfg := colony.Config()
	n := colony.Nest()
	th, ok := theme.Themes()[cfg.UI.Theme]
	if !ok {
		th = theme.Dark()
	}

	session := ""
	if resume {
		if sums, lerr := colony.ListSessions(ctx); lerr == nil && len(sums) > 0 {
			session = string(sums[0].ID)
		}
	}

	broker := bus.New[btea.Msg]()
	m := ui.New(ui.Options{
		Client:        colonyClient{c: colony},
		Theme:         th,
		Keys:          keys.Default(),
		FirstRun:      firstRun(n),
		Cwd:           n.Root,
		Model:         frontierModel(cfg),
		Provider:      frontierProvider(cfg),
		Models:        tierModels(cfg),
		ContextWindow: contextWindow,
		Session:       session,
		Drops:         broker.Dropped,
		Onboarded:     onboarded(n),
	})

	filter := input.NewFilter()
	prog := btea.NewProgram(m,
		btea.WithContext(ctx),
		btea.WithFilter(filter.Filter),
	)

	pumpCtx, stopPumps := context.WithCancel(ctx)
	defer stopPumps()
	go bus.Pump(pumpCtx, sub.C, broker)
	uiSub := broker.Subscribe(256)
	defer uiSub.Cancel()
	go bus.Drain(pumpCtx, uiSub, prog.Send)

	_, err = prog.Run()
	return err
}

// firstRun is true when the global config file does not exist yet; its
// existence is what a completed onboarding writes.
func firstRun(n nest.Nest) bool {
	_, err := os.Stat(n.GlobalConfig())
	return os.IsNotExist(err)
}

// frontierProvider and frontierModel read the head of the frontier
// chain, which is what the one M0 ant runs on.
func frontierProvider(cfg *config.Config) string {
	if t, ok := cfg.Tiers["frontier"]; ok && len(t.Chain) > 0 {
		return t.Chain[0].Provider
	}
	return ""
}

func frontierModel(cfg *config.Config) string {
	if t, ok := cfg.Tiers["frontier"]; ok && len(t.Chain) > 0 {
		return t.Chain[0].Model
	}
	return ""
}

// tierModels flattens every configured chain into the picker's list.
func tierModels(cfg *config.Config) []string {
	var models []string
	for _, t := range cfg.Tiers {
		for _, tg := range t.Chain {
			if tg.Model != "" && !slices.Contains(models, tg.Model) {
				models = append(models, tg.Model)
			}
		}
	}
	slices.Sort(models)
	return models
}

// onboardModels maps the onboarding picks to a frontier head per
// provider and tier, the one line of config the flow decides.
var onboardModels = map[string]map[string]string{
	"anthropic": {
		"best":     "claude-opus-4-8",
		"balanced": "claude-sonnet-5",
		"fast":     "claude-haiku-4-5",
	},
	"openai": {
		"best":     "gpt-5.1",
		"balanced": "gpt-5.1",
		"fast":     "gpt-5.1-mini",
	},
	"openrouter": {
		"best":     "anthropic/claude-opus-4.8",
		"balanced": "anthropic/claude-sonnet-5",
		"fast":     "anthropic/claude-haiku-4.5",
	},
}

var onboardEndpoints = map[string]struct{ url, env string }{
	"openai":     {url: "https://api.openai.com/v1", env: "OPENAI_API_KEY"},
	"openrouter": {url: "https://openrouter.ai/api/v1", env: "OPENROUTER_API_KEY"},
}

// onboarded persists the first-run choices as the global config file.
// The file existing is what makes the next start skip onboarding. Only
// an env reference is ever written, never a key value (D16).
func onboarded(n nest.Nest) func(splash.Outcome) error {
	return func(o splash.Outcome) error {
		var b strings.Builder
		b.WriteString("# ari config, written by first-run onboarding.\n")
		if ep, ok := onboardEndpoints[o.Provider]; ok {
			fmt.Fprintf(&b, "\n[provider.%s]\nkind = \"openai\"\nbase_url = %q\napi_key = \"${%s}\"\n",
				o.Provider, ep.url, ep.env)
		}
		if model := onboardModels[o.Provider][o.Tier]; model != "" {
			fmt.Fprintf(&b, "\n[tier.frontier]\nchain = [{ provider = %q, model = %q }]\n",
				o.Provider, model)
		}
		if err := os.WriteFile(n.GlobalConfig(), []byte(b.String()), 0o600); err != nil {
			return err
		}
		if o.InitProject {
			return os.MkdirAll(n.ProjectDir(), 0o755)
		}
		return nil
	}
}

// runHeadless runs one turn against the core and exits. The real client
// lands with the headless slice.
func runHeadless(_ *cobra.Command, _ string) error {
	return notYet("ari -p", "a later M0 slice in this milestone")
}
