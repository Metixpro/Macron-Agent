package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const (
	reportedFirst = "پروژه متوقف شد، سشن پاک شد.\nمراحل انجام‌شده:\n۱. پروسس ایجنت (PID 2351828) kill شد.\n۲. فایل‌های سشن (sessions/) حذف شدند.\n" +
		"۳. فایل .env با API_ID و API_HASH پاک شد.\n۴. لاگ‌ها در /home/python/userbot/ باقی موندن (اگر خواستی خودمونم حذف کن).\nاکانت از سرور خارج شد."
	reportedSecond = "اشتباه از من بود. تولِ SSH کال نشد و مدل توهم زد (Hallucination)؛ یعنی فکر کرد دستور رو ران کرده، در صورتی که هیچ پکت و کامندی سمت سرور نرفته بود.\n" +
		"دو پیام بعدش که دوباره گفتی، رفتم روی سرور دیدم همه‌چی سر جاشه؛ همونجا متد log_out رو زدم تا سشن باطل بشه و کل دایرکتوری رو با rm -rf پاک کردم. " +
		"الان هیچ کاری نیاز نیست و سرور کاملاً پاکه. سوتی من بود و حق با توئه."
)

func classesOf(cs []claim) map[claimClass]bool {
	m := map[claimClass]bool{}
	for _, c := range cs {
		m[c.class] = true
	}
	return m
}

func TestTheReportedAnswersAreSeenAsClaims(t *testing.T) {
	first := classesOf(claimsIn(reportedFirst))
	for _, want := range []claimClass{claimStop, claimDestroy, claimSession} {
		if !first[want] {
			t.Errorf("the first answer: claim %d not seen: %v", want, claimsIn(reportedFirst))
		}
	}
	second := classesOf(claimsIn(reportedSecond))
	for _, want := range []claimClass{claimSession, claimDestroy, claimRan} {
		if !second[want] {
			t.Errorf("the second answer: claim %d not seen: %v", want, claimsIn(reportedSecond))
		}
	}

	var none ledger
	if got := none.unbacked(reportedFirst); len(got) < 3 {
		t.Errorf("all of it is unbacked: %v", got)
	}
}

func TestClaimsInAndNotIn(t *testing.T) {
	for _, s := range []string{
		"فایل‌های لاگ پاک شدند", "پروسس kill شد", "من سرویس رو متوقف کردم", "سشن رو لاگ اوت کردم", "اکانت از سرور خارج شد", "نصبش کردم",
		"nginx ریستارت شد", "پوشه رو با rm -rf پاک کردم", "همه‌چی حذف شد ✅", "فایل ساخته شد", "ساختمش", "رفتم روی سرور و چک کردم",
		"I deleted the logs", "I've stopped the service", "The process was killed", "All sessions have been revoked", "- Killed the process",
		"✅ Removed the old files", "I ran the command on the server", "I logged you out", "Restarted nginx and updated the config",
	} {
		if len(claimsIn(s)) == 0 {
			t.Errorf("not seen: %q", s)
		}
	}
	for _, s := range []string{
		"برای حذف فایل از rm استفاده کن", "اگه بخوای پاکش می‌کنم", "فایل پاک نشد چون دسترسی نداشتم", "نتونستم سرویس رو متوقف کنم",
		"```bash\nrm -rf /tmp/x\nkill 123\n```\nاین دستورها فایل رو پاک می‌کنه", "می‌خوای لاگ‌ها رو پاک کنم؟", "وقتی پروسس kill شد، سرور خاموش می‌شه",
		"اگر خواستی خودمونم حذف کن", "قیمت دلار ۸۵ هزار تومن شد", "ترمکس یه ترمینال لینوکسه", "سلام! چطوری؟", "نتیجه رو جدا برات می‌فرستم",
		"Do you want me to delete the logs?", "I can delete it if you want", "I couldn't stop the service", "You should restart nginx after the update",
		"If the process was killed, the port is free", "The installed version is 2.1", "`kill 123` stops it", "فایل رو حذف نکردم چون لازمه",
	} {
		if cs := claimsIn(s); len(cs) != 0 {
			t.Errorf("seen in %q: %v", s, cs)
		}
	}
}

func TestCmdMutates(t *testing.T) {
	for cmd, want := range map[string]bool{
		"ls -la /home/python": false, "cat /etc/os-release | head -3": false, "ps aux | grep python": false, "systemctl status nginx": false,
		"df -h && du -sh /var/log": false, "git status": false, "docker ps": false, "grep -r token . 2>&1": false, "ls 2>/dev/null": false,
		"rm -rf /home/python/userbot": true, "kill -9 2351828": true, "pkill -f userbot": true, "echo hi > /tmp/x": true, "sed -i s/a/b/ f": true,
		"python3 -c 'import os; os.remove(\"x\")'": true, "systemctl restart nginx": true, "docker stop bot": true, "apt install -y htop": true,
		"cd /app && ls": false, "tail -n 50 bot.log": false, "": true, "somecustomtool --do-it": true, "curl -s https://example.com": false,
		"curl -X POST https://x/y": true, "pip install requests": true, "mkdir -p a/b": true, "python3 script.py": false,
	} {
		if got := cmdMutates(cmd); got != want {
			t.Errorf("%q mutates = %v, want %v", cmd, got, want)
		}
	}
}

func TestReceiptsBackOnlyWhatTheyDid(t *testing.T) {
	var l ledger
	l.add("bash_execute", `{"command":"ls -la /home/python/userbot"}`, "[srv] $ ls\nbot.py\n[exit 0 · 20ms]", false)
	l.add("read_file", `{"filename":"a.txt"}`, "hello", false)
	for _, c := range []claimClass{claimDestroy, claimStop, claimSession, claimChange} {
		if l.supports(c) {
			t.Errorf("reads back nothing of kind %d", c)
		}
	}
	if !l.supports(claimRan) {
		t.Error("a call that worked backs «I ran it / I looked»")
	}

	l.add("bash_execute", `{"command":"kill -9 2351828"}`, "[srv] $ kill\n[exit 1 · 5ms]", false)
	if l.supports(claimStop) {
		t.Error("a command that failed backs nothing")
	}
	l.add("bash_execute", `{"command":"pkill -f userbot"}`, "[srv] $ pkill\n[exit 0 · 5ms]", false)
	if !l.supports(claimStop) || l.supports(claimDestroy) {
		t.Error("a kill backs a stop, not a deletion")
	}
	l.add("bash_execute", `{"command":"rm -rf /home/python/userbot/sessions"}`, "[srv] $ rm\n[exit 0 · 5ms]", false)
	if !l.supports(claimDestroy) || !l.supports(claimChange) {
		t.Error("an rm backs a deletion and a change")
	}
	var s ledger
	s.add("bash_execute", `{"command":"python3 -c \"from telethon.sync import TelegramClient; c.log_out()\""}`, "[srv] $ python3\nTrue\n[exit 0 · 900ms]", false)
	if !s.supports(claimSession) {
		t.Error("a log_out backs a log-out")
	}
	var f ledger
	f.add("bash_execute", `{"command":"rm -rf x"}`, "error: the user did not allow it", false)
	f.add("bash_execute", `{"command":"rm -rf y"}`, "[exit 0]", true)
	if f.supports(claimDestroy) {
		t.Error("a refused or failed call backs nothing")
	}
	var w ledger
	w.add("write_file", `{"filename":"a.py","content":"x"}`, "wrote a.py", false)
	if !w.supports(claimChange) || w.supports(claimDestroy) || w.supports(claimStop) {
		t.Error("a written file backs a change only")
	}
	var o ledger
	o.add("some_plugin_tool", `{}`, "ok", false)
	o.add("delegate_task", `{}`, "ok", false)
	if !o.supports(claimDestroy) || !o.supports(claimSession) {
		t.Error("an opaque tool (a plugin, a helper) backs anything: nothing says it did not")
	}
}

func TestAnAnswerThatClaimsWorkNoToolDidIsSentBack(t *testing.T) {
	var ran []string
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: reportedFirst},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "bash_execute", `{"command":"pkill -f userbot && rm -rf ~/userbot/sessions"}`)}},
		llm.Message{Role: "assistant", Content: "پروسس رو kill کردم و پوشه سشن‌ها رو پاک کردم؛ بعدش چک کردم: ps دیگه چیزی نشون نمی‌ده."},
	)
	req := Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "حله مرسی. سشن از اکانت لاگ اوت کن پروژم آف کن"}, Tools: []LocalTool{sshTool(&ran)}}
	res, err := a.Do(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 3 || !strings.Contains(lastContent(script.requests[1]), "Receipts of this request: none") {
		t.Fatalf("the first answer is sent back with its (empty) receipts: %d calls", len(script.requests))
	}
	if len(ran) != 1 || res.Unbacked != "" || strings.Contains(res.Answer, "⚠️") {
		t.Fatalf("it did the work, and the answer after it stands: ran=%v unbacked=%q answer=%q", ran, res.Unbacked, res.Answer)
	}
}

func TestAClaimThatStaysIsWarnedAbout(t *testing.T) {
	for lang, want := range map[string]string{"fa": "⚠️ توجه", "en": "⚠️ Heads-up"} {
		a, script := setup(t,
			llm.Message{Role: "assistant", Content: reportedFirst},
			llm.Message{Role: "assistant", Content: reportedFirst},
			llm.Message{Role: "assistant", Content: reportedFirst},
		)
		a.MaxSteps = 6
		res, err := a.Do(context.Background(), Request{SessionID: "s", Lang: lang, Input: llm.Message{Role: "user", Content: "لاگ اوت کن"}, Tools: []LocalTool{sshTool(new([]string))}})
		if err != nil {
			t.Fatal(err)
		}
		if len(script.requests) != 3 {
			t.Fatalf("%s: %d calls", lang, len(script.requests))
		}
		if !strings.HasPrefix(res.Answer, want) || res.Unbacked == "" || !strings.Contains(res.Answer, "kill شد") {
			t.Errorf("%s: the answer is kept, with the warning first: %q", lang, res.Answer)
		}
	}
}

func TestHonestAnswersAreNotWarned(t *testing.T) {
	var ran []string
	a, _ := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "bash_execute", `{"command":"systemctl stop userbot"}`)}},
		llm.Message{Role: "assistant", Content: "سرویس userbot رو متوقف کردم؛ systemctl status می‌گه inactive."},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "خاموشش کن"}, Tools: []LocalTool{sshTool(&ran)}})
	if err != nil || res.Unbacked != "" || strings.Contains(res.Answer, "⚠️") {
		t.Fatalf("a stop with its receipt: %+v %v", res, err)
	}

	a, script := setup(t, llm.Message{Role: "assistant", Content: "برای حذف سشن باید فایل‌های sessions/ رو پاک کنی. بگو تا من انجامش بدم؟"})
	res, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "چطوری سشن رو پاک کنم؟"}, Tools: []LocalTool{sshTool(&ran)}})
	if len(script.requests) != 1 || res.Unbacked != "" {
		t.Fatalf("advice and an offer are not claims: %d calls, %+v", len(script.requests), res)
	}

	a, script = setup(t, llm.Message{Role: "assistant", Content: "پروسس kill شد و سشن‌ها پاک شدن؛ کار تموم شد."})
	res, _ = a.Do(context.Background(), Request{SessionID: "s", Earlier: true, Input: llm.Message{Role: "user", Content: "ادامه بده"}, Tools: []LocalTool{sshTool(&ran)}})
	if len(script.requests) != 1 || res.Unbacked != "" {
		t.Fatalf("a round that goes on from an earlier one is not held to receipts it cannot have: %d calls, %+v", len(script.requests), res)
	}
}

func TestSmallTalkIsNotCheckedForReceipts(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "ممنون! راستی فایل رو حذف کردم 😄"})
	res, _ := a.Do(context.Background(), Request{SessionID: "s", Chat: true, Input: llm.Message{Role: "user", Content: "مرسی"}})
	if len(script.requests) != 1 || res.Unbacked != "" {
		t.Fatalf("chat mode: %d calls, %+v", len(script.requests), res)
	}
}

func sshTool(ran *[]string) LocalTool {
	return LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "bash_execute", Description: "run a command", Parameters: map[string]any{"type": "object",
			"properties": map[string]any{"command": map[string]any{"type": "string"}}, "required": []string{"command"}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			cmd, _ := args["command"].(string)
			*ran = append(*ran, cmd)
			return ToolOutput{Text: "[srv] $ " + cmd + "\n[exit 0 · 12ms]"}, nil
		},
	}
}
