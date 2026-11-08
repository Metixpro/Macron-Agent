package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func TestVoiceMessagesAreHeard(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	var lastAuth atomic.Value
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/audio/transcriptions" {
			http.NotFound(w, r)
			return
		}
		calls.Add(1)
		lastAuth.Store(r.Header.Get("Authorization"))
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "فردا هوای تهران چطوره؟"})
	}))
	defer stt.Close()

	// The harness's only provider has no speech to text: as if none were set up.
	h.tg.files["v0"] = []byte("OggS voice zero")
	m := private(31, "")
	m.MessageID, m.Voice = 600, &telegram.Voice{FileID: "v0", Duration: 2}
	h.send(m)
	if text := llmText(h.llm.last()); !strings.Contains(text, "speech to text is not set up") {
		t.Fatalf("without a service: %s", text)
	}
	if h.bot.app.Registry.CanTranscribe() {
		t.Error("a provider without speech to text is skipped")
	}

	// The admin sets a service: the message with the key is deleted, the key stored encrypted.
	h.click(ownerID, ownerID, 900, "a:stt")
	if !hasButton(buttons(lastOf(h, "editMessageText")), "a:stt:set") {
		t.Fatal("panel")
	}
	h.click(ownerID, ownerID, 900, "a:stt:set")
	h.tg.reset()
	set := private(ownerID, stt.URL+"/v1/audio/transcriptions\nsk-stt-secret\nwhisper-large-v3")
	set.MessageID = 4100
	h.send(set)
	h.wantText("ذخیره شد")
	h.wantText("✅ کار می‌کنه")
	if del := h.tg.of("deleteMessage"); len(del) == 0 || del[0].body["message_id"].(float64) != 4100 {
		t.Error("the message with the key is deleted")
	}
	var raw string
	_ = h.st.DB().QueryRow("SELECT v FROM settings WHERE k = ?", settingSTT).Scan(&raw)
	if raw == "" || strings.Contains(raw, "sk-stt-secret") {
		t.Errorf("stored encrypted: %q", raw)
	}
	if got := h.bot.app.Registry.STTServices(); len(got) != 1 || got[0].URL != stt.URL+"/v1" || got[0].Model != "whisper-large-v3" {
		t.Fatalf("services: %+v", got)
	}

	// A voice message is answered like a typed one.
	h.tg.files["v1"] = []byte("OggS voice one")
	calls.Store(0)
	m = private(31, "")
	m.MessageID, m.Voice = 601, &telegram.Voice{FileID: "v1", Duration: 3}
	h.send(m)
	text := llmText(h.llm.last())
	if !strings.Contains(text, "فردا هوای تهران چطوره؟") || !strings.Contains(text, "answer it as if they had typed it") || calls.Load() != 1 {
		t.Fatalf("heard (%d calls): %s", calls.Load(), text)
	}
	if lastAuth.Load() != "Bearer sk-stt-secret" {
		t.Errorf("auth: %v", lastAuth.Load())
	}
	if !h.bot.app.Registry.Has(tools.ToolTranscribe) {
		t.Error("transcribe_audio")
	}
	// Replying to it later reads the kept transcript.
	reply := private(31, "اینو خلاصه کن")
	reply.MessageID, reply.ReplyToMessage = 602, m
	h.send(reply)
	if calls.Load() != 1 || !strings.Contains(llmText(h.llm.last()), "فردا هوای تهران") {
		t.Errorf("the transcript is kept (%d calls)", calls.Load())
	}

	// Off: voice messages are not sent anywhere.
	h.click(ownerID, ownerID, 900, "a:stt:off")
	if h.bot.app.Registry.CanTranscribe() {
		t.Fatal("off")
	}
	h.tg.files["v2"] = []byte("OggS voice two")
	m = private(31, "")
	m.MessageID, m.Voice = 603, &telegram.Voice{FileID: "v2", Duration: 1}
	h.send(m)
	if calls.Load() != 1 {
		t.Error("nothing is transcribed while it is off")
	}
	h.click(ownerID, ownerID, 900, "a:stt:off")
	h.click(ownerID, ownerID, 900, "a:stt:del")
	if _, ok := panelSTT(h.st); ok {
		t.Error("delete")
	}
}

func TestParseSTT(t *testing.T) {
	if s, ok := parseSTT("https://api.groq.com/openai/v1/\n-\n"); !ok || s.URL != "https://api.groq.com/openai/v1" || s.Key != "" || s.Model != "" {
		t.Errorf("groq: %+v %v", s, ok)
	}
	if s, ok := parseSTT("۰https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3\nhf_x"); !ok || !strings.HasPrefix(s.URL, "https://router") || s.Key != "hf_x" {
		t.Errorf("a stray character before the link: %+v", s)
	}
	if _, ok := parseSTT("not a url\nkey"); ok {
		t.Error("a bad address")
	}
}

func TestTheLiveCardListens(t *testing.T) {
	h := newHarness(t)
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(900 * time.Millisecond) // a local whisper takes its time
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "سلام"})
	}))
	defer stt.Close()
	raw, _ := json.Marshal(sttSetting{URL: stt.URL + "/v1"})
	_ = h.st.SetSecretSetting(context.Background(), settingSTT, string(raw))
	h.tg.files["lv"] = []byte("OggS live voice")
	m := private(32, "")
	m.MessageID, m.Voice = 700, &telegram.Voice{FileID: "lv", Duration: 2}
	h.send(m)
	var seq []string
	h.tg.mu.Lock()
	for _, c := range h.tg.calls {
		if rm, ok := c.body["rich_message"].(map[string]any); ok {
			seq = append(seq, c.method+" "+fmt.Sprint(rm["html"], rm["markdown"]))
		}
	}
	h.tg.mu.Unlock()
	all := strings.Join(seq, "\n")
	listen := strings.Index(all, "sendRichMessageDraft <tg-thinking>🎙 دارم می‌شنوم…</tg-thinking>")
	if listen < 0 || strings.Index(all, "<tg-thinking>💭") > listen || strings.LastIndex(all, "sendRichMessage ") < listen {
		t.Fatalf("the card listens (plain HTML outside any code block), then the answer comes:\n%s", all)
	}
}

func TestTranscribeAudioShowsAboveTheTerminal(t *testing.T) {
	l := &liveView{terminal: true, lng: fa}
	l.toolStart(0, tools.ToolTranscribe)
	l.toolProgress(0, tools.ToolTranscribe, tools.StatusListening, false)
	md := l.tr.markdown(time.Now(), time.Now(), true)
	head, block, _ := strings.Cut(md, "```")
	if !strings.Contains(head, "🎧 دارم گوش می‌دم…") || strings.Contains(block, "listening") || strings.Contains(block, "گوش") {
		t.Fatalf("the listening line is outside the code block:\n%s", md)
	}
	l.toolEnd(0, tools.ToolTranscribe, "Transcript of x")
	if strings.Contains(l.tr.markdown(time.Now(), time.Now(), true), "گوش") {
		t.Error("it goes when the tool ends")
	}
}

// A reply to the bot's own voice (or round video) is not listened to again:
// no speech to text, no «listening» on the card. A reply to someone else's
// voice gives its words as what the replied message says, not as the request.
func TestRepliesToVoiceMessages(t *testing.T) {
	h := newHarness(t)
	var calls atomic.Int32
	stt := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "جلسه فردا ساعت ده است"})
	}))
	defer stt.Close()
	raw, _ := json.Marshal(sttSetting{URL: stt.URL + "/v1"})
	_ = h.st.SetSecretSetting(context.Background(), settingSTT, string(raw))

	h.tg.files["own"], h.tg.files["round"], h.tg.files["sara"] = []byte("OggS the bot's own"), []byte("mp4 round"), []byte("OggS sara")
	for _, own := range []*telegram.Message{
		{MessageID: 800, From: &telegram.User{ID: botID, IsBot: true}, Voice: &telegram.Voice{FileID: "own", Duration: 3}},
		{MessageID: 801, From: &telegram.User{ID: botID, IsBot: true}, VideoNote: &telegram.Video{FileID: "round", Duration: 4}},
	} {
		own.Chat = telegram.Chat{ID: 33, Type: "private"}
		m := private(33, "اینو به mp3 تبدیل کن")
		m.MessageID, m.ReplyToMessage = 900+own.MessageID, own
		h.tg.reset()
		h.send(m)
		if calls.Load() != 0 || strings.Contains(h.tg.allText(), "دارم می‌شنوم") {
			t.Fatalf("the bot's own %d is not listened to (%d calls):\n%s", own.MessageID, calls.Load(), h.tg.allText())
		}
		if text := llmText(h.llm.last()); !strings.Contains(text, "you sent yourself earlier") {
			t.Errorf("the model knows it is its own: %s", text)
		}
	}

	sara := &telegram.Message{MessageID: 802, From: &telegram.User{ID: 77, FirstName: "Sara"}, Chat: telegram.Chat{ID: 33, Type: "private"},
		Voice: &telegram.Voice{FileID: "sara", Duration: 2}}
	m := private(33, "خلاصه‌اش چیه؟")
	m.MessageID, m.ReplyToMessage = 1802, sara
	h.send(m)
	text := llmText(h.llm.last())
	if calls.Load() != 1 || !strings.Contains(text, "جلسه فردا ساعت ده است") || !strings.Contains(text, "content of the replied message") ||
		strings.Contains(text, "answer it as if they had typed it") {
		t.Fatalf("someone else's voice is what the replied message says (%d calls): %s", calls.Load(), text)
	}
}
