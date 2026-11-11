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

const (
	SandboxOff   = "off"
	SandboxBwrap = "bwrap"
	SandboxAuto  = "auto"
)

var homeSecrets = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".netrc", ".git-credentials", ".pgpass",
	".config/gh", ".config/gcloud", ".config/rclone",
}

var homeCaches = []string{".cache", "go", ".npm", ".cargo/registry", ".cargo/git", ".rustup", ".m2", ".gradle", ".local/share/pnpm", ".bun/install"}

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

	for _, p := range sb.hide {
		switch fi, err := os.Stat(p); {
		case err != nil:
		case fi.IsDir():
			a = append(a, "--tmpfs", p)
		default:
			a = append(a, "--ro-bind", "/dev/null", p)
		}
	}

	if sb.macronHome != "" && isDir(sb.macronHome) {
		a = append(a, "--tmpfs", sb.macronHome)
		for _, sub := range []string{"venv", "skills", "plugins", "browsers", "browser-agent"} {
			p := filepath.Join(sb.macronHome, sub)
			if isDir(p) {
				a = append(a, "--ro-bind", p, p)
			}
		}
	}

	for _, p := range sb.ro {
		if pathExists(p) {
			a = append(a, "--ro-bind", p, p)
		}
	}

	for _, p := range sb.rw {
		if pathExists(p) {
			a = append(a, "--bind", p, p)
		}
	}
	if sb.userHome != "" {
		for _, c := range homeCaches {
			if p := filepath.Join(sb.userHome, c); isDir(p) {
				a = append(a, "--bind", p, p)
			}
		}
	}

	if sb.base != "" && top != sb.base && isDir(sb.base) {
		a = append(a, "--tmpfs", sb.base)
	}

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

func topOf(ws string) string {
	slash := strings.ReplaceAll(ws, "\\", "/")
	if i := strings.Index(slash, "/"+SubDir+"/"); i >= 0 {
		return ws[:i]
	}
	return ws
}

func (s *Machine) sandboxed(ws string, argv []string) []string {
	if s.sb == nil {
		return argv
	}
	return s.sb.wrap(topOf(ws), ws, argv)
}

func (s *Machine) Sandboxed() bool { return s.sb != nil }
