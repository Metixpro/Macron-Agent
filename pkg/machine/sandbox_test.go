package machine

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func indexOfPair(args []string, a, b string) int {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == a && args[i+1] == b {
			return i
		}
	}
	return -1
}

func TestBwrapWrapMountOrder(t *testing.T) {
	root := t.TempDir()
	userHome := filepath.Join(root, "home")
	sshDir := filepath.Join(userHome, ".ssh")
	macronHome := filepath.Join(userHome, ".macron")
	baseDir := filepath.Join(macronHome, "workspaces")
	sessionWS := filepath.Join(baseDir, "s1")
	secretFile := filepath.Join(userHome, ".netrc")
	for _, d := range []string{sshDir, macronHome, sessionWS} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(secretFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	sb := &sandbox{
		backend:    SandboxBwrap,
		bwrap:      "/usr/bin/bwrap",
		base:       baseDir,
		macronHome: macronHome,
		userHome:   userHome,
		hide:       []string{sshDir, secretFile},
	}

	// Case 1: top == userHome (external project at $HOME)
	argsHome := sb.wrap(userHome, userHome, []string{"ls"})
	bindHome := indexOfPair(argsHome, "--bind", userHome)
	tmpMacron := indexOfPair(argsHome, "--tmpfs", macronHome)
	tmpSSH := indexOfPair(argsHome, "--tmpfs", sshDir)
	roNull := slices.Index(argsHome, "/dev/null")
	if bindHome < 0 || tmpMacron < 0 || tmpSSH < 0 || roNull < 0 {
		t.Fatalf("missing expected mounts in argsHome: %v", argsHome)
	}
	if !(bindHome < tmpMacron && bindHome < tmpSSH && bindHome < roNull) {
		t.Fatalf("userHome bind (%d) must precede macronHome (%d), ssh (%d), and /dev/null (%d): %v",
			bindHome, tmpMacron, tmpSSH, roNull, argsHome)
	}

	// Case 2: top == sessionWS inside baseDir inside macronHome
	argsSess := sb.wrap(sessionWS, sessionWS, []string{"ls"})
	tmpMacron = indexOfPair(argsSess, "--tmpfs", macronHome)
	tmpBase := indexOfPair(argsSess, "--tmpfs", baseDir)
	bindSess := indexOfPair(argsSess, "--bind", sessionWS)
	tmpSSH = indexOfPair(argsSess, "--tmpfs", sshDir)
	if tmpMacron < 0 || tmpBase < 0 || bindSess < 0 || tmpSSH < 0 {
		t.Fatalf("missing expected mounts in argsSess: %v", argsSess)
	}
	if !(tmpMacron < bindSess && tmpBase < bindSess && bindSess < tmpSSH) {
		t.Fatalf("expected macron/base tmpfs (%d,%d) < session bind (%d) < hide (%d): %v",
			tmpMacron, tmpBase, bindSess, tmpSSH, argsSess)
	}
}
