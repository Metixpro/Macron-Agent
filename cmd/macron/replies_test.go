package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestPrivateReplyIncludesOriginalMessageAndRichText(t *testing.T) {
	h := newHarness(t)
	sara := &telegram.User{ID: 77, FirstName: "Sara"}

	m := private(40, "درباره این پیام توضیح بده")
	m.ReplyToMessage = &telegram.Message{MessageID: 11, From: sara, Chat: m.Chat, Text: "Is the meeting moved to Monday?"}
	h.send(m)
	req := llmText(h.llm.last())
	for _, want := range []string{"Is the meeting moved to Monday?", "replying to a message from", "Sara"} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "پیام خالی") || strings.Contains(req, textOnlyName) {
		t.Errorf("a call on a reply is not an empty message:\n%s", req)
	}

	var rich json.RawMessage = []byte(`{"blocks":[{"type":"paragraph","text":["قیمت طلا امروز ",{"type":"bold","text":"۵ میلیون"}]}]}`)
	m2 := private(41, "مکرون این قیمت مال کی بود؟")
	m2.ReplyToMessage = &telegram.Message{MessageID: 12, From: &telegram.User{ID: 42, IsBot: true, FirstName: "Macron"}, Chat: m2.Chat, RichMessage: rich}
	h.send(m2)
	if req := llmText(h.llm.last()); !strings.Contains(req, "قیمت طلا امروز ۵ میلیون") || !strings.Contains(req, "your own earlier message") {
		t.Errorf("the bot's rich answer is read from its blocks:\n%s", req)
	}
}

func TestGuestExternalReply(t *testing.T) {
	h := newHarness(t)
	h.tg.files["ext"] = testJPEG(40, 40)
	m := guestMsg(74, "@WazeBot")
	m.Quote = &telegram.TextQuote{Text: "Bonjour tout le monde"}
	m.ExternalReply = &telegram.ExternalReplyInfo{Origin: &telegram.MessageOrigin{Type: "user", SenderUser: &telegram.User{ID: 99, FirstName: "Lea"}},
		Photo: []telegram.PhotoSize{{FileID: "ext", Width: 40, Height: 40}}}
	h.bot.handleGuest(m)
	req := llmText(h.llm.last())
	for _, want := range []string{"Bonjour tout le monde", "Lea", "data:image/jpeg"} {
		if !strings.Contains(req, want) {
			t.Errorf("guest request misses %q:\n%s", want, req)
		}
	}
}
