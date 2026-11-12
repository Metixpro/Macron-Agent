package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

type claimClass int

const (
	claimDestroy claimClass = iota + 1
	claimStop
	claimSession
	claimChange
	claimRan
)

type claim struct {
	class claimClass
	text  string
}

type receipt struct {
	name   string
	cmd    string
	failed bool
}

type ledger struct{ rs []receipt }

var (
	readOnlyTools = setOf("read_file", "list_files", "search_code", "find_files", "project_tree", "code_outline", "web_search", "web_fetch",
		"deep_research", "view_image", "document_reader", "query_data", "github_inspect", "whois_lookup",
		"currency_gold_price", "crypto_currency_price", "search_image", "search_music", "list_skills", "read_skill",
		"recall_history", "more_tools", "tool_search", "list_schedules", "chat_info", "telegram_lookup", "lookup", "list_plans", "update_plan")

	execTools = setOf("bash_execute", "run_code", "shell_jobs", "install_package")

	writeTools = setOf("write_file", "edit_file", "create_spreadsheet", "generate_chart",
		"generate_qr", "text_to_speech", "pdf_tool", "media_convert", "split_video", "create_motion_graphic", "send_file",
		"download_file", "download_media_ytdlp", "import_repo", "extract_archive", "pack_files", "remember", "save_skill", "create_schedule",
		"schedule", "create_poll", "react", "send_effect", "send_widget", "update_widget")

	destroyTools = setOf("apply_patch", "cancel_schedule", "forget")
	stopTools    = setOf("cancel_schedule")
)

func setOf(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

var (
	destroyCmd = regexp.MustCompile(`(?i)(?:^|[^\w])(?:rm|rmdir|unlink|shred|truncate|del|delete|remove|erase|wipe|purge|prune|uninstall|drop)(?:[^\w]|$)|` +
		`os\.(?:remove|unlink|rmdir)|rmtree|\.unlink\(|\.rmdir\(|fs\.(?:rm|unlink|rmdir)|-delete\b|git\s+clean|docker\s+(?:rm|rmi|system\s+prune)|` +
		`pm2\s+delete|systemctl\s+(?:disable|mask)|(?:apt|apt-get|yum|dnf)\s+(?:remove|purge)|pip3?\s+uninstall|npm\s+(?:uninstall|rm)\b`)
	stopCmd = regexp.MustCompile(`(?i)(?:^|[^\w])(?:kill|pkill|killall|skill|terminate|shutdown|halt|poweroff|reboot)(?:[^\w]|$)|` +
		`systemctl\s+(?:stop|disable|mask|kill)|service\s+\S+\s+stop|docker\s+(?:stop|kill|rm)|docker-compose\s+(?:stop|down)|docker\s+compose\s+(?:stop|down)|` +
		`pm2\s+(?:stop|delete|kill)|supervisorctl\s+stop|tmux\s+kill|screen\s+.*quit|os\.kill|\.terminate\(|\.kill\(|signal\.SIG|` +
		`taskkill|nohup\s+.*&\s*$`)
	sessionCmd = regexp.MustCompile(`(?i)log[_\s-]?out|sign[_\s-]?out|revoke|invalidate|deauth|disconnect|terminate_?session|reset_?authorizations|auth\.log`)

	mutatingCmd = regexp.MustCompile(`(?i)(?:^|[\s;&|(` + "`" + `])(?:mv|cp|chmod|chown|chgrp|mkdir|touch|ln|tee|dd|install|useradd|userdel|passwd|crontab|nohup|screen|tmux|` +
		`rsync|scp|wget|patch|truncate|kill|pkill|killall|rm|rmdir|shred|reboot|shutdown|halt|poweroff)(?:[\s;&|)]|$)|` +
		`sed\s+-[a-z]*i|(?:apt|apt-get|yum|dnf|apk)\s+(?:install|remove|purge|upgrade|update)|pip3?\s+(?:install|uninstall)|npm\s+(?:install|i|uninstall|run|ci)\b|` +
		`systemctl\s+(?:start|stop|restart|reload|enable|disable|mask)|service\s+\S+\s+(?:start|stop|restart)|` +
		`docker\s+(?:run|start|stop|restart|rm|kill|compose|exec|build)|git\s+(?:commit|push|pull|checkout|reset|clean|merge|rebase|apply|add|rm|mv)|` +
		`os\.(?:remove|unlink|rename|system|kill|makedirs|mkdir)|shutil\.|rmtree|subprocess|\.write\(|open\([^)]*['"][wa]|fs\.(?:rm|unlink|write|mkdir)|writeFile|` +
		`\.kill\(|\.terminate\(|log[_\s-]?out|revoke|requests\.(?:post|put|delete)|curl\s[^|;]*(?:-X\s*(?:POST|PUT|DELETE)|-d\s|--data|-o\s|-O\b)`)
	redirectNoise = regexp.MustCompile(`\d?>\s*&\s*\d|&?>\s*/dev/null|\d>\s*/dev/null`)
	redirectOut   = regexp.MustCompile(`>`)
	readOnlyProgs = setOf("ls", "ll", "cat", "head", "tail", "less", "more", "grep", "egrep", "fgrep", "rg", "find", "pwd", "echo", "printf", "ps", "pgrep",
		"top", "htop", "df", "du", "free", "uptime", "whoami", "id", "uname", "stat", "file", "wc", "sort", "uniq", "cut", "awk", "tr", "which", "whereis",
		"type", "env", "printenv", "date", "hostname", "ip", "ss", "netstat", "lsof", "journalctl", "dmesg", "git", "docker", "systemctl", "pip", "pip3",
		"npm", "python", "python3", "node", "test", "[", "true", "cd", "basename", "dirname", "realpath", "readlink", "tree", "ping", "dig", "nslookup",
		"curl", "sleep", "xargs", "diff", "md5sum", "sha256sum", "jq", "tar", "zipinfo", "unzip", "ssh", "sudo", "nl", "column", "history", "last", "w", "who")
	cmdSplit = regexp.MustCompile(`&&|\|\||[;|\n]`)
)

func execText(args string) string {
	var m map[string]any
	if json.Unmarshal([]byte(args), &m) != nil {
		return strings.TrimSpace(args)
	}
	var sb strings.Builder
	for _, k := range []string{"command", "cmd", "code", "script", "source", "action", "packages", "package", "name", "id", "args"} {
		switch v := m[k].(type) {
		case string:
			sb.WriteString(v + "\n")
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					sb.WriteString(s + " ")
				}
			}
			sb.WriteString("\n")
		}
	}
	return textutil.TruncateRunes(sb.String(), 3000, "")
}

func cmdMutates(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}
	plain := redirectNoise.ReplaceAllString(cmd, " ")
	if mutatingCmd.MatchString(plain) || redirectOut.MatchString(plain) {
		return true
	}
	for _, seg := range cmdSplit.Split(plain, -1) {
		fields := strings.Fields(seg)
		for len(fields) > 0 && (strings.Contains(fields[0], "=") || fields[0] == "sudo" || fields[0] == "-n" || fields[0] == "time") {
			fields = fields[1:]
		}
		if len(fields) == 0 {
			continue
		}
		prog := fields[0]
		if i := strings.LastIndexByte(prog, '/'); i >= 0 {
			prog = prog[i+1:]
		}
		if !readOnlyProgs[prog] {
			return true
		}
	}
	return false
}

var execFailedRe = regexp.MustCompile(`(?m)^\[(?:exit -?[1-9]\d*|timed out)`)

func (l *ledger) add(name, args, result string, refused bool) {
	r := receipt{name: name, failed: refused || failedOutput(result) || execFailedRe.MatchString(result)}
	if execTools[name] {
		r.cmd = execText(args)
	}
	l.rs = append(l.rs, r)
}

func (l *ledger) supports(c claimClass) bool {
	for _, r := range l.rs {
		if r.failed {
			continue
		}
		known := readOnlyTools[r.name] || execTools[r.name] || writeTools[r.name] || destroyTools[r.name]
		switch {
		case !known:
			return true
		case c == claimRan:
			return true
		case readOnlyTools[r.name]:
			continue
		}
		switch c {
		case claimChange:
			if writeTools[r.name] || destroyTools[r.name] || execTools[r.name] && cmdMutates(r.cmd) {
				return true
			}
		case claimDestroy:
			if destroyTools[r.name] || execTools[r.name] && (r.cmd == "" || destroyCmd.MatchString(r.cmd)) {
				return true
			}
		case claimStop:
			if stopTools[r.name] || execTools[r.name] && (r.cmd == "" || stopCmd.MatchString(r.cmd)) {
				return true
			}
		case claimSession:
			if execTools[r.name] && (r.cmd == "" || sessionCmd.MatchString(r.cmd) || destroyCmd.MatchString(r.cmd) || stopCmd.MatchString(r.cmd)) {
				return true
			}
		}
	}
	return false
}

func (l *ledger) summary() string {
	if len(l.rs) == 0 {
		return "none (no tool was called in this request)"
	}
	var parts []string
	for i, r := range l.rs {
		if i >= 8 {
			parts = append(parts, fmt.Sprintf("… +%d", len(l.rs)-i))
			break
		}
		s := r.name
		if r.cmd != "" {
			s += " «" + textutil.TruncateRunes(textutil.OneLine(r.cmd), 60, "…") + "»"
		}
		if r.failed {
			s += " (failed)"
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}

const doneFa = `(?:شدند|شدن|شد|شده|گردیدند|گردید|کردیم|کردمش|کردم|زدیم|زدم|بستم|کشتم|گرفتم|دادم)`

func faClaim(stems string) *regexp.Regexp {
	return regexp.MustCompile(`(?:^|[^\p{L}])(?:` + stems + `)(?:ش|شون|شو)?\s*(?:رو|را)?\s*(?:\S{1,25}\s+){0,2}?` + doneFa + `(?:[^\p{L}]|$)`)
}

const (
	enDestroy = `deleted|removed|wiped|erased|cleaned(?:\s+up)?|purged|cleared|uninstalled`
	enStop    = `killed|terminated|stopped|shut\s+down|disabled|halted|turned\s+off`
	enSession = `logged\s+(?:you\s+|it\s+|the\s+\w+\s+)?out|signed\s+out|revoked|invalidated|ended\s+the\s+session|terminated\s+the\s+session`
	enChange  = `installed|restarted|rebooted|deployed|updated|configured|created|wrote|written|modified|edited|renamed|moved|patched|fixed|set\s+up`
)

func enClaim(verbs string) *regexp.Regexp {
	return regexp.MustCompile(`(?i)\bi(?:'ve|\s+have|'m\s+done|\s+also|\s+just|\s+now|\s+successfully)*\s+(?:\w+\s+){0,1}?(?:` + verbs + `)\b|` +
		`\b(?:was|were|has\s+been|have\s+been|got|is\s+now|are\s+now|been\s+successfully)\s+(?:successfully\s+)?(?:` + verbs + `)\b|` +
		`(?:^|\n)[\s\-•*✅✔☑️\d.)]*(?:` + verbs + `)\s+(?:the|all|your|it|everything|\w+)\b`)
}

var claimPatterns = []struct {
	class claimClass
	res   []*regexp.Regexp
}{
	{claimDestroy, []*regexp.Regexp{
		faClaim(`پاک|حذف|پاکسازی|ریموو|تمیز|rm(?:\s+-\S+)*|remove|delete|wipe|erase`), enClaim(enDestroy)}},
	{claimStop, []*regexp.Regexp{
		faClaim(`متوقف|توقف|خاموش|آف|کیل|kill|stop|terminate|shutdown|غیرفعال|disable`), enClaim(enStop)}},
	{claimSession, []*regexp.Regexp{
		faClaim(`لاگ\s?اوت|logout|log\s?out|sign\s?out|log_out|خارج|باطل|ابطال|revoke`), enClaim(enSession)}},
	{claimChange, []*regexp.Regexp{
		faClaim(`نصب|آنینستال|ریستارت|ریاستارت|ریبوت|دیپلوی|آپدیت|بروزرسانی|بهروزرسانی|ساخته|ایجاد|نوشته|ویرایش|اصلاح|تنظیم|کانفیگ|install|uninstall|restart|reboot|deploy`),
		regexp.MustCompile(`(?:^|[^\p{L}])(?:ساختم|ساختمش|نوشتم|نوشتمش|فرستادم|فرستادمش|نصبش\s+کردم)(?:[^\p{L}]|$)`),
		enClaim(enChange)}},
	{claimRan, []*regexp.Regexp{
		regexp.MustCompile(`(?:رفتم|وصل\s+شدم|کانکت\s+شدم|لاگین\s+کردم|وارد\s+شدم|متصل\s+شدم)\s+(?:\S+\s+){0,3}?(?:سرور|ssh|ماشین|اس\s?اس\s?اچ)|` +
			`(?:دستور|کامند|کد|اسکریپت)\S*\s+(?:رو\s+|را\s+)?(?:\S+\s+){0,2}?(?:ران|اجرا)\s+کردم|(?:ران|اجرا)\s+کردم(?:ش|شون)?(?:[^\p{L}]|$)`),
		regexp.MustCompile(`(?i)\bi(?:'ve|\s+have)?\s+(?:just\s+|then\s+)?(?:ran|executed|connected\s+to|logged\s+(?:in|into)|ssh'?ed|checked\s+the\s+server)\b`)}},
}

var (
	codeSpanRe = regexp.MustCompile("(?s)```.*?(?:```|\\z)|`[^`\n]*`")
	sentenceRe = regexp.MustCompile(`[.!؟?\n؛;]+`)

	notClaim = regexp.MustCompile(`(?i)نشد|نکردم|نتونستم|نتوانستم|نمیشه|نمیتونم|نشده|نکرد|نمیگیره|هنوز|بدون اینکه|اگه|اگر|وقتی|بعد از|قبل از|برای اینکه|` +
		`باید|بخوای|میخوای|خواستی|بخواید|بخواین|لازمه|میتونی|میتونید|بزنی|بزنید|مثلا|یعنی|` +
		`\b(?:if|when|once|after|before|unless|didn't|did not|couldn't|could not|haven't|have not|hasn't|not been|never|unable|failed|would|should|can|could|want|let me know|to be|for example|e\.g)\b`)
)

func normFa(s string) string {
	return strings.NewReplacer("‌شده", " (adj)", "‌", "", "ـ", "", "ي", "ی", "ك", "ک", "‏", "", "‎", "").Replace(s)
}

func claimsIn(answer string) []claim {
	s := normFa(codeSpanRe.ReplaceAllString(answer, " "))
	var out []claim
	seen := map[claimClass]bool{}
	for _, sent := range sentenceRe.Split(s, -1) {
		sent = strings.TrimSpace(sent)
		if sent == "" || strings.HasPrefix(sent, ">") || notClaim.MatchString(sent) {
			continue
		}
		for _, p := range claimPatterns {
			if seen[p.class] {
				continue
			}
			for _, re := range p.res {
				if re.MatchString(sent) {
					seen[p.class] = true
					out = append(out, claim{class: p.class, text: textutil.TruncateRunes(sent, 70, "…")})
					break
				}
			}
		}
	}
	return out
}

func (l *ledger) unbacked(answer string) []claim {
	var out []claim
	for _, c := range claimsIn(answer) {
		if !l.supports(c.class) {
			out = append(out, c)
		}
	}
	return out
}

func claimWords(cs []claim) string {
	var q []string
	seen := map[string]bool{}
	for _, c := range cs {
		if len(q) >= 3 {
			break
		}
		if !seen[c.text] {
			seen[c.text] = true
			q = append(q, "«"+c.text+"»")
		}
	}
	return strings.Join(q, ", ")
}

const noticeUnbacked = "[system notice] Your reply says things were done (%s), but no step of this request did them. Receipts of this request: %s. " +
	"What you did not run did not happen, and the user would believe it. If the user asked for these actions, do them now with the tools, " +
	"then check with one read-only call that they took effect and report what that call showed. If you cannot do them, or should not, say so plainly. " +
	"If they were done in an earlier reply, say that only when that reply's <work_notes> show it. Never write an action as done without its receipt."

func unbackedWarning(cs []claim, lang string) string {
	if lang == "en" {
		return fmt.Sprintf("⚠️ Heads-up: this reply says %s, but no tool ran for it, so none of it is confirmed and it may not have happened. "+
			"Tell me and I'll do it for real and show you the result.\n\n", claimWords(cs))
	}
	return fmt.Sprintf("⚠️ توجه: توی این جواب نوشتم %s، ولی هیچ ابزاری برای انجامش اجرا نشد؛ پس این کارها تأیید نشدن و ممکنه اصلاً انجام نشده باشن. "+
		"بگو تا واقعاً انجامشون بدم و نتیجه رو نشونت بدم.\n\n", claimWords(cs))
}
