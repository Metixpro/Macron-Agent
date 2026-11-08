package main

import (
	"strings"
	"testing"
	"time"

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
	h.wantText("قدرت ایجنت: <b>6</b> قدم و <b>45</b> دقیقه")

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
