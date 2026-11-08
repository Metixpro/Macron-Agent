//go:build unix

package updater

import "syscall"

// detached starts a process in a session of its own: it outlives a restart of this one.
func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
