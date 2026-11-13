//go:build unix

package machine

import (
	"os/exec"
	"strings"
	"sync"
	"syscall"
)

const (
	useGoShell = false
	foldEnv    = false
)

func ShellName() string { return "bash" }

func shellArgv(script string) ([]string, func(), error) {
	return []string{"bash", "-c", script}, func() {}, nil
}

func argsScript(args []string) string {
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	return "exec " + strings.Join(quoted, " ")
}

func resolveArgv(argv []string) []string { return argv }

func available(program string) bool {
	if program == "powershell" {
		return PowerShell() != ""
	}
	_, err := exec.LookPath(program)
	return err == nil
}

func PowerShell() string {
	if p, err := exec.LookPath("pwsh"); err == nil {
		return p
	}
	return ""
}

func prepare(*exec.Cmd, bool) {}

func groupAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

type tree struct {
	mu   sync.Mutex
	pid  int
	done bool
}

func startTracked(cmd *exec.Cmd) (*tree, error) {
	return nil, cmd.Start()
}

func startGroup(cmd *exec.Cmd) (*tree, error) {
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &tree{pid: cmd.Process.Pid}, nil
}

func (t *tree) kill() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.done && t.pid > 0 {
		_ = syscall.Kill(-t.pid, syscall.SIGKILL)
	}
}

func (t *tree) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	t.done = true
	t.mu.Unlock()
}

func platformHostEnv(env map[string]string) {
	if env["PATH"] == "" {
		env["PATH"] = defaultPath
	}
	if _, ok := env["LANG"]; !ok {
		env["LANG"] = "C.UTF-8"
	}
}

func platformDefaults(env map[string]string) {
	env["LC_ALL"] = "C.UTF-8"
}

const defaultPath = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?&|;<>(){}[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
