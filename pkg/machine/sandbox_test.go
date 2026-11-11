package machine

import (
	"slices"
	"testing"
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
		"/workspaces/s1":                       "/workspaces/s1",
		"/workspaces/s1/.agents/sub":           "/workspaces/s1",
		"/workspaces/s1/.agents/sub/deeper":    "/workspaces/s1",
		`C:\workspaces\s1\.agents\sub`:        `C:\workspaces\s1`,
	}
	for in, want := range cases {
		if got := topOf(in); got != want {
			t.Errorf("topOf(%q) = %q, want %q", in, got, want)
		}
	}
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

	wantFlags := []string{
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/dev/shm",
		"--tmpfs", "/tmp",
		"--bind", "/base/workspaces/session-1", "/base/workspaces/session-1",
		"--chdir", "/base/workspaces/session-1",
		"--unshare-pid",
		"--die-with-parent",
		"--new-session",
		"--unshare-net",
		"--", "python3", "script.py",
	}

	for i := 0; i < len(wantFlags)-1; i += 2 {
		flag, val := wantFlags[i], wantFlags[i+1]
		idx := slices.Index(cmd, flag)
		if idx < 0 || idx+1 >= len(cmd) || cmd[idx+1] != val {
			t.Errorf("missing or mismatched flag pair: %s %s in %v", flag, val, cmd)
		}
	}

	last3 := cmd[len(cmd)-3:]
	if last3[0] != "--" || last3[1] != "python3" || last3[2] != "script.py" {
		t.Errorf("tail = %v, want [--, python3, script.py]", last3)
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
