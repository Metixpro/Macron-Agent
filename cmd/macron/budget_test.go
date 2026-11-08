package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestAgentBudgetFromThePanelAndContinuation(t *testing.T) {
	h := newHarness(t)
	if steps, timeout := h.bot.agentBudget(); steps != 4 || timeout != 30*time.Second {
		t.Fatalf("server defaults: %d %s", steps, timeout)
	}
	h.click(ownerID, ownerID, 9, "a:st:steps")
	h.send(private(ownerID, "2"))
	h.wantText("یه عدد بین 5 و 1000 بفرست")
	h.send(private(ownerID, "۶"))
	h.click(ownerID, ownerID, 9, "a:st:minutes")
	h.send(private(ownerID, "45"))
	if steps, timeout := h.bot.agentBudget(); steps != 6 || timeout != 45*time.Minute {
		t.Fatalf("panel budget: %d %s", steps, timeout)
	}
	h.tg.reset()
	h.click(ownerID, ownerID, 9, "a:ai")
	h.wantText("قدرت ایجنت: ⏩ ادامه خودکار: خاموش — هر بار 6 قدم")
	h.wantText("ترمز: 45 دقیقه، یا 15 دقیقه بی‌کاری")

	// A job bigger than the budget: a report, and the work log in memory.
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if strings.Contains(llmText(req), "Stop working now") {
			return map[string]any{"role": "assistant", "content": "نصفش انجام شد؛ بگو ادامه بده."}, 0
		}
		return toolCall("list_files", `{}`), 0
	}
	h.send(private(70, "یه کار خیلی بزرگ"))
	h.wantText("نصفش انجام شد")
	if !strings.Contains(llmText(h.llm.requests[0]), "up to 6 tool rounds and about 45 minutes") {
		t.Error("the model must know the budget")
	}
	mem, _ := h.bot.mem.Load(bg, 70)
	if last := mem.History[len(mem.History)-1].Content; !strings.Contains(last, "[work log before stopping") || strings.Count(last, "list_files") != 6 {
		t.Errorf("memory keeps the work log:\n%s", last)
	}
	// Back to the server's defaults.
	h.st.SetSetting(bg, store.SettingAgentSteps, "0")
	if steps, _ := h.bot.agentBudget(); steps != 4 {
		t.Errorf("0 = default: %d", steps)
	}
}

func TestAnAnswerIsNeverLost(t *testing.T) {
	h := newHarness(t)
	h.tg.setReject(func(method string, body map[string]any) string {
		text, _ := body["text"].(string)
		if method == "sendRichMessage" || (method == "sendMessage" && strings.Contains(text, "<b>")) {
			return "Bad Request: something went wrong"
		}
		return ""
	})
	h.llm.reply = say("**مهم:** جواب کامل اینجاست")
	h.send(private(71, "سلام"))
	delivered := false
	for _, c := range h.tg.of("sendMessage") {
		if text, _ := c.body["text"].(string); strings.Contains(text, "**مهم:** جواب کامل اینجاست") {
			delivered = true
		}
	}
	if !delivered {
		t.Errorf("the answer was lost:\n%s", h.tg.allText())
	}
}

// The ⏩ button goes forever → 3 → 10 → off → forever; forever is the default.
func TestAutoContinueButton(t *testing.T) {
	h := newHarness(t)
	_ = h.st.DeleteSetting(bg, store.SettingAutoContinue)
	if h.bot.autoContinue() != autoForever || h.bot.agentExtensions() != agent.ExtendForever {
		t.Fatalf("default %d", h.bot.autoContinue())
	}
	for _, want := range []int{3, 10, autoOff, autoForever} {
		h.click(ownerID, ownerID, 9, "a:st:auto")
		if got := h.bot.autoContinue(); got != want {
			t.Fatalf("after a click: %d, want %d", got, want)
		}
	}
	h.click(ownerID, ownerID, 9, "a:st:auto")
	h.wantText("قدرت ایجنت: ⏩ تا 3 تمدید")
	if h.bot.agentExtensions() != 3 {
		t.Fatal("3 extensions")
	}
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "-1")
	if h.bot.agentExtensions() != 0 {
		t.Fatal("off is a fixed budget")
	}
}
