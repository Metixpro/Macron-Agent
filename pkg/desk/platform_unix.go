//go:build !windows

package desk

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/ssh/agent"
)

func hideWindow(*exec.Cmd) {}

func sshAgent() agent.ExtendedAgent {
	sock := os.Getenv("SSH_AUTH_SOCK")
	if sock == "" {
		return nil
	}
	c, err := net.Dial("unix", sock)
	if err != nil {
		return nil
	}
	return agent.NewClient(c)
}

func wslAvailable() bool { return false }

func reveal(path string, folder bool) error {
	if runtime.GOOS == "darwin" {
		if folder {
			return exec.Command("open", "-R", path).Start()
		}
		return exec.Command("open", path).Start()
	}
	if folder {
		path = filepath.Dir(path)
	}
	return exec.Command("xdg-open", path).Start()
}
