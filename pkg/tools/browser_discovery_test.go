package tools

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestFindChromeInStandardUserCache(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("XDG cache layout")
	}
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	cache := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", cache)
	old := chromeDirs
	oldBins := chromeBinaries
	chromeDirs = nil
	chromeBinaries = nil
	defer func() { chromeDirs = old; chromeBinaries = oldBins }()
	binary := filepath.Join(cache, "ms-playwright", "chromium_headless_shell-9999", "chrome-headless-shell-linux64", "chrome-headless-shell")
	if err := os.MkdirAll(filepath.Dir(binary), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if got := findChrome(); got != binary {
		t.Fatalf("browser cache not detected: %q", got)
	}
}
