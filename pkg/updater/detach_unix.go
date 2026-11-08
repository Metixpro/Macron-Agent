//go:build unix

package updater

import "syscall"

func detached() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }
