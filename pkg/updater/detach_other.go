//go:build !unix && !windows

package updater

import "syscall"

func detached() *syscall.SysProcAttr { return nil }
