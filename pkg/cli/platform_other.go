//go:build !windows

package cli

func windowsService(*IO, string) int             { return 1 }
func windowsUninstall(*IO, bool, bool, bool) int { return 1 }

func windowsAutoStart(io *IO, _ []string) int {
	io.println("telegram autostart is only used by Windows sign-in startup")
	return 2
}
