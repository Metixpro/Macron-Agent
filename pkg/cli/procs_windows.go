package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func windowsInstallationMatch(path, binary string) bool {
	if windowsBinaryMatches(path, binary) {
		return true
	}
	if !strings.EqualFold(filepath.Base(binary), "macron.exe") && !strings.EqualFold(filepath.Base(binary), "macron-desktop.exe") {
		return false
	}
	sibling := filepath.Join(filepath.Dir(binary), "macron.exe")
	if strings.EqualFold(filepath.Base(binary), "macron.exe") {
		sibling = filepath.Join(filepath.Dir(binary), "macron-desktop.exe")
	}
	return windowsBinaryMatches(path, sibling)
}

func stopInstallationProcesses(binary string) error {
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var errs []error
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if int(entry.ProcessID) == os.Getpid() {
			continue
		}
		h, openErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, entry.ProcessID)
		if openErr != nil {
			continue
		}
		path, imageErr := processImage(h)
		if imageErr == nil && windowsInstallationMatch(path, binary) {
			kill, killErr := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, entry.ProcessID)
			if killErr != nil {
				errs = append(errs, fmt.Errorf("stop %s: %w", path, killErr))
			} else {
				// Recheck the image on the termination handle to guard against PID reuse.
				image, imageErr := processImage(kill)
				if imageErr != nil || !windowsInstallationMatch(image, binary) {
					errs = append(errs, fmt.Errorf("process %d changed during uninstall", entry.ProcessID))
				} else if killErr := windows.TerminateProcess(kill, 0); killErr != nil {
					errs = append(errs, fmt.Errorf("stop %s: %w", image, killErr))
				} else if state, waitErr := windows.WaitForSingleObject(kill, 5000); waitErr != nil || state != uint32(windows.WAIT_OBJECT_0) {
					errs = append(errs, fmt.Errorf("process %d did not stop", entry.ProcessID))
				}
				windows.CloseHandle(kill)
			}
		}
		windows.CloseHandle(h)
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}
