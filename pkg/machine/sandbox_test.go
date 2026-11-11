package machine

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSandboxOffWords(t *testing.T) {
	for _, word := range []string{"off", "OFF", "0", "false", "no", "none", " False "} {
		if !offWord(word) {
			t.Errorf("offWord(%q) = false, want true", word)
		}
	}
	for _, word := range []string{"bwrap", "auto", "1", "true", "yes", "on"} {
		if offWord(word) {
			t.Errorf("offWord(%q) = true, want false", word)
		}
	}
}

func TestSandboxTopOf(t *testing.T) {
	cases := map[string]string{
		"/workspaces/s1":                    "/workspaces/s1",
		"/workspaces/s1/.agents/sub":        "/workspaces/s1",
		"/workspaces/s1/.agents/sub/deeper": "/workspaces/s1",
		`C:\workspaces\s1\.agents\sub`:      `C:\workspaces\s1`,
	}
	for in, want := range cases {
		if got := topOf(in); got != want {
			t.Errorf("topOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func containsSubslice(haystack, needle []string) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		if slices.Equal(haystack[i:i+len(needle)], needle) {
			return true
		}
	}
	return false
}

func TestSandboxWrapStructure(t *testing.T) {
	sb := &sandbox{
		bwrap:      "/usr/bin/bwrap",
		base:       "/base/workspaces",
		macronHome: "/home/dev/.macron",
		userHome:   "/home/dev",
		hide:       []string{"/home/dev/.ssh"},
		noNet:      true,
	}

	cmd := sb.wrap("/base/workspaces/session-1", "/base/workspaces/session-1", []string{"python3", "script.py"})
	if len(cmd) == 0 || cmd[0] != "/usr/bin/bwrap" {
		t.Fatalf("cmd[0] = %v, want /usr/bin/bwrap", cmd)
	}

	expectedSequences := [][]string{
		{"--ro-bind", "/", "/"},
		{"--dev", "/dev"},
		{"--proc", "/proc"},
		{"--tmpfs", "/dev/shm"},
		{"--tmpfs", "/tmp"},
		{"--bind", "/base/workspaces/session-1", "/base/workspaces/session-1"},
		{"--chdir", "/base/workspaces/session-1"},
		{"--unshare-pid"},
		{"--die-with-parent"},
		{"--new-session"},
		{"--unshare-net"},
		{"--", "python3", "script.py"},
	}

	for _, seq := range expectedSequences {
		if !containsSubslice(cmd, seq) {
			t.Errorf("missing expected sequence %v in command: %v", seq, cmd)
		}
	}
}

func TestMachineSandboxedDefault(t *testing.T) {
	m := &Machine{}
	if m.Sandboxed() {
		t.Error("new empty machine should not be sandboxed")
	}
	raw := []string{"echo", "hi"}
	if got := m.sandboxed("/tmp/ws", raw); !slices.Equal(got, raw) {
		t.Errorf("sandboxed without sandbox modified argv: %v", got)
	}
}

func TestSandboxExecutionOnLinux(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap not available on this machine")
	}

	tmpDir, err := os.MkdirTemp("", "macron-sb-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ws := tmpDir + "/ws"
	if err := os.MkdirAll(ws, 0o700); err != nil {
		t.Fatal(err)
	}

	m, err := New(Config{
		BaseDir: tmpDir,
		Sandbox: "bwrap",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !m.Sandboxed() {
		t.Fatal("expected machine to be sandboxed with bwrap")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// 1. Can write inside workspace
	res := m.runIn(ctx, ws, RunOptions{
		Shell: "echo 'hello sandbox' > test.txt && cat test.txt",
	})
	if res.ExitCode != 0 {
		t.Fatalf("run in workspace failed (code %d): %s %s", res.ExitCode, res.Error, res.Stderr)
	}
	if strings.TrimSpace(res.Stdout) != "hello sandbox" {
		t.Fatalf("unexpected stdout: %q", res.Stdout)
	}

	// 2. Cannot write to root filesystem
	res = m.runIn(ctx, ws, RunOptions{
		Shell: "echo 'blocked' > /test_leak.txt",
	})
	if res.ExitCode == 0 {
		t.Fatal("expected writing to / to fail inside sandbox")
	}
}
