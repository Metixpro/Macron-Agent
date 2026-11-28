package updater

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

const (
	windowsCLI     = "macron.exe"
	windowsDesktop = "macron-desktop.exe"
)

type binaryUpdate struct {
	target string
	next   string
}

func installBinaries(ctx context.Context, self string, bin, archive []byte, version string, report func(string, string, int)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := filepath.Dir(self)
	lock, err := lockUpdate(dir)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(lock)

	cliTarget, desktopTarget := self, filepath.Join(dir, windowsDesktop)
	desktopEntry := strings.Contains(strings.ToLower(filepath.Base(self)), "desktop")
	if desktopEntry {
		cliTarget, desktopTarget = filepath.Join(dir, windowsCLI), self
	}
	sibling := desktopTarget
	if desktopEntry {
		sibling = cliTarget
	}
	info, err := os.Lstat(sibling)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if desktopEntry {
			cliTarget = ""
		} else {
			desktopTarget = ""
		}
	case err != nil:
		return fmt.Errorf("inspect installed executable %s: %w", sibling, err)
	case !info.Mode().IsRegular():
		return fmt.Errorf("installed executable %s is not a regular file", sibling)
	}

	stage, err := makeStage(dir)
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	cliNext := filepath.Join(stage, windowsCLI)
	if err := os.WriteFile(cliNext, bin, 0o755); err != nil {
		return err
	}
	var updates []binaryUpdate
	if cliTarget != "" {
		updates = append(updates, binaryUpdate{target: cliTarget, next: cliNext})
	}
	var desktopNext string
	if desktopTarget != "" {
		desktop, err := extractFile(archive, windowsDesktop)
		if err != nil {
			return err
		}
		desktopNext = filepath.Join(stage, windowsDesktop)
		if err := os.WriteFile(desktopNext, desktop, 0o755); err != nil {
			return err
		}
		updates = append(updates, binaryUpdate{target: desktopTarget, next: desktopNext})
	}

	report("preflight", "Verifying update binaries", 93)
	if desktopNext != "" {
		if err := verifyVersion(desktopNext, version); err != nil {
			return err
		}
	}
	checkCLI := Preflight
	if desktopEntry {
		// The service startup check opens the configured database. A desktop
		// update only needs to verify that both executables report the release.
		checkCLI = verifyVersion
	}
	if err := checkCLI(cliNext, version); err != nil {
		return err
	}
	report("installing", "Installing update", 97)
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceBinaries(updates)
}

func swapBinary(self, next string) error {
	lock, err := lockUpdate(filepath.Dir(self))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(lock)
	return replaceBinaries([]binaryUpdate{{target: self, next: next}})
}

func lockUpdate(dir string) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(filepath.Join(dir, ".macron-update.lock"))
	if err != nil {
		return windows.InvalidHandle, err
	}
	lock, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_TEMPORARY|windows.FILE_FLAG_DELETE_ON_CLOSE, 0)
	if errors.Is(err, windows.ERROR_FILE_EXISTS) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
		return windows.InvalidHandle, errors.New("another update is already installing in this folder")
	}
	if err != nil {
		return windows.InvalidHandle, fmt.Errorf("lock the installation folder %s: %w", dir, err)
	}
	return lock, nil
}

func replaceBinaries(updates []binaryUpdate) (err error) {
	backedUp := 0
	installed := 0
	defer func() {
		if err == nil {
			return
		}
		for i := installed - 1; i >= 0; i-- {
			target := updates[i].target
			next := updates[i].next
			if revertErr := os.Rename(target, next); revertErr != nil {
				if remErr := os.Remove(target); remErr != nil {
					err = errors.Join(err, fmt.Errorf("revert %s: %w", target, revertErr))
				}
			}
		}
		for i := backedUp - 1; i >= 0; i-- {
			target := updates[i].target
			backup := target + ".old"
			if restoreErr := os.Rename(backup, target); restoreErr != nil {
				err = errors.Join(err, fmt.Errorf("restore %s from %s: %w", target, backup, restoreErr))
			}
		}
	}()

	for _, update := range updates {
		if err := os.Rename(update.target, update.target+".old"); err != nil {
			return fmt.Errorf("back up %s: %w", update.target, err)
		}
		backedUp++
	}
	for _, update := range updates {
		if err := os.Rename(update.next, update.target); err != nil {
			return fmt.Errorf("install %s: %w", update.target, err)
		}
		installed++
	}
	return nil
}
