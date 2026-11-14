package machine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWindowsPowerShellUnicodeExitCodesAndTimeout(t *testing.T) {
	m, err := New(Config{BaseDir: filepath.Join(t.TempDir(), "پروژه آزمایشی"), Sandbox: "off"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command, output string
		code                  int
		timeout               time.Duration
	}{
		{"unicode", "[IO.File]::WriteAllText('فارسی.txt','سلام دنیا'); Get-Content 'فارسی.txt'", "سلام دنیا", 0, 10 * time.Second},
		{"native exit", "cmd /c exit 7", "", 7, 10 * time.Second},
		{"cmdlet error", "Get-Item -LiteralPath './missing-qa-141'", "", 1, 10 * time.Second},
		{"timeout", "Start-Sleep 60", "", 124, time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := m.Execute(context.Background(), tc.name, tc.command, tc.timeout)
			if r.ExitCode != tc.code || strings.TrimSpace(r.Stdout) != tc.output {
				t.Fatalf("%+v", r)
			}
		})
	}
}
func TestWindowsPowerShellFallback(t *testing.T) {
	if os.Getenv("MACRON_TEST_PS_FALLBACK") != "1" {
		if _, err := exec.LookPath("powershell.exe"); err != nil {
			t.Skip("Windows PowerShell is not installed")
		}
		var kept []string
		for _, p := range filepath.SplitList(os.Getenv("PATH")) {
			if _, err := os.Stat(filepath.Join(p, "pwsh.exe")); err != nil {
				kept = append(kept, p)
			}
		}
		t.Setenv("PATH", strings.Join(kept, string(os.PathListSeparator)))
		t.Setenv("MACRON_TEST_PS_FALLBACK", "1")
		// Shell selection is cached per process, just as it is for a fresh app launch.
		out, err := exec.Command(os.Args[0], "-test.run=^TestWindowsPowerShellFallback$", "-test.v").CombinedOutput()
		if err != nil {
			t.Fatalf("fresh-process fallback: %v\n%s", err, out)
		}
		return
	}
	m, err := New(Config{BaseDir: t.TempDir(), Sandbox: "off"})
	if err != nil {
		t.Fatal(err)
	}
	r := m.Execute(context.Background(), "fallback", "$PSVersionTable.PSVersion.Major", 10*time.Second)
	if r.ExitCode != 0 || strings.TrimSpace(r.Stdout) != "5" {
		t.Fatalf("fallback: %+v", r)
	}
}
