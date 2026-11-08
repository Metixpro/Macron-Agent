package main

import (
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestForwardedMessagesReachTheModelWithTheirOrigin(t *testing.T) {
	h := newHarness(t)
	m := private(60, "این خبر رو ببین")
	m.ForwardOrigin = &telegram.MessageOrigin{Type: "channel", Date: 1, MessageID: 77,
		Chat: &telegram.Chat{ID: -1001234, Type: "channel", Title: "News", Username: "news"}}
	h.send(m)
	if req := llmText(h.llm.last()); !strings.Contains(req, "forwarded from News · @news · id: -1001234 · msg_id: 77") {
		t.Errorf("channel origin missing:\n%s", req)
	}
	mem, _ := h.bot.mem.Load(bg, 60)
	if !strings.Contains(mem.History[0].Content, "[forwarded from News · @news · id: -1001234 · msg_id: 77]") {
		t.Errorf("memory note: %q", mem.History[0].Content)
	}

	// A reply to a message someone forwarded from a hidden user.
	r := private(60, "این چی می‌گه؟")
	r.ReplyToMessage = &telegram.Message{MessageID: 5, From: &telegram.User{ID: 99, FirstName: "Sara", Username: "sara"},
		Chat: r.Chat, Text: "متن فوروارد شده", ForwardSenderName: "Mina"}
	h.send(r)
	req := llmText(h.llm.last())
	for _, want := range []string{"Sara · @sara · id: 99", "who forwarded it from Mina (hidden user, ID not provided by Telegram)"} {
		if !strings.Contains(req, want) {
			t.Errorf("reply context misses %q:\n%s", want, req)
		}
	}
}

func TestSafetyRefusalIsExplainedAndRefunded(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(map[string]any, int) (map[string]any, int) {
		return map[string]any{"error": `{"error":{"message":"Request blocked by the Prohibited Use policy"}}`}, 400
	}
	h.send(private(61, "یه چیزی"))
	h.wantText("با قوانین ایمنی سرویس‌های هوش مصنوعی سازگار نیست")
	h.noText("Prohibited")
	if n, _ := h.st.UsedToday(bg, 61); n != 0 {
		t.Errorf("a refused request is refunded, used=%d", n)
	}
	h.tg.reset()
	h.send(englishMsg(62, "something"))
	h.wantText("doesn't fit the safety rules")
}
