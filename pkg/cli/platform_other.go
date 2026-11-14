//go:build !windows

package cli

func windowsService(*IO, string) int             { return 1 }
func windowsUninstall(*IO, bool, bool, bool) int { return 1 }
