package cmd

import (
	"github.com/spf13/cobra"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Serve the core over HTTP (arrives with M5)",
	RunE: func(c *cobra.Command, args []string) error {
		return notYet("ari serve", "M5 (v0.6)")
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
