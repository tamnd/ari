package cmd

import (
	"github.com/spf13/cobra"
)

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Audit the nest, permission rules, and listening surfaces",
	RunE: func(c *cobra.Command, args []string) error {
		return notYet("ari doctor", "the release slice of this milestone")
	},
}

func init() {
	doctorCmd.Flags().Bool("fix", false, "repair what can be repaired safely")
	rootCmd.AddCommand(doctorCmd)
}
