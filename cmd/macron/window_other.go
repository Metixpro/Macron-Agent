//go:build !windows

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func desktopNotify(string, string) {}

func showWindow(context.Context, string) bool { return false }

func openAppBrowser(address string) error {
	if runtime.GOOS == "darwin" {
		return exec.Command("open", address).Start()
	}
	for _, b := range []string{"google-chrome", "chromium", "chromium-browser", "microsoft-edge", "brave-browser"} {
		if p, err := exec.LookPath(b); err == nil {
			if err := exec.Command(p, "--app="+address, "--new-window").Start(); err == nil {
				return nil
			}
		}
	}
	return exec.Command("xdg-open", address).Start()
}

func alert(title, message string) {
	fmt.Fprintf(os.Stderr, "%s: %s\n", title, message)
}
