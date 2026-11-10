package machine

import (
	"os"
	"os/user"
	"regexp"
	"strings"
)

var reMount = regexp.MustCompile(`/workspace\b`)

func (s *Machine) hostPath(ws, text string) string {
	if !strings.Contains(text, MountPoint) {
		return text
	}
	return reMount.ReplaceAllLiteralString(text, ws)
}

func (s *Machine) hostArgv(ws string, argv []string) []string {
	out := make([]string, len(argv))
	for i, a := range argv {
		out[i] = s.hostPath(ws, a)
	}
	return out
}

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "this user"
}

var secretEnv = regexp.MustCompile(`(?i)(^MACRON_)|(TOKEN|SECRET|PASSWORD|PASSWD|API_?KEY|DSN|CREDENTIAL)`)

func hostEnv() map[string]string {
	env := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k == "" || secretEnv.MatchString(k) {
			continue
		}
		env[k] = v
	}
	if env["PATH"] == "" {
		env["PATH"] = defaultPath
	}
	if home, err := os.UserHomeDir(); err == nil && env["HOME"] == "" {
		env["HOME"] = home
	}
	if _, ok := env["LANG"]; !ok {
		env["LANG"] = "C.UTF-8"
	}

	env["PAGER"], env["GIT_PAGER"] = "cat", "cat"
	return env
}
