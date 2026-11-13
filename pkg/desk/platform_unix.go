//go:build !windows

package desk

import (
	"net"
	"os"
	"os/exec"

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
