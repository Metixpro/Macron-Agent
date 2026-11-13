//go:build windows

package desk

import (
	"os"
	"os/exec"
	"syscall"

	"golang.org/x/crypto/ssh/agent"
)

func hideWindow(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
}

func sshAgent() agent.ExtendedAgent {
	f, err := os.OpenFile(`\\.\pipe\openssh-ssh-agent`, os.O_RDWR, 0)
	if err != nil {
		return nil
	}
	return agent.NewClient(f)
}

func wslAvailable() bool {
	_, err := exec.LookPath("wsl.exe")
	return err == nil
}
