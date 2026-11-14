package cli

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

func detached() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW}
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return err == windows.ERROR_ACCESS_DENIED
	}
	defer windows.CloseHandle(h)
	state, err := windows.WaitForSingleObject(h, 0)
	return err == nil && state == uint32(windows.WAIT_TIMEOUT)
}

func processImage(h windows.Handle) (string, error) {
	buf := make([]uint16, 32768)
	n := uint32(len(buf))
	err := windows.QueryFullProcessImageName(h, 0, &buf[0], &n)
	if err != nil {
		return "", err
	}
	return windows.UTF16ToString(buf[:n]), nil
}

func windowsBinaryMatches(path, binary string) bool {
	path, binary = filepath.Clean(path), filepath.Clean(binary)
	return strings.EqualFold(path, binary) || strings.EqualFold(path, binary+".old") || strings.EqualFold(path, binary+".next")
}

func ownsBackgroundProcess(pid int) bool {
	if pid <= 0 || pid == os.Getpid() {
		return false
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(h)
	path, err := processImage(h)
	bin, binErr := selfBinary()
	return err == nil && binErr == nil && windowsBinaryMatches(path, bin)
}

func terminate(pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return windows.ERROR_INVALID_PARAMETER
	}
	h, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	// Check again on the handle being terminated: a stale PID must not kill
	// another program if Windows has reused the process id.
	path, err := processImage(h)
	bin, binErr := selfBinary()
	if err != nil {
		return err
	}
	if binErr != nil {
		return binErr
	}
	if !windowsBinaryMatches(path, bin) {
		return windows.ERROR_ACCESS_DENIED
	}
	return windows.TerminateProcess(h, 0)
}

func inputReady(d time.Duration) bool {
	h := windows.Handle(os.Stdin.Fd())
	state, err := windows.WaitForSingleObject(h, uint32(d.Milliseconds()))
	return err == nil && state == uint32(windows.WAIT_OBJECT_0)
}
