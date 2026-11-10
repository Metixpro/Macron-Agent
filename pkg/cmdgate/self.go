package cmdgate

import (
	"path"
	"regexp"
	"strings"
)

func (env Env) expand(w string) string {
	w = strings.TrimSpace(w)
	if env.UserHome != "" {
		for _, p := range []string{"~", "$HOME", "${HOME}"} {
			if w == p || strings.HasPrefix(w, p+"/") {
				w = env.UserHome + strings.TrimPrefix(w, p)
				break
			}
		}
	}
	if strings.HasPrefix(w, "/") {
		return path.Clean(w)
	}
	return w
}

func (env Env) tail(secret string) string {
	if env.SelfHome == "" || !strings.HasPrefix(secret, env.SelfHome+"/") {
		return ""
	}
	name := path.Base(env.SelfHome)
	if !strings.HasPrefix(name, ".") && !strings.Contains(strings.ToLower(name), "macron") {
		return ""
	}
	return name + strings.TrimPrefix(secret, env.SelfHome)
}

func (env Env) secretText(text string) bool {
	for _, s := range env.SelfSecrets {
		if s == "" {
			continue
		}
		if strings.Contains(text, s) {
			return true
		}
		if t := env.tail(s); t != "" && strings.Contains(text, t) {
			return true
		}
	}
	return false
}

func (env Env) secretRef(w string) bool {
	if len(env.SelfSecrets) == 0 || w == "" {
		return false
	}
	if env.secretText(w) {
		return true
	}
	x := env.expand(w)
	for _, s := range env.SelfSecrets {
		if s == "" {
			continue
		}
		if x == s {
			return true
		}
		if strings.ContainsAny(x, "*?[") {
			if ok, _ := path.Match(x, s); ok {
				return true
			}
		}
	}
	return false
}

func (env Env) homeRef(w string) bool {
	if env.SelfHome == "" {
		return false
	}
	return env.expand(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(w), "*"), "/")) == env.SelfHome
}

func (env Env) dbText(text string) bool {
	for _, db := range env.SelfDB {
		if db == "" {
			continue
		}
		if strings.Contains(text, db) {
			return true
		}
		if t := env.tail(db); t != "" && strings.Contains(text, t) {
			return true
		}
	}
	return false
}

func (env Env) outside(file string) bool {
	if file == "" || file == "-" {
		return false
	}
	if strings.Contains(file, "..") {
		return true
	}
	x := env.expand(file)
	if strings.HasPrefix(x, "~") || strings.HasPrefix(x, "$") {
		return true
	}
	if !strings.HasPrefix(x, "/") {
		return false
	}
	for _, ok := range []string{"/workspace", "/tmp", "/var/tmp", env.WorkDir} {
		if ok != "" && (x == ok || strings.HasPrefix(x, ok+"/")) {
			return false
		}
	}
	return x != "/dev/null" && x != "/dev/stdin"
}

func sensitiveKey(k string) bool {
	if gateKey(k) {
		return true
	}
	u := strings.ToUpper(k)
	for _, w := range []string{"OWNERS", "TOKEN", "SECRET", "PASSWORD", "API_KEY", "API_URL", "TELEGRAM_API", "_HOME", "ENV_FILE", "_DB",
		"PROFILE", "WEBHOOK", "DASHBOARD"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

func secretValue(k string) bool {
	u := strings.ToUpper(k)
	for _, w := range []string{"TOKEN", "SECRET", "PASSWORD", "API_KEY"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

func hasFlag(flags []string, names ...string) bool {
	for _, f := range flags {
		f = strings.TrimLeft(f, "-")
		if i := strings.IndexByte(f, '='); i >= 0 {
			f = f[:i]
		}
		for _, n := range names {
			if f == n {
				return true
			}
		}
	}
	return false
}

func (env Env) selfCommand(flags, ops []string, add func(Level, string, string, ...string)) {
	if len(ops) == 0 {
		if !hasFlag(flags, "check", "help", "h", "version") {
			add(Dangerous, "self-manage", "starting a second copy of the agent")
		}
		return
	}
	at := func(i int) string {
		if i < len(ops) {
			return ops[i]
		}
		return ""
	}
	switch ops[0] {
	case "version", "help", "doctor", "logs":
	case "uninstall":
		if !hasFlag(flags, "dry-run") {
			add(Dangerous, "self-manage", "removing the agent and all of its data")
		}
	case "service":
		switch at(1) {
		case "status":
		case "stop", "uninstall":
			add(Dangerous, "self-manage", "stopping the agent's own service")
		default:
			add(Dangerous, "self-manage", "restarting or reinstalling the agent's own service")
		}
	case "config":
		switch at(1) {
		case "path", "list":
		case "get":
			if secretValue(at(2)) {
				add(Dangerous, "self-manage", "printing one of the agent's own keys")
			}
		case "set", "unset":
			switch {
			case gateKey(at(2)):
				add(Hardline, "self-protect", "changing the command gate from inside a job")
			case sensitiveKey(at(2)):
				add(Dangerous, "self-manage", "changing the agent's owners, keys or addresses")
			default:
				add(Dangerous, "self-manage", "changing the agent's own settings")
			}
		default:
			add(Dangerous, "self-manage", "changing the agent's own settings")
		}
	case "update":
		if !hasFlag(flags, "check") {
			add(Dangerous, "self-manage", "updating the agent itself")
		}
	default:
		add(Dangerous, "self-manage", "managing the agent's own installation («"+shortWord(ops[0])+"»)")
	}
}

func shortWord(s string) string {
	if r := []rune(s); len(r) > 24 {
		return string(r[:24]) + "…"
	}
	return s
}

var copiers = map[string]bool{"tar": true, "zip": true, "7z": true, "7zz": true, "7za": true, "cp": true, "rsync": true, "scp": true,
	"rclone": true, "cat": true, "base64": true, "xxd": true, "gzip": true}

var interpreters = map[string]bool{"python": true, "perl": true, "ruby": true, "node": true, "nodejs": true, "php": true, "deno": true,
	"bun": true, "lua": true}

var fetchers = map[string]bool{"curl": true, "wget": true, "fetch": true, "nc": true, "ncat": true, "base64": true, "xxd": true}

func interpreter(name string) bool {
	return interpreters[strings.TrimRight(strings.ToLower(name), "0123456789.")]
}

func fetched(stages []Cmd) string {
	for _, c := range stages {
		if n, _, _ := unwrap(c.Words); fetchers[strings.ToLower(n)] {
			return n
		}
	}
	return ""
}

func uploads(name string, args []string) []string {
	var out []string
	fromData := func(v string, form bool) {
		if form {
			_, v, _ = strings.Cut(v, "=")
			if !strings.HasPrefix(v, "@") && !strings.HasPrefix(v, "<") {
				return
			}
			v = v[1:]
			if i := strings.IndexByte(v, ';'); i >= 0 {
				v = v[:i]
			}
			out = append(out, v)
			return
		}
		if i := strings.IndexByte(v, '@'); i >= 0 && (i == 0 || !strings.ContainsAny(v[:i], " {\"'")) {
			out = append(out, v[i+1:])
		}
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		flag, val, joined := a, "", false
		if strings.HasPrefix(a, "--") {
			if f, v, ok := strings.Cut(a, "="); ok {
				flag, val, joined = f, v, true
			}
		} else if strings.HasPrefix(a, "-") && len(a) > 2 && (a[1] == 'd' || a[1] == 'F' || a[1] == 'T') {
			flag, val, joined = a[:2], a[2:], true
		}
		next := func() string {
			if joined {
				return val
			}
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		if name == "wget" {
			if flag == "--post-file" || flag == "--body-file" {
				out = append(out, next())
			}
			continue
		}
		switch flag {
		case "-d", "--data", "--data-binary", "--data-ascii", "--data-urlencode", "--json":
			fromData(next(), false)
		case "-F", "--form":
			fromData(next(), true)
		case "-T", "--upload-file":
			out = append(out, next())
		}
	}
	return out
}

func (env Env) uploadCheck(name string, args []string, c Cmd, before []Cmd, add func(Level, string, string, ...string)) {
	const reason = "sending a file from outside the work folder to another server"
	for _, f := range uploads(name, args) {
		switch {
		case env.outside(f):
			add(Dangerous, "upload-file", reason, f)
		case f == "-" || f == "." || f == "":
			for _, r := range c.Redirs {
				if strings.HasPrefix(strings.TrimLeft(r.Op, "0123456789"), "<") && env.outside(r.Target) {
					add(Dangerous, "upload-file", reason, r.Target)
				}
			}
			for _, st := range before {
				for _, w := range st.Words {
					if env.outside(w) {
						add(Dangerous, "upload-file", reason, w)
					}
				}
			}
		}
	}
}

func findStarts(args []string) (starts []string, deletes bool) {
	expr := false
	for i, a := range args {
		if !expr && (strings.HasPrefix(a, "-") || a == "(" || a == "!") {
			if a == "-H" || a == "-L" || a == "-P" {
				continue
			}
			expr = true
		}
		if !expr {
			starts = append(starts, a)
			continue
		}
		if a == "-delete" {
			deletes = true
		}
		if (a == "-exec" || a == "-execdir" || a == "-ok") && i+1 < len(args) {
			switch base(args[i+1]) {
			case "rm", "shred", "unlink":
				deletes = true
			}
		}
	}
	return starts, deletes
}

func (env Env) findsSelf(nested []string) bool {
	for _, n := range nested {
		for _, pl := range Parse(n).Pipelines {
			for _, c := range pl {
				name, args, _ := unwrap(c.Words)
				if name != "pgrep" && name != "pidof" {
					continue
				}
				for _, a := range args {
					if env.self(a) {
						return true
					}
				}
			}
		}
	}
	return false
}

func selfCall(name string) *regexp.Regexp {
	return regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `(?:\.service)?["'\s,\]\[]+(?:--?[\w-]+["'\s,]+)*(uninstall|service|config|update|exec|backup|setup)\b`)
}

func AnalyzeCode(code string, env Env) []Finding {
	text := Clean(code)
	var out []Finding
	if env.secretText(text) {
		out = append(out, Finding{Level: Dangerous, Rule: "self-keys", Reason: "code that touches the agent's own keys and settings file"})
	}
	if env.dbText(text) {
		out = append(out, Finding{Level: Dangerous, Rule: "self-data", Reason: "code that opens the agent's own database"})
	}
	for _, n := range env.SelfNames {
		if n != "" && selfCall(n).MatchString(text) {
			out = append(out, Finding{Level: Dangerous, Rule: "self-manage", Reason: "code that runs the agent's own program to manage it"})
			break
		}
	}
	sortFindings(out)
	return out
}
