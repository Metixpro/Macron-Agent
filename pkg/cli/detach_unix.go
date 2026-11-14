//go:build unix

package cli

import (
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func terminate(pid int) error {
	p, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return p.Signal(syscall.SIGTERM)
}

func inputReady(d time.Duration) bool {
	fds := []unix.PollFd{{Fd: int32(os.Stdin.Fd()), Events: unix.POLLIN}}
	n, err := unix.Poll(fds, int(d.Milliseconds()))
	return err == nil && n > 0
}

func ownsBackgroundProcess(int) bool { return true }
