package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// filesSent lists the Telegram calls that carried a file.
func filesSent(h *harness) []string {
	var out []string
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	for _, c := range h.tg.calls {
		switch c.method {
		case "sendPhoto", "sendDocument", "sendAudio", "sendVideo", "sendMediaGroup", "sendAnimation", "sendVoice":
			out = append(out, c.method)
		case "sendRichMessage", "editMessageText", "editRichMessage":
			if rm, ok := c.body["rich_message"].(map[string]any); ok && (rm["media"] != nil || strings.Contains(fmt.Sprint(rm), "attach://")) {
				out = append(out, c.method+"+media")
			}
		}
	}
	return out
}

// TestTheModelChoosesWhatIsSent: what a tool made is not sent by itself;
// the model is reminded once, and sends it by showing it in its answer.
func TestTheModelChoosesWhatIsSent(t *testing.T) {
	h := newHarness(t)
	// These tests count foreground requests; learning has its own suite.
	if err := h.st.SetSetting(context.Background(), settingLearn, "false"); err != nil {
		t.Fatal(err)
	}
	const uid = 71
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("generate_qr", `{"data":"https://example.com","filename":"site_qr.png"}`), 0
		case 1:
			return map[string]any{"role": "assistant", "content": "QR کدت آماده‌ست."}, 0
		}
		return map[string]any{"role": "assistant", "content": "QR کدت آماده‌ست:\n![QR سایت](site_qr.png)"}, 0
	}
	h.send(private(uid, "برای example.com یه QR کد بساز"))
	if n := h.llm.count(); n != 3 {
		t.Fatalf("%d model calls: the answer that sent nothing must be reminded once", n)
	}
	if last := llmText(h.llm.last()); !strings.Contains(last, "Only what you choose is sent") || !strings.Contains(last, "site_qr.png") {
		t.Fatalf("no reminder: %s", last)
	}
	if got := filesSent(h); len(got) == 0 {
		t.Fatalf("the QR code the model showed was not sent; calls: %v", h.tg.allText())
	}
}

// TestAFileTheModelDoesNotChooseStays: a download fetched for the work is
// not sent when the model, reminded, still chooses nothing.
func TestAFileTheModelDoesNotChooseStays(t *testing.T) {
	h := newHarness(t)
	// These tests count foreground requests; learning has its own suite.
	if err := h.st.SetSetting(context.Background(), settingLearn, "false"); err != nil {
		t.Fatal(err)
	}
	const uid = 72
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("generate_qr", `{"data":"test","filename":"probe.png"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "تست انجام شد، همه‌چی درسته ✅"}, 0
	}
	h.send(private(uid, "یه تست کوچیک بکن"))
	if n := h.llm.count(); n != 3 {
		t.Fatalf("%d model calls: one reminder, then the answer stands", n)
	}
	if got := filesSent(h); len(got) != 0 {
		t.Fatalf("a file the model did not choose was sent: %v", got)
	}
	h.wantText("تست انجام شد")
}
