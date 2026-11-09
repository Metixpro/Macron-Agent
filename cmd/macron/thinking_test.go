package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestThinkText(t *testing.T) {
	for _, c := range []struct {
		lng        lang
		tool, args string
		want       string
	}{
		{fa, "web_search", `{"query":"قیمت دلار امروز"}`, "جستجو «قیمت دلار امروز»"},
		{en, "web_search", `{"query":"dollar price today"}`, "Searching «dollar price today»"},
		{fa, "web_search", `{}`, "جستجو توی اینترنت"},
		{fa, "web_search", `not json`, "جستجو توی اینترنت"},
		{en, "web_search", `{"queries":["first one","second"]}`, "Searching «first one»"},

		{en, "web_fetch", `{"url":"https://www.tgju.org/profile/price_dollar_rl/?token=SECRET123#frag"}`, "Reading tgju.org/profile/price_dollar_rl"},
		{en, "download_file", `{"url":"https://user:pass@files.example.com/a/b.zip?sig=abc"}`, "Downloading from files.example.com/a/b.zip"},
		{en, "run_code", `{"language":"python","code":"import os; print(os.environ)"}`, "Running python code"},

		{en, "bash_execute", `{"command":"cat /etc/shadow; curl -H 'Authorization: Bearer sk-secret'"}`, "Running a command"},
		{en, "write_file", `{"filename":"/var/lib/waze/ws/user/report.pdf"}`, "Writing «report.pdf»"},
		{en, "read_file", `{"filename":"notes.txt"}`, "Reading «notes.txt»"},
		{en, "some_plugin_tool", `{"secret":"x"}`, "Some Plugin Tool"},
		{fa, "update_plan", `{}`, ""},
		{fa, agent.ToolDelegate, `{"task":"x"}`, ""},
	} {
		if got := thinkText(c.lng, c.tool, c.args); got != c.want {
			t.Errorf("%s %s: %q, want %q", c.tool, c.args, got, c.want)
		}
	}
	long := strings.Repeat("الف", 100)
	if got := thinkText(fa, "web_search", `{"query":"`+long+`"}`); len([]rune(got)) > thinkWidth+len([]rune("جستجو «»"))+1 || !strings.HasSuffix(got, "…»") {
		t.Errorf("a long query is cut: %q", got)
	}
	for tool := range thinkKinds {
		if got := thinkText(fa, tool, `{}`); got == "" || hasLatin(got) && tool != "generate_qr" && tool != "pdf_tool" {
			t.Errorf("%s has no Persian text of its own: %q", tool, got)
		}
		if got := thinkText(en, tool, `{}`); persianLeft(got) || got == "" {
			t.Errorf("%s has no English text: %q", tool, got)
		}
	}
}

func hasLatin(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' {
			return true
		}
	}
	return false
}

func TestTheThinkingCardShowsWhatIsDone(t *testing.T) {
	t0 := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	var log thinkLog
	log.start(0, "web_search", "جستجو «قیمت دلار»", t0)
	log.end(0, "web_search", false, t0.Add(2*time.Second))
	log.start(0, "web_fetch", "خوندن tgju.org", t0.Add(2*time.Second))
	log.end(0, "web_fetch", true, t0.Add(3*time.Second))
	log.start(0, "generate_chart", "کشیدن نمودار «روند»", t0.Add(3*time.Second))
	log.progress(0, "generate_chart", "2 series")

	md := log.markdown(fa, t0.Add(14*time.Second), t0, "")
	for _, want := range []string{
		"🔎 دارم توی اینترنت می‌گردم…",
	} {
		_ = want
	}
	for _, want := range []string{
		"📊 دارم نمودار رو می‌کشم…",
		"`0:14`",
		"- 🔎 جستجو «قیمت دلار» ✅ `2s`",
		"- 📖 خوندن tgju.org ❌",
		"- 📊 **کشیدن نمودار «روند»** ⏳ `11s` · 2 series",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("the card misses %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "MACRON") || strings.Contains(md, "```") {
		t.Errorf("it is not the terminal:\n%s", md)
	}

	log.end(0, "generate_chart", false, t0.Add(8*time.Second))
	md = log.markdown(en, t0.Add(9*time.Second), t0, "")
	if !strings.HasPrefix(md, "💭 Thinking") && !strings.Contains(md, thinkingStatus(en)) {
		t.Errorf("between steps: %s", md)
	}
	if !strings.Contains(md, "✅ `5s`") {
		t.Errorf("a finished step shows its time:\n%s", md)
	}

	for i := range 9 {
		log.start(0, "web_search", fmt.Sprintf("جستجو «%d»", i), t0)
		log.end(0, "web_search", false, t0)
	}
	md = log.markdown(fa, t0.Add(20*time.Second), t0, "")
	if n := strings.Count(md, "\n- "); n != thinkShown {
		t.Errorf("%d steps show, want %d:\n%s", n, thinkShown, md)
	}
	if !strings.Contains(md, "و ۶ کار قبلی…") || !strings.Contains(md, "«8»") || strings.Contains(md, "«2»") {
		t.Errorf("the latest steps, and a count of the earlier ones:\n%s", md)
	}
	if got := len(log.steps); got > thinkKept {
		t.Errorf("memory is bounded: %d", got)
	}

	md = log.markdown(fa, t0.Add(20*time.Second), t0, "📋 `1/2`\n\n- [x] یک\n- [ ] دو")
	if i, j := strings.Index(md, "📋"), strings.Index(md, "\n- 🔎"); i < 0 || j < i {
		t.Errorf("the plan comes before the steps:\n%s", md)
	}
}

func TestThinkingCardEscapesWhatStepsAreAbout(t *testing.T) {
	var log thinkLog
	log.start(0, "web_search", thinkText(en, "web_search", `{"query":"*bold* _x_ [a](b) # c"}`), time.Now())
	md := log.markdown(en, time.Now(), time.Now(), "")
	if strings.Contains(md, "*bold*") || strings.Contains(md, "[a](b)") {
		t.Errorf("not escaped: %s", md)
	}
}

func TestSubAgentsShowInTheThinkingCard(t *testing.T) {
	l := &liveView{lng: en, started: time.Now(), dirty: false}
	id := l.subAgent("Excel report")
	if id == 0 || !l.noting {
		t.Fatalf("a sub-agent without the terminal still gets an id: %d", id)
	}
	l.toolStartWith(id, "run_code", `{"language":"python"}`)
	l.toolEnd(id, "run_code", "ok")
	body, _ := l.groupBody()
	if !strings.Contains(body, "Helper: Excel report") || !strings.Contains(body, "Running python code ✅") {
		t.Errorf("the helper and its call show:\n%s", body)
	}
	l.subDone(id, false)
	if body, _ = l.groupBody(); !strings.Contains(body, "🤖 Helper: Excel report ✅") {
		t.Errorf("the helper ends:\n%s", body)
	}
}

func TestThinkingCardInTheDraftWhenTheTerminalIsOff(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingLiveTrace, "false")
	_ = h.bot.app.Machine.WriteFile(sessionFor(27), "notes.txt", []byte("hi"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("read_file", `{"filename":"notes.txt"}`), 0
		case 1:
			time.Sleep(1500 * time.Millisecond)
			return toolCall("bash_execute", `{"command":"cat /etc/passwd | grep root"}`), 0
		}
		time.Sleep(900 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "تمام شد."}, 0
	}
	h.send(private(27, "یه نگاهی بنداز"))
	all := liveFrames(h)
	joined := strings.Join(all, "\n=====\n")
	for _, want := range []string{"دارم فکر", "خوندن «notes.txt»"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the draft misses %q:\n%s", want, joined)
		}
	}
	for _, hidden := range []string{traceTitle, "MACRON", "cat /etc/passwd", "grep root", "/workspace"} {
		if strings.Contains(joined, hidden) {
			t.Errorf("the card shows %q:\n%s", hidden, joined)
		}
	}
	h.wantText("تمام شد.")
}

func TestThinkingCardIsOnlyForTheTerminalOff(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("list_files", `{}`), 0
		}
		time.Sleep(900 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "خالیه."}, 0
	}
	h.send(private(28, "فایل‌هام؟"))
	for _, frame := range liveFrames(h) {
		if s := frame; strings.Contains(s, "نگاه به فایل‌ها") {
			t.Errorf("the terminal's draft has the card's text:\n%s", s)
		}
	}
}
