//go:build !windows

package cli

func windowsService(*IO, string) int             { return 1 }
func windowsStartupInstalled() bool              { return false }
func windowsUninstall(*IO, bool, bool, bool) int { return 1 }
