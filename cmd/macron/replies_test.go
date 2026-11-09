package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
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

func TestPrivateReplyWhileWorkingReachesRedirectedModel(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
			return map[string]any{"role": "assistant", "content": "old answer"}, 0
		}
		return map[string]any{"role": "assistant", "content": "The referenced plan is understood."}, 0
	}
	first := private(ownerID, "برنامه جدیدی پیشنهاد بده")
	wait := h.background(first)
	<-started
	m := followUpMsg(first, 78, "این یکی رو انجام بده")
	m.ReplyToMessage = &telegram.Message{MessageID: 12, Chat: m.Chat, From: &telegram.User{ID: botID, IsBot: true}, RichMessage: json.RawMessage(`{"blocks":[{"type":"paragraph","text":"Create weekly.csv with disk usage."}]}`)}
	m.Quote = &telegram.TextQuote{Text: "Create weekly.csv"}
	h.bot.handleMessage(m)
	wait()
	req := llmText(h.llm.last())
	for _, want := range []string{"Create weekly.csv with disk usage.", "your own earlier message", "Create weekly.csv", "این یکی رو انجام بده"} {
		if !strings.Contains(req, want) {
			t.Errorf("redirected model lost reply context %q", want)
		}
	}
	if h.llm.count() != 2 {
		t.Fatalf("reply should redirect one task, got %d model calls", h.llm.count())
	}
}

func TestQuoteWithoutOriginalMessageStillReachesModel(t *testing.T) {
	h := newHarness(t)
	m := private(ownerID, "این بخش رو توضیح بده")
	m.Quote = &telegram.TextQuote{Text: "asyncio.TaskGroup cancels sibling tasks on failure."}
	h.send(m)
	if !strings.Contains(llmText(h.llm.last()), m.Quote.Text) {
		t.Fatal("quote disappeared when Telegram omitted the original message")
	}
}

func TestReplyIsContextualEvenWhenItsTextIsSmallTalk(t *testing.T) {
	h := newHarness(t)
	m := private(ownerID, "ok")
	m.ReplyToMessage = &telegram.Message{MessageID: 12, Chat: m.Chat, From: &telegram.User{ID: botID, IsBot: true}, Text: "Shall I write the deployment script?"}
	in, err := h.bot.buildInput(bg, &store.User{ID: ownerID}, m, m.Text, false)
	if err != nil {
		t.Fatal(err)
	}
	if in.plain {
		t.Fatal("a reply must not be routed as context-free small talk")
	}
}

func TestSelectedQuoteBeyondReplyExcerptIsPreserved(t *testing.T) {
	h := newHarness(t)
	m := private(ownerID, "همین بخش رو اصلاح کن")
	selected := "Change only port 8088 in the production configuration."
	m.ReplyToMessage = &telegram.Message{MessageID: 12, Chat: m.Chat, From: &telegram.User{ID: botID, IsBot: true}, Text: strings.Repeat("earlier text ", 650) + selected}
	m.Quote = &telegram.TextQuote{Text: selected}
	in, err := h.bot.buildInput(bg, &store.User{ID: ownerID}, m, m.Text, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.about, selected) || !strings.Contains(in.hint, selected) || !strings.Contains(in.about, "primary reference") {
		t.Fatal("selected text at the end of a long answer was lost")
	}
	if len([]rune(in.about)) > 11000 {
		t.Fatal("reply context exceeded its bounded excerpt")
	}
}

func TestSteeringPreservesRepliedRichPhotoAndCaption(t *testing.T) {
	h := newHarness(t)
	h.tg.files["replied-photo"] = testJPEG(40, 40)
	first := private(ownerID, "original task")
	run := newRun(ownerID, first, func() {}, nil, tierOwner)
	h.bot.runs.Store(int64(ownerID), run)
	defer h.bot.runs.Delete(int64(ownerID))
	m := followUpMsg(first, 78, "این عکس رو بررسی کن")
	m.ReplyToMessage = &telegram.Message{MessageID: 12, Chat: m.Chat, From: &telegram.User{ID: botID, IsBot: true}, RichMessage: json.RawMessage(`{"blocks":[{"type":"photo","photo":[{"file_id":"replied-photo","width":40,"height":40}],"caption":{"text":"A deployment architecture diagram"}}]}`)}
	if !h.bot.interjectMode(&store.User{ID: ownerID}, m, m.Text, false) {
		t.Fatal("reply was not accepted")
	}
	xs := run.take()
	if len(xs) != 1 {
		t.Fatalf("wanted one correction, got %d", len(xs))
	}
	if !strings.Contains(xs[0].msg.Text(), "A deployment architecture diagram") || !strings.Contains(xs[0].memo, "replying to") {
		t.Fatal("steering lost rich media caption or memory reference")
	}
	if len(xs[0].msg.Parts) < 2 {
		t.Fatal("replied photo did not reach the model as an image")
	}
}

func TestQueuedReplyKeepsItsOriginalReference(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
		}
		return map[string]any{"role": "assistant", "content": "A newer unrelated answer."}, 0
	}
	first := private(ownerID, "اول این کار رو انجام بده")
	wait := h.background(first)
	<-started
	queued := followUpMsg(first, 78, "/queue این بخش رو توضیح بده")
	queued.ReplyToMessage = &telegram.Message{MessageID: 12, Chat: queued.Chat, From: &telegram.User{ID: botID, IsBot: true}, Text: "The old reference describes incremental backups."}
	h.bot.handleMessage(queued)
	before := h.llm.count()
	close(release)
	wait()
	if before != 1 || h.llm.count() != 2 {
		t.Fatalf("reply did not stay a separate queued turn: before=%d after=%d", before, h.llm.count())
	}
	if req := llmText(h.llm.last()); !strings.Contains(req, "The old reference describes incremental backups.") || !strings.Contains(req, "your own earlier message") {
		t.Fatal("queued turn lost the selected earlier answer")
	}
}
