package main

import (
	"os"

	"github.com/Metixpro/macron-agent/pkg/cli"
)

// runCommand runs a command of the macron program (macron setup, doctor…).
// It reports false when the arguments are not a command, so the agent starts.
func runCommand(args []string) bool {
	handled, code := cli.Run(args, cli.Std())
	if handled && code != 0 {
		os.Exit(code)
	}
	return handled
}
