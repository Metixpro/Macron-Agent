package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestGroupCallOnAReplyIsAboutThatMessage(t *testing.T) {
	h := newHarness(t)
	sara := &telegram.User{ID: 77, FirstName: "Sara"}

	for _, call := range []string{"@WazeBot", "مکرون"} {
		m := group(40, call)
		m.ReplyToMessage = &telegram.Message{MessageID: 11, From: sara, Chat: m.Chat, Text: "Is the meeting moved to Monday?"}
		ok, text := h.bot.addressed(m)
		if !ok || text != textCalledOnReply {
			t.Fatalf("%q on a reply → %v %q", call, ok, text)
		}
	}
	m := group(40, "@WazeBot")
	m.ReplyToMessage = &telegram.Message{MessageID: 11, From: sara, Chat: m.Chat, Text: "Is the meeting moved to Monday?"}
	h.send(m)
	req := llmText(h.llm.last())
	for _, want := range []string{"Is the meeting moved to Monday?", "replying to a message from", "Sara", "روی یه پیام ریپلای کرد"} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q:\n%s", want, req)
		}
	}
	if strings.Contains(req, "پیام خالی") || strings.Contains(req, textOnlyName) {
		t.Errorf("a call on a reply is not an empty message:\n%s", req)
	}

	var rich json.RawMessage = []byte(`{"blocks":[{"type":"paragraph","text":["قیمت طلا امروز ",{"type":"bold","text":"۵ میلیون"}]}]}`)
	m2 := group(41, "مکرون این قیمت مال کی بود؟")
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
	for _, want := range []string{"Bonjour tout le monde", "Lea", "data:image/jpeg", "روی یه پیام ریپلای کرد"} {
		if !strings.Contains(req, want) {
			t.Errorf("guest request misses %q:\n%s", want, req)
		}
	}
}

func TestGroupReplyToAnAnswerForSomeoneElse(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = say("طلا امروز حدود ۵ میلیونه")
	ask := group(40, "مکرون قیمت طلا امروز چنده؟")
	ask.From = &telegram.User{ID: 40, FirstName: "Sara"}
	h.send(ask)

	h.bot.fb.mu.Lock()
	var answerID int64
	for id, ref := range h.bot.fb.refs[ask.Chat.ID] {
		if ref.askerID != 40 || !strings.Contains(ref.question, "قیمت طلا") {
			t.Errorf("answer %d linked to %+v", id, ref)
		}
		answerID = max(answerID, id)
	}
	h.bot.fb.mu.Unlock()
	if answerID == 0 {
		t.Fatal("the group answer's messages are linked to Sara's request")
	}
	answer := func(m *telegram.Message, id int64) {
		m.ReplyToMessage = &telegram.Message{MessageID: id, From: &telegram.User{ID: botID, IsBot: true, FirstName: "Macron"}, Chat: m.Chat, Text: "طلا امروز حدود ۵ میلیونه"}
	}

	reza := group(41, "منبعت کجاست؟ من شنیدم ۶ میلیونه")
	reza.From = &telegram.User{ID: 41, FirstName: "Reza"}
	answer(reza, answerID)
	if ok, _ := h.bot.addressed(reza); !ok {
		t.Fatal("a reply to the bot's answer is for the bot")
	}
	h.send(reza)
	req := llmText(h.llm.last())
	for _, want := range []string{"your answer to Sara", "قیمت طلا امروز چنده", "The person writing now is Reza", "a different person", "don't repeat your whole earlier answer", "منبعت کجاست"} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q:\n%s", want, req)
		}
	}

	sara := group(40, "یعنی بخرم؟")
	sara.From = &telegram.User{ID: 40, FirstName: "Sara"}
	answer(sara, answerID)
	h.send(sara)
	if req := llmText(h.llm.last()); !strings.Contains(req, "this same person's earlier request") || strings.Contains(req, "a different person") {
		t.Errorf("Sara's follow-up on her own answer:\n%s", req)
	}

	old := group(41, "این چی بود؟")
	old.From = &telegram.User{ID: 41, FirstName: "Reza"}
	answer(old, 3)
	h.send(old)
	if req := llmText(h.llm.last()); !strings.Contains(req, "may have been an answer to someone else") {
		t.Errorf("an unknown answer of the bot's:\n%s", req)
	}
}

func TestWakeWordOnSomeoneElsesMessage(t *testing.T) {
	h := newHarness(t)
	sara := &telegram.User{ID: 77, FirstName: "Sara"}
	for i, text := range []string{"مکرون نظرت چیه", "مکرون، نظرت چیه؟", "Macron what do you think", "ماکرون این درسته؟"} {
		before := h.llm.count()
		m := group(40, text)
		m.MessageID = int64(100 + i)
		m.ReplyToMessage = &telegram.Message{MessageID: 11, From: sara, Chat: m.Chat, Text: "به نظرم فردا بارون میاد"}
		h.send(m)
		if h.llm.count() == before {
			t.Errorf("%q on Sara's message got no answer", text)
			continue
		}
		if req := llmText(h.llm.last()); !strings.Contains(req, "به نظرم فردا بارون میاد") {
			t.Errorf("%q: the replied message reaches the model:\n%s", text, req)
		}
	}
}
