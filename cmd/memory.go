package cmd

import (
	"github.com/spf13/cobra"
)

var memoryCmd = &cobra.Command{
	Use:   "memory",
	Short: "Inspect and export colony memory (arrives with M2)",
	RunE: func(c *cobra.Command, args []string) error {
		return notYet("ari memory", "M2 (v0.3)")
	},
}

func init() {
	rootCmd.AddCommand(memoryCmd)
}
