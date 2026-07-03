// Package cmd holds the ari command tree.
//
// The root command opens the TUI. Every other command is a thin client of
// the same core; none of them reach around it.
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
	"github.com/tamnd/ari/version"
)

var rootCmd = &cobra.Command{
	Use:   "ari",
	Short: "ari is a coding agent shaped like an ant colony",
	Long: `ari (アリ, ant) is a coding agent for the terminal.

Run ari in a repo to open the chat. Run ari -p "prompt" for a headless
turn. Everything ari does is asked about first unless you widen the
permission mode yourself.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(c *cobra.Command, args []string) error {
		if prompt, _ := c.Flags().GetString("prompt"); prompt != "" {
			return runHeadless(c, prompt)
		}
		return runTUI(c)
	},
}

// Execute runs the command tree. main is the only caller.
func Execute() error {
	rootCmd.PersistentFlags().StringP("config", "c", "", "path to a config file (overrides discovery)")
	rootCmd.Flags().StringP("prompt", "p", "", "run one headless turn with this prompt and exit")
	rootCmd.Flags().Bool("json", false, "with -p, stream raw events as JSON lines to stdout")
	rootCmd.Flags().String("mode", "", "permission mode: ask, auto-edit, full-auto, plan")
	rootCmd.Flags().Bool("resume", false, "resume the most recent session in this project")

	err := fang.Execute(context.Background(), rootCmd, fang.WithVersion(version.Resolve()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return err
}

func notYet(what, milestone string) error {
	return fmt.Errorf("%s is not built yet; it arrives with %s", what, milestone)
}
