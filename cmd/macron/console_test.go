package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// consoleJob is a job half way: a plan, the context, done, failed and
// running steps, and a sub-agent at work.
func consoleJob(now time.Time) trace {
	tr := trace{console: true}
	tr.plan = []agent.PlanItem{{Text: "گرفتن قیمت طلا", Status: agent.PlanDone}, {Text: "کشیدن نمودار", Status: agent.PlanInProgress},
		{Text: "فرستادن نتیجه", Status: agent.PlanPending}}
	tr.ctxUsed, tr.ctxWindow = 105000, 256000
	for range 2 {
		tr.start(0, "web_search", now)
		tr.end(0, "web_search", false)
	}
	tr.start(0, "web_fetch", now)
	tr.end(0, "web_fetch", true)
	tr.start(0, "generate_chart", now)
	tr.progress(0, "generate_chart", "rendering `2` series", true)
	s := tr.subStart("Excel report", now)
	tr.start(s, tools.ToolCreateSpreadsheet, now)
	tr.end(s, tools.ToolCreateSpreadsheet, false)
	tr.start(s, tools.ToolRunCode, now)
	tr.progress(s, tools.ToolRunCode, "45% · 3/7", false)
	return tr
}

func TestTheConsoleDrawsIconLines(t *testing.T) {
	now := time.Now()
	tr := consoleJob(now)
	md := tr.markdown(now.Add(21*time.Second), now, true)
	want := "💭 **MACRON** ⟫ live `step 6` `0:21`\n\n" +
		"📋 `1/3`\n\n- [x] گرفتن قیمت طلا\n- [ ] ⏳ **کشیدن نمودار**\n- [ ] فرستادن نتیجه\n\n" +
		"🧠 `ctx` 🟩🟩🟩⬛⬛⬛⬛⬛ `41% · 105k/256k`\n" +
		"🔎 Search `×2` ✅\n" +
		"🌐 Read Page ❌\n" +
		"📊 **Chart** ⏳ `⠼ 21s`\n" +
		"\u2003↳ `rendering '2' series`\n" +
		"🤖 **Sub-agent: Excel report** ⏳ `⠼ 21s`\n" +
		"├ 📗 Spreadsheet ✅\n" +
		"└ 🧪 **Run Code** ⏳ `⠼ 21s · 45% · 3/7`"
	if md != want {
		t.Fatalf("console:\n%s\nwant:\n%s", md, want)
	}

	// A group's message changes rarely: no running times, no spinner.
	if g := tr.markdown(now.Add(21*time.Second), now, false); !strings.Contains(g, "\n📊 **Chart** ⏳\n") || strings.Contains(g, "⠼") {
		t.Errorf("a group's console:\n%s", g)
	}
	// The classic message: the same lines in HTML, never a code block.
	h := tr.html(now.Add(21*time.Second), now, false)
	for _, w := range []string{"💭 <b>MACRON</b> ⟫ live <code>step 6</code>", "🔎 Search <code>×2</code> ✅", "└ 🧪 <b>Run Code</b> ⏳ <code>45% · 3/7</code>"} {
		if !strings.Contains(h, w) {
			t.Errorf("HTML misses %q:\n%s", w, h)
		}
	}
	if strings.Contains(h, "<pre") || strings.Contains(md, "```") {
		t.Error("the console has no code block: premium emoji cannot live in one")
	}

	// A look-alike with a premium emoji is used; an animated one needs no spinner.
	tr.icons = iconPicker{emoji.New(map[string]string{"⌛": "11", "✔️": "12"})}
	md = tr.markdown(now.Add(21*time.Second), now, true)
	for _, w := range []string{"📊 **Chart** ⌛ `21s`", "🔎 Search `×2` ✔️", "- [x] گرفتن قیمت طلا", "- [ ] ⌛ **کشیدن نمودار**"} {
		if !strings.Contains(md, w) {
			t.Errorf("premium look-alikes: misses %q:\n%s", w, md)
		}
	}
	if strings.Contains(md, "⠼") {
		t.Errorf("a premium running icon moves by itself:\n%s", md)
	}

	// Events, an unknown tool, and a title that needs escaping.
	var ev trace
	ev.console = true
	ev.event("New Message", now)
	ev.compacted(160000, 80000, true, now)
	ev.start(0, "my_plugin_tool", now)
	ev.subStart("a*b_c", now)
	out := ev.markdown(now, now, false)
	for _, w := range []string{"💬 New Message\n", "🗜️ Context compacted `160k ➔ 80k (-50%)`", "🔌 **My Plugin Tool** ⏳", `🤖 **Sub-agent: a\*b\_c** ⏳`} {
		if !strings.Contains(out, w) {
			t.Errorf("misses %q:\n%s", w, out)
		}
	}
	// The classic code block stays as it was for the trace's zero value.
	var classic trace
	classic.start(0, "web_search", now)
	if c := classic.markdown(now, now, false); !strings.Contains(c, "```bash") || !strings.Contains(c, "◆ Search") {
		t.Errorf("the classic style:\n%s", c)
	}
}

func TestTheWorkLog(t *testing.T) {
	now := time.Now()
	tr := consoleJob(now)
	tr.end(0, "generate_chart", false)
	for _, s := range tr.steps {
		if s.sub {
			tr.end(s.id, tools.ToolRunCode, false)
			tr.subEnd(s.id, false)
		}
	}
	if tr.workLog(fa, 5*time.Second) != "" {
		t.Error("a quick job needs no work log")
	}
	var one trace
	one.start(0, "web_search", now)
	one.end(0, "web_search", false)
	if one.workLog(fa, time.Minute) != "" {
		t.Error("one tool call needs no work log")
	}
	log := tr.workLog(fa, 42*time.Second)
	if !strings.HasPrefix(log, "<details><summary>**🧰 گزارش کار · ۶ ابزار · ۰:۴۲**</summary>\n\n🔎 Search `×2` ✅\n") ||
		!strings.HasSuffix(log, "\n\n</details>") || strings.Contains(log, "⏳") || strings.Contains(log, "ctx") {
		t.Errorf("work log:\n%s", log)
	}
	if en := tr.workLog(en, 75*time.Second); !strings.Contains(en, "**🧰 Work log · 6 tools · 1:15**") {
		t.Errorf("English work log:\n%s", en)
	}
}

func TestTheConsoleInTheLiveCard(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingPremiumEmoji, "true")
	_ = h.st.SetPremiumEmoji(bg, "📁", "5294096239464295059")
	h.bot.reloadEmoji(bg)
	_ = h.bot.app.Machine.WriteFile(sessionFor(26), "notes.txt", []byte("hi"))
	job := func() {
		calls := 0
		h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
			if calls++; calls == 1 {
				return toolCall("list_files", `{}`), 0
			}
			time.Sleep(900 * time.Millisecond)
			return map[string]any{"role": "assistant", "content": "یه فایل داری."}, 0
		}
	}
	job()
	drafts := func() string {
		var all []string
		for _, d := range h.tg.of("sendRichMessageDraft") {
			all = append(all, fmt.Sprint(d.body["rich_message"]))
		}
		return strings.Join(all, "\n=====\n")
	}
	h.send(private(26, "چه فایل‌هایی دارم؟"))
	if d := drafts(); !strings.Contains(d, "![📁](tg://emoji?id=5294096239464295059) Files") || strings.Contains(d, "```") {
		t.Errorf("the tool's icon is its premium emoji:\n%s", d)
	}

	// The classic code block is still there for whoever prefers it.
	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:st:tstyle")
	if h.bot.consoleStyle() {
		t.Fatal("the style did not change")
	}
	job()
	h.send(private(26, "و الان؟"))
	if d := drafts(); !strings.Contains(d, "```bash") || !strings.Contains(d, "✓ Files") {
		t.Errorf("the classic terminal:\n%s", d)
	}
	h.click(ownerID, ownerID, 9, "a:st:tstyle")
	if !h.bot.consoleStyle() {
		t.Error("back to the console")
	}
}

func TestTheWorkLogGoesAboveTheAnswer(t *testing.T) {
	h := newHarness(t)
	m := private(27, "سلام")
	h.bot.sendAnswerWithFiles(context.Background(), m, sessionFor(27), "جواب اصلی", "HEAD", nil, nil, "", nil)
	if all := h.tg.allText(); !strings.Contains(all, "HEAD\n\nجواب اصلی") {
		t.Errorf("the work log above the answer:\n%s", all)
	}

	now := time.Now()
	lv := &liveView{b: h.bot, lng: fa, started: now.Add(-time.Minute), tr: consoleJob(now)}
	if lv.workLog() == "" {
		t.Fatal("a long job has its work log")
	}
	h.click(ownerID, ownerID, 9, "a:st:worklog")
	if lv.workLog() != "" {
		t.Error("the setting turns it off")
	}
}

func TestImportingAPremiumEmojiPack(t *testing.T) {
	if packName("https://t.me/addemoji/vector_icons_by_fStikBot") != "vector_icons_by_fStikBot" || packName("tg://addemoji?set=Abc_1") != "Abc_1" ||
		packName("t.me/addstickers/Cats") != "Cats" || packName("Vector_Icons") != "Vector_Icons" || packName("سلام") != "" || packName("a b") != "" {
		t.Error("packName")
	}
	h := newHarness(t)
	_ = h.st.SetPremiumEmoji(bg, "📁", "1111")
	_ = h.st.SetPremiumEmoji(bg, "❤", "2222")
	_ = h.st.SetPremiumEmoji(bg, "✅", "7002")
	h.bot.reloadEmoji(bg)
	h.tg.mu.Lock()
	h.tg.sets["vector_icons_by_fStikBot"] = `[{"emoji_list":["🔎"]},{"emoji_list":["📁"]},{"emoji_list":["✅"]},{"emoji_list":["🔎"]}]`
	h.tg.mu.Unlock()

	h.click(ownerID, ownerID, 9, "a:em:pack")
	h.send(private(ownerID, "https://t.me/addemoji/vector_icons_by_fStikBot"))
	h.wantText("✅ پک «t» وارد شد: ۱ ایموجی جدید، ۱ جایگزین و ۱ از قبل همین بود.")
	h.wantText("♻️ ۱ تا ایموجی توی خود پک تکراری بود")
	h.wantText("ایموجی پریمیوم خاموشه")
	got, _ := h.st.PremiumEmoji(bg)
	if got["🔎️"] != "7000" || got["📁️"] != "7001" || got["📁"] != "" || got["❤"] != "2222" || got["✅"] != "7002" {
		t.Errorf("mappings after the import: %v", got)
	}
	if id, _ := h.bot.emojis.Load().ID("🔎"); id != "7000" {
		t.Errorf("the set is reloaded: %q", id)
	}

	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:em:icons")
	if all := h.tg.allText(); !strings.Contains(all, "✅ 🔎 Search") || !strings.Contains(all, "▫️ 🌐 Read Page (یا 📰)") {
		t.Errorf("the console's icons:\n%s", all)
	}

	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:em:pack")
	h.send(private(ownerID, "no_such_pack"))
	h.wantText("پیدا نشد")
}

// sentReport reports whether report.pdf reached the chat: as a document, or
// inside the answer's rich message.
func sentReport(h *harness) bool {
	for _, d := range h.tg.of("sendDocument") {
		if d.body["document_name"] == "report.pdf" {
			return true
		}
	}
	for _, r := range h.tg.of("sendRichMessage") {
		if strings.Contains(fmt.Sprint(r.body), "report.pdf") {
			return true
		}
	}
	return false
}

// A file the model attached goes to the user even when the job then runs
// out of time: the live card said it would, and that work is done.
func TestAttachedFilesGoEvenWhenTheJobRunsOutOfTime(t *testing.T) {
	h := newHarness(t)
	h.bot.runTimeout = 1500 * time.Millisecond
	const uid = 28
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "report.pdf", []byte("%PDF-1.4 fake report"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolSendFile, `{"filename":"report.pdf","caption":"گزارش"}`), 0
		}
		time.Sleep(3 * time.Second) // past the request's time
		return map[string]any{"role": "assistant", "content": "تموم شد"}, 0
	}
	h.send(private(uid, "گزارش رو بساز و بفرست"))
	if !sentReport(h) {
		t.Fatalf("the attached file did not go:\n%s", h.tg.allText())
	}
	mc, err := h.bot.mem.Load(context.Background(), uid)
	if err != nil || len(mc.History) == 0 || !strings.Contains(mc.History[len(mc.History)-1].Content, "<files_sent>report.pdf</files_sent>") {
		t.Errorf("the memory knows the file arrived: %+v %v", mc, err)
	}
	if strings.Contains(h.tg.allText(), "Send File") {
		t.Error("the card says the file is attached, not sent")
	}
}

// A stop is a stop: what was attached stays in the workspace for «continue».
func TestAStoppedJobSendsNoFiles(t *testing.T) {
	h := newHarness(t)
	old := stopButtonAfter
	stopButtonAfter = 0
	t.Cleanup(func() { stopButtonAfter = old })
	const uid = 29
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "report.pdf", []byte("%PDF-1.4 fake report"))
	second, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	let := func() { once.Do(func() { close(release) }) }
	t.Cleanup(let)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall(toolSendFile, `{"filename":"report.pdf"}`), 0
		case 1:
			close(second)
			<-release
		}
		return map[string]any{"role": "assistant", "content": "تموم شد"}, 0
	}
	wait := h.background(group(uid, "مکرون گزارش رو بفرست"))
	<-second
	for i := 0; i < 50 && stopButtonData(h) == ""; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	h.click(uid, -100500, 501, stopButtonData(h))
	let()
	wait()
	h.wantText("متوقفش کردم")
	if sentReport(h) {
		t.Error("a stopped job sends nothing more")
	}
}

// TestThePlanIsATaskList: rich messages draw «- [ ]» and «- [x]» as real
// checkboxes; each step is its own list item (lines that only follow each
// other would run into one paragraph), and a step's own number, dash or
// brackets stay text.
func TestThePlanIsATaskList(t *testing.T) {
	items := []agent.PlanItem{
		{Text: "استخراج ساختار مخزن و خواندن SKILL.md", Status: agent.PlanDone},
		{Text: "نصب وابستگی‌های پایتونی و تست رندر PDFها", Status: agent.PlanInProgress},
		{Text: "1. ثبت دو اسکیل [pdf-atelier]", Status: agent.PlanPending},
		{Text: "- گزارش نهایی", Status: agent.PlanPending},
	}
	want := "📋 `1/4`\n\n" +
		"- [x] استخراج ساختار مخزن و خواندن SKILL.md\n" +
		"- [ ] ⏳ **نصب وابستگی‌های پایتونی و تست رندر PDFها**\n" +
		"- [ ] 1\\. ثبت دو اسکیل \\[pdf-atelier\\]\n" +
		"- [ ] \\- گزارش نهایی"
	if got := planMarkdown(items); got != want {
		t.Fatalf("plan:\n%s\nwant:\n%s", got, want)
	}
	if planMarkdown(nil) != "" {
		t.Fatal("no plan, nothing")
	}
	// The classic (HTML) card keeps its icons: its line breaks are kept as they are.
	if h := planHTML(items); !strings.Contains(h, "✅ استخراج") || !strings.Contains(h, "\n⏳ <b>") {
		t.Fatalf("html: %s", h)
	}
}
