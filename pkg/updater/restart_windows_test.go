package updater

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsDelayedRestartWithoutUnixShell(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "تست O'Brien $name")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	// The helper records the arguments instead of restarting a real installation.
	script := filepath.Join(dir, "macron.cmd")
	if err := os.WriteFile(script, []byte("@echo off\r\necho %1 %2> \"%~dp0called.txt\"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0"))
	how, err := RestartLater(script, false)
	if err != nil || how != "Windows background restart" {
		t.Fatalf("%s %v", how, err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(filepath.Join(dir, "called.txt"))
		if err == nil {
			if strings.TrimSpace(string(data)) != "service restart" {
				t.Fatalf("wrong arguments: %q", data)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("the Windows restart helper did not run")
}
