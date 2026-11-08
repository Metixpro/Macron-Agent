package main

import (
	"os"

	"github.com/Metixpro/macron-agent/pkg/cli"
)

func runCommand(args []string) bool {
	handled, code := cli.Run(args, cli.Std())
	if handled && code != 0 {
		os.Exit(code)
	}
	return handled
}
