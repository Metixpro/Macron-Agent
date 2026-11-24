package machine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

const (
	SandboxOff     = "off"
	SandboxBwrap   = "bwrap"
	SandboxWindows = "windows-job"
	SandboxAuto    = "auto"
)

var homeSecrets = []string{
	".ssh", ".gnupg", ".aws", ".azure", ".kube", ".docker", ".netrc", ".git-credentials", ".pgpass",
	".config/gh", ".config/gcloud", ".config/rclone",
}

var sandboxSecretEnvKeys = map[string]bool{
	"MACRON_API_KEY": true, "OPENAI_API_KEY": true, "ANTHROPIC_API_KEY": true,
	"GEMINI_API_KEY": true, "OPENROUTER_API_KEY": true, "DEEPSEEK_API_KEY": true,
	"TELEGRAM_BOT_TOKEN": true, "GITHUB_TOKEN": true, "GH_TOKEN": true,
	"AWS_SECRET_ACCESS_KEY": true, "AWS_SESSION_TOKEN": true, "AZURE_CLIENT_SECRET": true,
	"SLACK_BOT_TOKEN": true, "SLACK_WEBHOOK_URL": true, "DISCORD_BOT_TOKEN": true,
	"DISCORD_WEBHOOK_URL": true, "NOTION_TOKEN": true, "TRELLO_TOKEN": true,
	"TODOIST_API_TOKEN": true, "WAZUH_API_PASS": true, "GMAIL_REFRESH_TOKEN": true,
	"GMAIL_CLIENT_SECRET": true, "SMTP_PASS": true, "BALE_BOT_TOKEN": true,
	"RUBIKA_BOT_TOKEN": true, "EITAA_TOKEN": true, "WHATSAPP_ACCESS_TOKEN": true,
}

var homeCaches = []string{".cache", "go", ".npm", ".cargo/registry", ".cargo/git", ".rustup", ".m2", ".gradle", ".local/share/pnpm", ".bun/install"}

type sandbox struct {
	backend    string
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

func newSandbox(cfg Config) (*sandbox, string, error) {
	execMode := strings.ToLower(strings.TrimSpace(cfg.ExecutionMode))
	if execMode == "" {
		execMode = strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_EXECUTION_MODE")))
	}
	if execMode == "" {
		execMode = "best-effort"
	}
	switch execMode {
	case "local":
		return nil, "Execution mode is LOCAL. Sandbox is explicitly bypassed.", nil
	case "sandbox", "best-effort":
	default:
		return nil, "", fmt.Errorf("invalid execution mode %q (use local, sandbox, or best-effort)", execMode)
	}

	mode := strings.ToLower(strings.TrimSpace(cfg.Sandbox))
	if mode == "" {
		mode = strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_SANDBOX")))
	}
	if mode == "" {
		if runtime.GOOS == "windows" {
			mode = SandboxWindows
		} else if execMode == "sandbox" {
			mode = SandboxBwrap
		}
	}

	switch {
	case mode == "" || offWord(mode):
		if execMode == "sandbox" {
			return nil, "", errors.New("sandbox execution mode is required, but sandbox setting is 'off'")
		}
		return nil, "", nil
	case mode == SandboxWindows || mode == SandboxBwrap || mode == SandboxAuto || mode == "on" || mode == "1" || mode == "true" || mode == "yes":
	default:
		msg := "unknown sandbox " + strconv.Quote(mode) + " (use bwrap, windows-job, auto, or off)"
		if execMode == "sandbox" {
			return nil, "", fmt.Errorf("%s, but strict sandbox mode is required", msg)
		}
		return nil, msg + "; commands run without one", nil
	}

	// Native Windows Job-Object + Workspace-Isolated Sandbox (zero external dependencies)
	if runtime.GOOS == "windows" {
		sb := &sandbox{
			backend:    SandboxWindows,
			base:       cfg.BaseDir,
			macronHome: cfg.HomeDir,
			noNet:      cfg.SandboxNoNet || offWord(os.Getenv("MACRON_SANDBOX_NET")),
		}
		return sb, "", nil
	}

	bw, err := exec.LookPath("bwrap")
	if err != nil {
		msg := "bwrap is not installed (apt install bubblewrap)"
		if runtime.GOOS != "linux" {
			msg = "bwrap is not installed (bubblewrap sandbox is only available on Linux)"
		}
		if execMode == "sandbox" {
			return nil, "", fmt.Errorf("%s, but strict sandbox mode is required", msg)
		}
		return nil, msg + "; commands run without a sandbox", nil
	}

	sb := &sandbox{
		backend:    SandboxBwrap,
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
		msg := "bwrap failed self-test (" + why + ")"
		if execMode == "sandbox" {
			return nil, "", fmt.Errorf("%s, but strict sandbox mode is required", msg)
		}
		return nil, msg + "; commands run without a sandbox", nil
	}
	return sb, "", nil
}

func (sb *sandbox) wrap(top, dir string, argv []string) []string {
	if sb.backend != SandboxBwrap || sb.bwrap == "" {
		return argv
	}
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
	s.sbMu.RLock()
	sb := s.sb
	s.sbMu.RUnlock()
	if sb == nil {
		return argv
	}
	return sb.wrap(topOf(ws), ws, argv)
}

// SandboxBackend returns the active isolation backend ("bwrap", "windows-job", or "").
func (s *Machine) SandboxBackend() string {
	s.sbMu.RLock()
	sb := s.sb
	s.sbMu.RUnlock()
	if sb == nil {
		return ""
	}
	return sb.backend
}

// applySandboxEnv strips sensitive credential variables and isolates temp
// directories inside the active workspace when running in sandbox mode.
func applySandboxEnv(env map[string]string, ws, backend string) {
	for k := range env {
		if sandboxSecretEnvKeys[strings.ToUpper(k)] {
			delete(env, k)
		}
	}
	if ws != "" {
		tmpDir := filepath.Join(ws, ".macron-sandbox-tmp")
		if os.MkdirAll(tmpDir, 0o700) == nil {
			putEnv(env, "TEMP", tmpDir)
			putEnv(env, "TMP", tmpDir)
			putEnv(env, "TMPDIR", tmpDir)
			putEnv(env, "MPLCONFIGDIR", filepath.Join(tmpDir, "mpl"))
		}
	}
	putEnv(env, "MACRON_SANDBOX_ACTIVE", "1")
	if backend != "" {
		putEnv(env, "MACRON_SANDBOX_BACKEND", backend)
	}
}

func (s *Machine) Sandboxed() bool {
	s.sbMu.RLock()
	ok := s.sb != nil
	s.sbMu.RUnlock()
	return ok
}
