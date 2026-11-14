package tui

import (
	"runtime"
	"testing"
)

func TestShellHeadingMatchesPlatform(t *testing.T) {
	want := "Bash"
	if runtime.GOOS == "windows" {
		want = "PowerShell"
	}
	title, arg := ToolHeading("bash_execute", `{"command":"echo test"}`)
	if title != want || arg != "echo test" {
		t.Fatal(title, arg)
	}
}
