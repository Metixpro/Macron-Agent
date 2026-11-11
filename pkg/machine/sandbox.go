package machine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sandbox modes for Config.Sandbox (MACRON_SANDBOX when it is empty).
const (
	SandboxOff   = "off"
	SandboxBwrap = "bwrap"
	SandboxAuto  = "auto"
)

// homeSecrets are paths under the user's home that sandbox commands never need to see.
var homeSecrets = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".netrc", ".git-credentials", ".pgpass",
	".config/gh", ".config/gcloud", ".config/rclone",
}

// sandbox runs commands with bubblewrap: the system read-only, the user's home read-only without secrets,
// the app's secret configuration hidden, other sessions' workspaces invisible,
// only the running session's workspace writable, and an isolated PID/IPC namespace.
type sandbox struct {
	bwrap      string
	base       string
	macronHome string
	userHome   string
	ro         []string
	rw         []string
	hide       []string
	noNet      bool
}

func offWord(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "off", "0", "false", "no", "none":
		return true
	}
	return false
}

func absAll(paths []string) []string {
	var out []string
	for _, p := range paths {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if a, err := filepath.Abs(p); err == nil {
			out = append(out, a)
		}
	}
	return out
}

// newSandbox returns nil when commands run without a sandbox, and a note for the log when one was asked for
// but cannot be used.
func newSandbox(cfg Config) (*sandbox, string) {
	mode := strings.ToLower(strings.TrimSpace(cfg.Sandbox))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_SANDBOX")))
	}
	switch {
	case mode == "" || offWord(mode):
		return nil, ""
	case mode == SandboxBwrap || mode == SandboxAuto || mode == "on" || mode == "1" || mode == "true" || mode == "yes":
	default:
		return nil, "unknown sandbox " + strconv.Quote(mode) + " (use bwrap or off); commands run without one"
	}
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, "bwrap is not installed (apt install bubblewrap); commands run without a sandbox"
	}
	sb := &sandbox{
		bwrap:      bw,
		base:       cfg.BaseDir,
		macronHome: cfg.HomeDir,
		noNet:      cfg.SandboxNoNet || offWord(os.Getenv("MACRON_SANDBOX_NET")),
	}
	if h, err := os.UserHomeDir(); err == nil && h != "/" {
		sb.userHome = h
		for _, p := range homeSecrets {
			full := filepath.Join(h, p)
			if pathExists(full) {
				sb.hide = append(sb.hide, full)
			}
		}
	}
	sb.ro, sb.rw = absAll(cfg.ReadOnly), absAll(cfg.Writable)
	sb.hide = append(sb.hide, absAll(cfg.Hide)...)
	if rt := os.Getenv("XDG_RUNTIME_DIR"); rt != "" {
		sb.hide = append(sb.hide, rt)
	} else if uid := os.Getuid(); uid >= 0 {
		runUser := "/run/user/" + strconv.Itoa(uid)
		if pathExists(runUser) {
			sb.hide = append(sb.hide, runUser)
		}
	}

	// Self-test bubblewrap with a probe command.
	probeDir, err := os.MkdirTemp(cfg.BaseDir, ".probe-bwrap-")
	if err != nil {
		probeDir = os.TempDir()
	} else {
		defer os.RemoveAll(probeDir)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	probe := sb.wrap(probeDir, probeDir, []string{"true"})
	if out, err := exec.CommandContext(ctx, probe[0], probe[1:]...).CombinedOutput(); err != nil {
		why := strings.TrimSpace(string(out))
		if why == "" {
			why = err.Error()
		}
		return nil, "bwrap failed self-test (" + why + "); commands run without a sandbox"
	}
	return sb, ""
}

// wrap returns argv run in the sandbox, with top (the session's workspace) writable and dir as the folder it starts in.
func (sb *sandbox) wrap(top, dir string, argv []string) []string {
	a := []string{
		sb.bwrap,
		"--ro-bind", "/", "/",
		"--dev", "/dev",
		"--proc", "/proc",
		"--tmpfs", "/dev/shm",
		"--tmpfs", "/tmp",
	}
	if isDir("/var/tmp") {
		a = append(a, "--tmpfs", "/var/tmp")
	}

	// Hide home secrets (like ~/.ssh, ~/.gnupg, ~/.aws, etc.).
	for _, p := range sb.hide {
		switch fi, err := os.Stat(p); {
		case err != nil:
		case fi.IsDir():
			a = append(a, "--tmpfs", p)
		default:
			a = append(a, "--ro-bind", "/dev/null", p)
		}
	}

	// If Macron home directory exists (e.g. ~/.macron), isolate it with tmpfs so secrets
	// (config.env, secret.key, macron.db) are completely inaccessible.
	// Selectively re-expose read-only tool directories if present.
	if sb.macronHome != "" && isDir(sb.macronHome) {
		a = append(a, "--tmpfs", sb.macronHome)
		for _, sub := range []string{"venv", "skills", "plugins", "browsers", "browser-agent"} {
			p := filepath.Join(sb.macronHome, sub)
			if isDir(p) {
				a = append(a, "--ro-bind", p, p)
			}
		}
	}

	// Custom read-only binds.
	for _, p := range sb.ro {
		if pathExists(p) {
			a = append(a, "--ro-bind", p, p)
		}
	}

	// Custom read-write binds.
	for _, p := range sb.rw {
		if pathExists(p) {
			a = append(a, "--bind", p, p)
		}
	}

	// If base workspaces directory is outside Macron home and not top itself, hide sibling sessions.
	if sb.base != "" && top != sb.base && isDir(sb.base) {
		a = append(a, "--tmpfs", sb.base)
	}

	// Mount the session's workspace as writable and enter it.
	a = append(a, "--bind", top, top, "--chdir", dir, "--unshare-pid", "--die-with-parent", "--new-session")
	if sb.noNet {
		a = append(a, "--unshare-net")
	}
	return append(append(a, "--"), argv...)
}

func isDir(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && fi.IsDir()
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// topOf returns the session's own root workspace for ws, which may be a sub-agent's folder inside it.
func topOf(ws string) string {
	slash := filepath.ToSlash(ws)
	if i := strings.Index(slash, "/"+SubDir+"/"); i >= 0 {
		return ws[:i]
	}
	return ws
}

// sandboxed returns argv as it runs in ws: inside the sandbox when one is configured.
func (s *Machine) sandboxed(ws string, argv []string) []string {
	if s.sb == nil {
		return argv
	}
	return s.sb.wrap(topOf(ws), ws, argv)
}

// Sandboxed reports whether commands run in a sandbox.
func (s *Machine) Sandboxed() bool { return s.sb != nil }
