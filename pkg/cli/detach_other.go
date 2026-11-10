//go:build !unix

package cli

import (
	"errors"
	"syscall"
	"time"
)

func detached() *syscall.SysProcAttr { return nil }

func alive(int) bool { return false }

func terminate(int) error { return errors.New("not supported on this system") }

func inputReady(time.Duration) bool { return false }
