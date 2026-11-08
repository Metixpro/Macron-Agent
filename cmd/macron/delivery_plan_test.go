package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
)

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
	h.click(ownerID, ownerID, 9, "a:em:pack")
	h.send(private(ownerID, "no_such_pack"))
	h.wantText("پیدا نشد")
}

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

func TestAttachedFilesGoEvenWhenTheJobRunsOutOfTime(t *testing.T) {
	h := newHarness(t)
	h.bot.runTimeout = 1500 * time.Millisecond
	const uid = 28
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "report.pdf", []byte("%PDF-1.4 fake report"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolSendFile, `{"filename":"report.pdf","caption":"گزارش"}`), 0
		}
		time.Sleep(3 * time.Second)
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

	if h := planHTML(items); !strings.Contains(h, "✅ استخراج") || !strings.Contains(h, "\n⏳ <b>") {
		t.Fatalf("html: %s", h)
	}
}
