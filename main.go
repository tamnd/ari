package main

import (
	"os"

	"github.com/tamnd/ari/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
