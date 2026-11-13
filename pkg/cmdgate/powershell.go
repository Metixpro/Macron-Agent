package cmdgate

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

var (
	psStatementRe = regexp.MustCompile("\r?\n|;|&&|\\|\\||\\|")
	psDeleteRe    = regexp.MustCompile(`^(?:remove-item|rm|ri|del|erase|rd|rmdir)$`)
	psRecurseRe   = regexp.MustCompile(`^(?:-r|-re|-rec|-recu|-recur|-recurs|-recurse|/s)$`)
	psDriveRootRe = regexp.MustCompile(`^(?:[a-z]:|\$env:systemdrive|\$\{env:systemdrive\})\\?\*?$`)
	psSystemRe    = regexp.MustCompile(`^(?:[a-z]:|\$env:systemdrive)\\(?:windows|program files|program files \(x86\)|programdata|users|system volume information|boot|recovery)(?:\\system32)?\\?\*?$|^(?:\$env:(?:windir|systemroot|programfiles|programdata)|\$\{env:programfiles\(x86\)\})\\?\*?$`)
	psHomeRe      = regexp.MustCompile(`^(?:~|\$home|\$env:userprofile|\$\{env:userprofile\}|[a-z]:\\users\\[^\\]+)\\?\*?$`)
	psDownloadRe  = regexp.MustCompile(`\b(?:iwr|irm|curl|wget|invoke-webrequest|invoke-restmethod|start-bitstransfer)\b|downloadstring|downloadfile|net\.webclient`)
	psIexRe       = regexp.MustCompile(`\b(?:iex|invoke-expression)\b|\|\s*powershell\b|\|\s*pwsh\b`)
	psWords       = regexp.MustCompile(`"[^"]*"|'[^']*'|\S+`)
)

type psRule struct {
	re     *regexp.Regexp
	level  Level
	rule   string
	reason string
}

var psRules = []psRule{
	{regexp.MustCompile(`\b(?:format-volume|clear-disk|initialize-disk|remove-partition)\b|^diskpart\b|^format(?:\.com)?\s+[a-z]:`), Hardline, "format", "formatting or wiping a disk"},
	{regexp.MustCompile(`\bvssadmin(?:\.exe)?\s+delete\b|\bwbadmin(?:\.exe)?\s+delete\b|\bwmic\b.*\bshadowcopy\b.*\bdelete\b`), Hardline, "backups", "deleting Windows' backups and restore points"},
	{regexp.MustCompile(`\b(?:stop-computer|restart-computer)\b|^shutdown(?:\.exe)?\s+[-/][srpgh]`), Hardline, "power", "shutting the machine down or restarting it"},
	{regexp.MustCompile(`\bbcdedit\b`), Dangerous, "boot", "changing how Windows boots"},
	{regexp.MustCompile(`\bcipher(?:\.exe)?\s+/w\b`), Dangerous, "wipe", "wiping free disk space"},
	{regexp.MustCompile(`\bset-executionpolicy\b`), Dangerous, "execution-policy", "changing which scripts Windows lets run"},
	{regexp.MustCompile(`^reg(?:\.exe)?\s+(?:delete|import|restore|load|unload)\b|\b(?:remove-item|remove-itemproperty|ri|rp)\b.*\bhk(?:lm|cu|cr|u|cc):|^reg(?:\.exe)?\s+add\s+"?hk(?:lm|ey_local_machine)`), Dangerous, "registry", "changing or deleting registry keys"},
	{regexp.MustCompile(`\b(?:set-itemproperty|new-itemproperty|sp)\b.*\bhklm:`), Dangerous, "registry", "changing machine-wide registry settings"},
	{regexp.MustCompile(`\bset-mppreference\b.*-disable|\badd-mppreference\b.*-exclusion|\bset-mppreference\b.*-exclusion`), Dangerous, "defender", "weakening Windows Defender"},
	{regexp.MustCompile(`\bset-netfirewallprofile\b.*-enabled\s+(?:\$?false|0)|netsh\s+advfirewall\s+set\b.*\bstate\s+off|\bnetsh\s+firewall\s+set\s+opmode\s+disable`), Dangerous, "firewall", "turning the firewall off"},
	{regexp.MustCompile(`\bnew-localuser\b|\badd-localgroupmember\b|^net(?:\.exe)?\s+user\b.*\s/add\b|^net(?:\.exe)?\s+localgroup\b.*\s/add\b`), Dangerous, "accounts", "creating accounts or adding them to a group"},
	{regexp.MustCompile(`\btakeown\b|\bicacls\b.*\s/grant\b.*\beveryone\b|\bicacls\b.*\s/reset\b.*\s/t\b`), Dangerous, "permissions", "taking ownership or opening up file permissions"},
	{regexp.MustCompile(`\bclear-recyclebin\b`), Dangerous, "recycle-bin", "emptying the recycle bin"},
	{regexp.MustCompile(`\b(?:stop-service|spsv|suspend-service)\b|\bset-service\b.*-startuptype\s+disabled|^sc(?:\.exe)?\s+(?:stop|delete|config)\b`), Dangerous, "service", "stopping, disabling or deleting a service"},
	{regexp.MustCompile(`\b(?:stop-process|spps|kill)\b.*-force\b|^taskkill(?:\.exe)?\b.*\s/f\b`), Dangerous, "kill-force", "force-killing processes"},
	{regexp.MustCompile(`\b(?:uninstall-package|uninstall-module)\b|^winget\s+uninstall\b|^msiexec(?:\.exe)?\s+/x\b`), Dangerous, "uninstall", "uninstalling software"},
}

func psPath(t string) string {
	t = strings.Trim(strings.TrimSpace(t), `"'`)
	t = strings.ReplaceAll(strings.ToLower(t), "/", `\`)
	for strings.Contains(t, `\\`) {
		t = strings.ReplaceAll(t, `\\`, `\`)
	}
	return t
}

func (env Env) psSelfDB(t string) bool {
	for _, db := range env.SelfDB {
		if db == "" {
			continue
		}
		d := psPath(db)
		if t == d || strings.HasPrefix(t, d+"-") || filepath.Base(strings.ReplaceAll(d, `\`, "/")) == t {
			return true
		}
	}
	return false
}

func (env Env) psSelfHome(t string) bool {
	if env.SelfHome == "" {
		return false
	}
	h := strings.TrimSuffix(psPath(env.SelfHome), `\`)
	t = strings.TrimSuffix(strings.TrimSuffix(t, `\*`), `\`)
	return t == h
}

func analyzePowerShell(command string, env Env) []Finding {
	var out []Finding
	add := func(l Level, rule, reason string, paths ...string) {
		out = append(out, Finding{Level: l, Rule: rule, Reason: reason, Paths: paths})
	}
	low := strings.ToLower(command)
	if psIexRe.MatchString(low) && psDownloadRe.MatchString(low) {
		add(Dangerous, "remote-code", "running code downloaded from the internet")
	}
	for _, stmt := range psStatementRe.Split(command, -1) {
		s := strings.TrimSpace(strings.ToLower(stmt))
		s = strings.TrimSpace(strings.TrimLeft(s, "&. "))
		if s == "" {
			continue
		}
		for _, r := range psRules {
			if r.re.MatchString(s) {
				add(r.level, r.rule, r.reason)
			}
		}
		words := psWords.FindAllString(s, -1)
		if len(words) == 0 {
			continue
		}
		verb := strings.Trim(words[0], `"'`)
		if i := strings.LastIndexAny(verb, `\/`); i >= 0 {
			verb = verb[i+1:]
		}
		verb = strings.TrimSuffix(verb, ".exe")
		args := words[1:]
		switch {
		case psDeleteRe.MatchString(verb):
			recursive := false
			var targets []string
			for _, a := range args {
				switch {
				case psRecurseRe.MatchString(a):
					recursive = true
				case strings.HasPrefix(a, "-") || strings.HasPrefix(a, "/") && len(a) <= 3:
				default:
					targets = append(targets, psPath(a))
				}
			}
			for _, t := range targets {
				switch {
				case psDriveRootRe.MatchString(t):
					add(Hardline, "rm-root", "deleting a whole drive", t)
				case recursive && psSystemRe.MatchString(t):
					add(Hardline, "rm-system", "recursive delete of a Windows system folder", t)
				case recursive && psHomeRe.MatchString(t):
					add(Hardline, "rm-home", "recursive delete of a home folder", t)
				}
				if env.psSelfDB(t) {
					add(Hardline, "self-protect", "deleting the agent's own database", t)
				}
				if recursive && env.psSelfHome(t) {
					add(Dangerous, "self-manage", "deleting the agent's own home folder", t)
				}
			}
			if recursive {
				add(Dangerous, "rm-recursive", "recursive delete", targets...)
			}
		case verb == "stop-process" || verb == "spps" || verb == "kill" || verb == "taskkill":
			for i, a := range args {
				v := strings.Trim(a, `"'`)
				prev := ""
				if i > 0 {
					prev = args[i-1]
				}
				if env.SelfPID > 0 && v == strconv.Itoa(env.SelfPID) {
					add(Hardline, "self-protect", "killing the agent itself")
				}
				if prev == "-name" || prev == "-n" || prev == "-processname" || prev == "/im" || verb != "taskkill" && !strings.HasPrefix(v, "-") {
					if env.self(strings.TrimSuffix(v, ".exe")) {
						add(Hardline, "self-protect", "killing the agent itself")
					}
				}
			}
		}
	}
	return out
}
