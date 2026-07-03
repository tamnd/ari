package cmd

import (
	"fmt"
	"runtime"

	"github.com/spf13/cobra"
	"github.com/tamnd/ari/version"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the ari version and build stamp",
	Run: func(c *cobra.Command, args []string) {
		fmt.Printf("ari %s (%s, %s, %s/%s)\n", version.Resolve(), version.Commit, version.Date, runtime.GOOS, runtime.GOARCH)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
