package cmdgate

import (
	"path"
	"regexp"
	"strings"
)

type Level int

const (
	Safe Level = iota
	Dangerous
	Hardline
)

type Finding struct {
	Level  Level
	Rule   string
	Reason string
	Paths  []string
}

type Env struct {
	SudoPassword bool
	SelfNames    []string
	SelfDB       []string
	SelfPID      int
}

const maxDepth = 6

func Analyze(command string, env Env) []Finding {
	var out []Finding
	analyze(command, env, 0, &out)

	masked := maskQuotes(Clean(command))
	if forkBombRe.MatchString(strings.Join(strings.Fields(masked), "")) || forkBombRe.MatchString(masked) {
		out = append(out, Finding{Level: Hardline, Rule: "fork-bomb", Reason: "a fork bomb"})
	}
	if blockDevRedirRe.MatchString(masked) {
		out = append(out, Finding{Level: Hardline, Rule: "write-block-device", Reason: "writing straight onto a disk device"})
	}
	sortFindings(out)
	return out
}

func sortFindings(f []Finding) {
	for i := 1; i < len(f); i++ {
		for j := i; j > 0 && f[j].Level > f[j-1].Level; j-- {
			f[j], f[j-1] = f[j-1], f[j]
		}
	}
}

func Worst(f []Finding) Level {
	if len(f) == 0 {
		return Safe
	}
	return f[0].Level
}

var (
	forkBombRe      = regexp.MustCompile(`:\(\)\{:\|:&\};:|(\w+)\(\)\s*\{\s*(\w+)\s*\|\s*(\w+)\s*&\s*\}`)
	blockDevRedirRe = regexp.MustCompile(`>\s*/dev/(?:sd|nvme|hd|mmcblk|vd|xvd)[a-z0-9]*`)
	blockDevRe      = regexp.MustCompile(`^/dev/(?:sd|nvme|hd|mmcblk|vd|xvd)[a-z0-9]*$`)
	sqlDangerRe     = regexp.MustCompile(`(?is)\bdrop\s+(?:table|database|schema)\b|\btruncate\s+(?:table\s+)?\w|\bdelete\s+from\s+[\w."\x60]+\s*(?:;|$)`)
	sqlWriteRe      = regexp.MustCompile(`(?i)\b(?:insert|update|delete|replace|drop|alter)\b`)
	gateTablesRe    = regexp.MustCompile(`(?i)\b(?:settings|cmd_allow|cmd_audit)\b`)
)

func maskQuotes(s string) string {
	r := []rune(s)
	var quote rune
	for i := 0; i < len(r); i++ {
		c := r[i]
		switch {
		case quote != 0:
			if c == '\\' && quote == '"' && i+1 < len(r) {
				r[i], r[i+1] = ' ', ' '
				i++
				continue
			}
			if c == quote {
				quote = 0
				continue
			}
			if c != '\n' {
				r[i] = ' '
			}
		case c == '\\' && i+1 < len(r):
			i++
		case c == '\'' || c == '"':
			quote = c
		}
	}
	return string(r)
}

var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "ksh": true, "dash": true, "ash": true, "fish": true, "busybox-sh": true}

var wrappers = map[string]bool{"sudo": true, "doas": true, "env": true, "nohup": true, "nice": true, "ionice": true, "time": true,
	"timeout": true, "stdbuf": true, "command": true, "builtin": true, "exec": true, "setsid": true, "chrt": true, "taskset": true,
	"busybox": true, "unbuffer": true, "flock": true, "watch": true}

var keywords = map[string]bool{"if": true, "then": true, "else": true, "elif": true, "fi": true, "do": true, "done": true, "while": true,
	"until": true, "for": true, "case": true, "esac": true, "in": true, "!": true, "function": true, "select": true, "{": true, "}": true}

func unwrap(words []string) (name string, args []string, sudoS bool) {
	i := 0
	for i < len(words) {
		w := words[i]
		switch {
		case keywords[w]:
			i++
		case isAssignment(w):
			i++
		case wrappers[base(w)]:
			wr := base(w)
			i++
			for done := false; !done && i < len(words); {
				a := words[i]
				if wr == "sudo" && (a == "-S" || a == "--stdin" || strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "--") && strings.Contains(a, "S")) {
					sudoS = true
				}
				switch {
				case strings.HasPrefix(a, "-"):
					i++

					if (wr == "sudo" || wr == "doas") && (a == "-u" || a == "-g" || a == "-C" || a == "-p" || a == "-U" || a == "-h" || a == "-D") ||
						wr == "nice" && a == "-n" || wr == "ionice" && (a == "-c" || a == "-n") || wr == "timeout" && (a == "-s" || a == "-k") ||
						wr == "chrt" && a == "-p" || wr == "taskset" && a == "-c" || wr == "watch" && (a == "-n" || a == "-d") {
						i++
					}
				case wr == "env" && isAssignment(a):
					i++
				case wr == "timeout" && looksDuration(a):
					i++
				case (wr == "nice" || wr == "chrt" || wr == "taskset") && strings.Trim(a, "0123456789,-x") == "":
					i++
				default:
					done = true
				}
			}
		default:
			return base(w), words[i+1:], sudoS
		}
	}
	return "", nil, sudoS
}

func isAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i, r := range w[:eq] {
		if !(r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || i > 0 && r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func looksDuration(s string) bool {
	return s != "" && strings.Trim(strings.TrimRight(s, "smhd"), "0123456789.") == ""
}

func base(w string) string {
	if i := strings.LastIndexByte(w, '/'); i >= 0 && i < len(w)-1 {
		return w[i+1:]
	}
	return w
}

func splitFlags(args []string) (flags, operands []string) {
	done := false
	for _, a := range args {
		switch {
		case done:
			operands = append(operands, a)
		case a == "--":
			done = true
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
		default:
			operands = append(operands, a)
		}
	}
	return flags, operands
}

func hasShort(flags []string, letters string, long ...string) bool {
	for _, f := range flags {
		if strings.HasPrefix(f, "--") {
			for _, l := range long {
				if f == l || strings.HasPrefix(f, l+"=") {
					return true
				}
			}
			continue
		}
		if strings.ContainsAny(f[1:], letters) {
			return true
		}
	}
	return false
}

var systemRoots = map[string]bool{"/home": true, "/root": true, "/etc": true, "/usr": true, "/var": true, "/bin": true, "/sbin": true,
	"/boot": true, "/lib": true, "/lib64": true, "/lib32": true, "/opt": true, "/srv": true, "/dev": true, "/proc": true, "/sys": true}

func target(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	switch strings.TrimSuffix(strings.TrimSuffix(t, "*"), "/") {
	case "~", "$HOME", "${HOME}", "~root":
		return "home"
	}
	if !strings.HasPrefix(t, "/") {
		return ""
	}
	c := path.Clean(strings.TrimSuffix(strings.TrimSuffix(t, "*"), "/"))
	if strings.HasSuffix(t, "/*") || t == "/*" {
		c = path.Clean(strings.TrimSuffix(t, "/*"))
		if c == "." || c == "" {
			c = "/"
		}
	}
	switch {
	case c == "/" || c == "" || c == ".":
		return "root"
	case systemRoots[c]:
		return "system"
	case strings.Count(c, "/") == 2 && strings.HasPrefix(c, "/home/"):
		return "home"
	}
	return ""
}

func topLevel(t string) bool {
	return strings.HasPrefix(t, "/") && strings.Count(path.Clean(t), "/") == 1
}

func systemConfig(t string) bool {
	if !strings.HasPrefix(t, "/") {
		return strings.HasPrefix(t, "~/.ssh/") || strings.HasPrefix(t, "$HOME/.ssh/")
	}
	c := path.Clean(t)
	for _, p := range []string{"/etc/", "/boot/", "/usr/", "/lib/", "/lib64/", "/bin/", "/sbin/", "/root/.ssh/", "/var/lib/dpkg/", "/var/spool/cron/"} {
		if strings.HasPrefix(c+"/", p) {
			return true
		}
	}
	return strings.Contains(c, "/.ssh/authorized_keys")
}

func (env Env) self(name string) bool {
	n := strings.TrimSuffix(base(name), ".service")
	for _, s := range env.SelfNames {
		if n == s {
			return true
		}
	}
	return false
}

func (env Env) selfDB(t string) bool {
	for _, db := range env.SelfDB {
		if db != "" && (t == db || strings.HasPrefix(t, db+"-") || strings.HasSuffix(t, "/"+path.Base(db)) || t == path.Base(db)) {
			return true
		}
	}
	return false
}

func analyze(command string, env Env, depth int, out *[]Finding) {
	if depth > maxDepth {
		return
	}
	sc := Parse(command)
	for _, n := range sc.Nested {
		analyze(n, env, depth+1, out)
	}
	add := func(l Level, rule, reason string, paths ...string) {
		*out = append(*out, Finding{Level: l, Rule: rule, Reason: reason, Paths: paths})
	}
	for _, pl := range sc.Pipelines {
		for ci, c := range pl {
			name, args, sudoS := unwrap(c.Words)
			if sudoS && !env.SudoPassword {
				add(Hardline, "sudo-stdin", "sudo -S with no SUDO_PASSWORD: guessing the password from stdin")
			}
			low := strings.ToLower(name)

			for _, r := range c.Redirs {
				if !strings.Contains(r.Op, ">") {
					continue
				}
				switch {
				case blockDevRe.MatchString(r.Target):
					add(Hardline, "write-block-device", "writing straight onto a disk device", r.Target)
				case env.selfDB(r.Target):
					add(Hardline, "self-protect", "overwriting the agent's own database", r.Target)
				case systemConfig(r.Target):
					add(Dangerous, "system-config", "overwriting a system configuration file", r.Target)
				}
			}
			if name == "" {
				continue
			}
			flags, ops := splitFlags(args)
			switch {
			case low == "rm" || strings.HasPrefix(name, "$"):
				recursive := hasShort(flags, "rR", "--recursive")
				if !recursive && strings.HasPrefix(name, "$") {
					break
				}
				for _, t := range ops {
					switch target(t) {
					case "root":
						add(Hardline, "rm-root", "recursive delete of the filesystem's root", t)
					case "system":
						if recursive {
							add(Hardline, "rm-system", "recursive delete of a system folder", t)
						}
					case "home":
						if recursive {
							add(Hardline, "rm-home", "recursive delete of a home folder", t)
						}
					}
					if env.selfDB(t) {
						add(Hardline, "self-protect", "deleting the agent's own database", t)
					}
					if topLevel(t) && target(t) == "" {
						add(Dangerous, "rm-root-path", "deleting a folder right under /", t)
					}
				}
				if recursive && low == "rm" {
					add(Dangerous, "rm-recursive", "recursive delete", ops...)
				}
			case strings.HasPrefix(low, "mkfs") || low == "mke2fs" || low == "wipefs":
				add(Hardline, "mkfs", "formatting a filesystem")
			case low == "dd":
				for _, a := range args {
					if strings.HasPrefix(a, "of=") && blockDevRe.MatchString(strings.TrimPrefix(a, "of=")) {
						add(Hardline, "dd-device", "dd onto a disk device", strings.TrimPrefix(a, "of="))
					}
				}
				for _, a := range args {
					if strings.HasPrefix(a, "if=") {
						add(Dangerous, "dd", "a raw copy with dd")
						break
					}
				}
			case low == "kill":
				for _, a := range ops {
					if env.SelfPID > 0 && a == itoa(env.SelfPID) {
						add(Hardline, "self-protect", "killing the agent itself")
					}
				}
				minusOne := false
				for _, a := range args {
					if a == "-1" {
						minusOne = true
					}
				}
				switch {
				case minusOne && len(args) == 1:
					add(Hardline, "kill-all", "killing every process")
				case minusOne:
					add(Dangerous, "kill-all", "a signal to every process")
				}
			case low == "shutdown" || low == "reboot" || low == "halt" || low == "poweroff":
				add(Hardline, "power", "shutting the machine down or restarting it")
			case low == "init" || low == "telinit":
				for _, a := range ops {
					if a == "0" || a == "6" {
						add(Hardline, "power", "shutting the machine down or restarting it")
					}
				}
			case low == "systemctl":
				verb, units := "", []string{}
				for _, a := range ops {
					if verb == "" {
						verb = a
					} else {
						units = append(units, a)
					}
				}
				switch verb {
				case "poweroff", "reboot", "halt", "kexec", "suspend", "hibernate":
					add(Hardline, "power", "shutting the machine down or restarting it")
				case "stop", "disable", "mask", "kill":
					for _, u := range units {
						if env.self(u) {
							add(Hardline, "self-protect", "stopping the agent's own service")
						}
					}
					add(Dangerous, "systemctl", "stopping or disabling a service", units...)
				case "restart":
					add(Dangerous, "systemctl", "restarting a service", units...)
				}
			case low == "pkill" || low == "killall":
				for _, o := range ops {
					if env.self(o) {
						add(Hardline, "self-protect", "killing the agent itself")
					}
				}
				if low == "pkill" && hasShort(flags, "9", "--signal") || low == "pkill" && containsAny(flags, "-KILL", "-SIGKILL") ||
					low == "killall" && (containsAny(flags, "-KILL", "-9", "-SIGKILL", "-r", "--regexp") || hasShort(flags, "r")) {
					add(Dangerous, "kill-force", "force-killing processes by name")
				}
			case low == "chmod":
				for _, a := range ops {
					switch strings.TrimLeft(a, "0") {
					case "777", "666", "a+rwx", "ugo+rwx", "o+w", "a+w", "o+rwx":
						add(Dangerous, "chmod-open", "making files writable by everyone", ops...)
					}
				}
			case low == "chown":
				if hasShort(flags, "R", "--recursive") && len(ops) > 0 && strings.HasPrefix(ops[0], "root") {
					add(Dangerous, "chown-root", "giving a whole tree to root", ops[1:]...)
				}
			case low == "sqlite3" || low == "psql" || low == "mysql" || low == "mariadb":
				all := strings.Join(args, " ")
				if sqlDangerRe.MatchString(all) {
					add(Dangerous, "sql-destructive", "dropping, truncating or emptying a database table")
				}
				for _, a := range ops {
					if env.selfDB(a) && sqlWriteRe.MatchString(all) && gateTablesRe.MatchString(all) {
						add(Hardline, "self-protect", "changing the agent's own settings behind its back")
					}
				}
			case env.self(low) && len(ops) >= 3 && ops[0] == "config" && (ops[1] == "set" || ops[1] == "unset") && gateKey(ops[2]):
				add(Hardline, "self-protect", "changing the command gate from inside a job")
			case low == "tee" || low == "cp" || low == "mv" || low == "install" || low == "ln" || low == "truncate" || low == "shred":
				dests := ops
				if low != "tee" && low != "truncate" && low != "shred" && len(ops) > 0 {
					dests = ops[len(ops)-1:]
				}
				for _, d := range dests {
					switch {
					case env.selfDB(d):
						add(Hardline, "self-protect", "overwriting the agent's own database", d)
					case blockDevRe.MatchString(d):
						add(Hardline, "write-block-device", "writing straight onto a disk device", d)
					case systemConfig(d):
						add(Dangerous, "system-config", "overwriting a system configuration file", d)
					}
				}
			case low == "sed":
				if hasShort(flags, "i", "--in-place") {
					for _, d := range ops {
						if systemConfig(d) {
							add(Dangerous, "system-config", "editing a system configuration file in place", d)
						}
					}
				}
			case shells[low]:
				script := ""
				for i, a := range args {
					if a == "-c" && i+1 < len(args) {
						script = args[i+1]
					}
				}
				switch {
				case script != "":
					analyze(script, env, depth+1, out)
				case c.Heredoc:
					add(Dangerous, "shell-heredoc", "a script fed to a shell from a heredoc")
				case ci > 0 && !hasFileArg(ops):
					add(Dangerous, "pipe-to-shell", "a script piped into a shell: "+pipeSource(pl[:ci]))
				default:
					for _, a := range ops {
						if strings.HasPrefix(a, "<(") {
							add(Dangerous, "shell-procsub", "a downloaded or generated script run by a shell")
						}
					}
				}
			case low == "eval" || low == "source" || low == ".":
				joined := strings.Join(args, " ")
				if low == "eval" {
					analyze(joined, env, depth+1, out)
				}
				if strings.Contains(joined, "$(…)") || strings.Contains(joined, "<(…)") {
					add(Dangerous, "eval-substitution", "running what another command printed")
				}
			case low == "xargs":
				i := 0
				for i < len(args) && strings.HasPrefix(args[i], "-") {
					if a := args[i]; a == "-n" || a == "-I" || a == "-L" || a == "-P" || a == "-d" || a == "-E" || a == "-s" {
						i++
					}
					i++
				}
				if inner, iargs, _ := unwrap(args[min(i, len(args)):]); inner != "" {
					sub := append([]string{inner}, iargs...)
					analyze(strings.Join(quoteAll(sub), " "), env, depth+1, out)
				}

			case low == "remove-item" || low == "rm.exe":
				if containsFold(args, "-recurse") && containsFold(args, "-force") {
					add(Dangerous, "win-remove", "Remove-Item -Recurse -Force")
				}
			case low == "format" || low == "format.com":
				for _, a := range ops {
					if len(a) == 2 && a[1] == ':' {
						add(Dangerous, "win-format", "formatting a drive")
					}
				}
			case low == "vssadmin" && containsFold(args, "delete") && containsFold(args, "shadows"),
				low == "bcdedit" && containsFold(args, "/set"),
				low == "reg" && containsFold(args, "delete"),
				low == "taskkill" && containsFold(args, "/f"),
				low == "iex" || low == "invoke-expression":
				add(Dangerous, "windows", "a destructive Windows command")
			}
		}
	}
}

func gateKey(k string) bool {
	k = strings.ToLower(k)
	return strings.HasPrefix(k, "approvals") || strings.HasPrefix(k, "cmdgate") || strings.Contains(k, "yolo")
}

func hasFileArg(ops []string) bool {
	for _, o := range ops {
		if !strings.HasPrefix(o, "-") && !strings.HasPrefix(o, "<(") {
			return true
		}
	}
	return false
}

func pipeSource(stages []Cmd) string {
	var names []string
	for _, c := range stages {
		if n, _, _ := unwrap(c.Words); n != "" {
			names = append(names, n)
		}
	}
	return strings.Join(names, " | ")
}

func containsAny(xs []string, ws ...string) bool {
	for _, x := range xs {
		for _, w := range ws {
			if x == w {
				return true
			}
		}
	}
	return false
}

func containsFold(xs []string, w string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, w) {
			return true
		}
	}
	return false
}

func quoteAll(ws []string) []string {
	out := make([]string, len(ws))
	for i, w := range ws {
		out[i] = "'" + strings.ReplaceAll(w, "'", `'\''`) + "'"
	}
	return out
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
