package main

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

func needsQRCode(t *testing.T) {
	t.Helper()
	config.ActivateToolEnv()
	if err := exec.Command("python3", "-c", "import qrcode").Run(); err != nil {
		t.Skipf("generate_qr needs the Python qrcode package: %v", err)
	}
}

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

func TestTheModelChoosesWhatIsSent(t *testing.T) {
	needsQRCode(t)
	h := newHarness(t)

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

func TestAFileTheModelDoesNotChooseStays(t *testing.T) {
	needsQRCode(t)
	h := newHarness(t)

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
