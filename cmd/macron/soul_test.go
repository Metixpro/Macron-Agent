package main

import (
	"strings"
	"testing"
)

func TestSoulInThePanel(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 900, "a:soul")
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "پیش‌فرض") || !hasButton(buttons(v), "a:soul:set") {
		t.Fatalf("soul panel: %v", v.body)
	}
	h.click(ownerID, ownerID, 900, "a:soul:get")
	if d := lastOf(h, "sendDocument"); d.body["document_name"] != "SOUL.md" {
		t.Errorf("download: %v", d.body)
	}
	h.click(ownerID, ownerID, 900, "a:soul:set")
	h.send(private(ownerID, "تو {{name_fa}} ({{name}}) هستی؛ یه دزد دریایی شوخ‌طبع 🏴‍☠️"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		return map[string]any{"role": "assistant", "content": "آهوی!"}, 0
	}
	h.send(private(77, "سلام"))
	p := llmText(h.llm.last())
	if !strings.Contains(p, "تو مکرون (Macron Ai) هستی؛ یه دزد دریایی شوخ‌طبع") || !strings.Contains(p, "technical details are not public") {
		t.Error("the new soul is used and the ground rules stay")
	}
	h.click(ownerID, ownerID, 900, "a:soul:reset")
	h.send(private(77, "سلام"))
	if !strings.Contains(llmText(h.llm.last()), "قواعد اجرایی") {
		t.Error("back to the default soul")
	}
}
