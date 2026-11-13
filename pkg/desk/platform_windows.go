//go:build windows

package desk

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"

	"golang.org/x/crypto/ssh/agent"
	"golang.org/x/sys/windows"
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

func reveal(path string, folder bool) error {
	if folder {
		cmd := exec.Command("explorer.exe")
		cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
		return cmd.Start()
	}
	verb, err := windows.UTF16PtrFromString("open")
	if err != nil {
		return err
	}
	file, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	dir, err := windows.UTF16PtrFromString(filepath.Dir(path))
	if err != nil {
		return err
	}
	return windows.ShellExecute(0, verb, file, nil, dir, windows.SW_SHOWNORMAL)
}
