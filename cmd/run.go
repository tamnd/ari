package cmd

import (
	"github.com/spf13/cobra"
)

// runTUI opens the terminal shell. The real client lands with the ui
// slices; until then the command is honest about what exists.
func runTUI(_ *cobra.Command) error {
	return notYet("the ari TUI", "a later M0 slice in this milestone")
}

// runHeadless runs one turn against the core and exits. The real client
// lands with the headless slice.
func runHeadless(_ *cobra.Command, _ string) error {
	return notYet("ari -p", "a later M0 slice in this milestone")
}
