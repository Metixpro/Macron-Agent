package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

var bg = context.Background()

func TestConversationFlow(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = say("سلام علی جان! **خوبم** مرسی 😊")
	h.send(private(7, "سلام خوبی؟"))

	rich := h.tg.of("sendRichMessage")
	if len(rich) != 1 {
		t.Fatalf("expected one rich answer, got %d; calls: %v", len(rich), h.tg.allText())
	}
	body := rich[0].body
	rm := body["rich_message"].(map[string]any)
	if rm["markdown"] != "سلام علی جان! **خوبم** مرسی 😊" || rm["is_rtl"] != true {
		t.Errorf("rich message: %v", rm)
	}
	if rp := body["reply_parameters"].(map[string]any); rp["message_id"].(float64) != 77 {
		t.Errorf("the answer must reply to the user's message: %v", rp)
	}
	if body["reply_markup"] != nil {
		t.Error("no buttons under the assistant's answers (the mode is chosen with /mode)")
	}
	// Alive: a "seen" reaction, the live terminal draft, typing.
	reacts := h.tg.of("setMessageReaction")
	if len(reacts) == 0 || !strings.Contains(fmt.Sprint(reacts[0].body["reaction"]), "👀") {
		t.Errorf("expected a 👀 reaction first: %v", reacts)
	}
	last := fmt.Sprint(reacts[len(reacts)-1].body["reaction"])
	if !strings.Contains(last, "🤗") {
		t.Errorf("a greeting should end with 🤗, got %s", last)
	}
	// No tool ran: a thinking bubble (with a stop button), no terminal.
	drafts := h.tg.of("sendRichMessageDraft")
	if len(drafts) == 0 || !strings.Contains(fmt.Sprint(drafts[0].body["rich_message"]), "tg-thinking") ||
		strings.Contains(fmt.Sprint(drafts), traceTitle) || drafts[0].body["can_stop"] != true {
		t.Errorf("expected a thinking draft with a stop button: %v", drafts)
	}
	if len(h.tg.of("sendChatAction")) == 0 {
		t.Error("expected a typing action")
	}

	// What the model saw: persona, profile with bio, the user's text.
	req := llmText(h.llm.last())
	for _, want := range []string{"مکرون", "@Cianyd", "username: @ali_dev", "bio: عاشق Go و قهوه", "سلام خوبی؟"} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q", want)
		}
	}
	// Memory and quota.
	mem, _ := h.bot.mem.Load(bg, 7)
	if mem.Messages != 2 || !strings.Contains(mem.History[1].Content, "خوبم") {
		t.Errorf("memory: %+v", mem)
	}
	if n, _ := h.st.UsedToday(bg, 7); n != 1 {
		t.Errorf("quota used = %d", n)
	}

	// The second message carries the history.
	h.send(private(7, "اسمم چی بود؟"))
	if req := llmText(h.llm.last()); !strings.Contains(req, "سلام خوبی؟") || !strings.Contains(req, "خوبم") {
		t.Error("history was not sent")
	}
}

func TestLongAnswerIsSplitIntoSeveralMessages(t *testing.T) {
	h := newHarness(t)
	var sb strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&sb, "## بخش %d\nاین یک پاراگراف طولانی فارسی است که برای آزمایش تقسیم پیام نوشته شده است و چند جمله دارد.\n\n", i)
	}
	h.llm.reply = say(sb.String())
	h.send(private(8, "یه گزارش بلند بده"))
	rich := h.tg.of("sendRichMessage")
	if len(rich) < 2 {
		t.Fatalf("a long answer must arrive as several messages, got %d", len(rich))
	}
	for i, c := range rich {
		md := c.body["rich_message"].(map[string]any)["markdown"].(string)
		if n := len([]rune(md)); n > partChars {
			t.Errorf("part %d has %d chars", i, n)
		}
		_, replied := c.body["reply_parameters"]
		if replied != (i == 0) {
			t.Errorf("only the first part replies to the user (part %d)", i)
		}
	}
	all := ""
	for _, c := range rich {
		all += c.body["rich_message"].(map[string]any)["markdown"].(string)
	}
	if !strings.Contains(all, "بخش 0") || !strings.Contains(all, "بخش 59") {
		t.Error("content lost while splitting")
	}
}

func TestRichFallbackToClassicHTML(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendRichMessage"] = 400
	h.tg.desc["sendRichMessage"] = "Bad Request: can't parse rich message"
	h.llm.reply = say("**پررنگ** و x < 5")
	h.send(private(9, "test"))
	msgs := h.tg.of("sendMessage")
	if len(msgs) != 1 || msgs[0].body["parse_mode"] != "HTML" || !strings.Contains(msgs[0].body["text"].(string), "<b>پررنگ</b> و x &lt; 5") {
		t.Fatalf("classic fallback: %v", msgs)
	}
	if h.bot.richOff.Load() {
		t.Error("a content error must not disable rich messages for everyone")
	}
	h.tg.desc["sendRichMessage"] = "Not Found: method not found"
	h.tg.fail["sendRichMessage"] = 404
	h.send(private(9, "again"))
	if !h.bot.richOff.Load() {
		t.Error("an unsupported Bot API server should switch to classic messages")
	}
}

func TestDailyFiguresAndRefund(t *testing.T) {
	h := newHarness(t)
	// The fake model says each call used 100 + 20 tokens. Nothing limits a request; the day's figures count.
	for i := 1; i <= 3; i++ {
		h.send(private(10, "پیام"))
		if n, _ := h.st.TokensToday(bg, 10); n != 120*i {
			t.Fatalf("a request adds what it used: %d after %d", n, i)
		}
	}
	if n, _ := h.st.UsedToday(bg, 10); n != 3 {
		t.Errorf("requests counted: %d", n)
	}

	// A failure on our side gives the request back.
	h.llm.reply = func(map[string]any, int) (map[string]any, int) { return nil, 503 }
	h.st.ResetUsageToday(bg, 11)
	h.send(private(11, "x"))
	if n, _ := h.st.UsedToday(bg, 11); n != 0 {
		t.Errorf("a failed request must be refunded, used=%d", n)
	}
	if n, _ := h.st.TokensToday(bg, 11); n != 0 {
		t.Errorf("a failed request costs no tokens: %d", n)
	}
	h.wantText("به مغزم دسترسی ندارم")
	// The user never sees the provider's own words (the owners' alert may: it is for them).
	for _, method := range []string{"sendMessage", "editMessageText"} {
		for _, c := range h.tg.of(method) {
			if id, _ := c.body["chat_id"].(float64); int64(id) == 11 && strings.Contains(fmt.Sprint(c.body["text"]), "upstream exploded") {
				t.Fatalf("the user was shown the provider's error: %v", c.body["text"])
			}
		}
	}
}

func TestPhotoReplyAndProfileReachTheModel(t *testing.T) {
	h := newHarness(t)
	h.tg.files["ph1"] = testJPEG(800, 600)
	h.tg.files["old"] = testJPEG(300, 300)
	h.llm.reply = say("چه عکس قشنگی!")
	m := private(12, "")
	m.Caption = "این چیه؟"
	m.Photo = []telegram.PhotoSize{{FileID: "ph1", Width: 800, Height: 600}}
	m.ReplyToMessage = &telegram.Message{MessageID: 5, From: &telegram.User{ID: 42, FirstName: "Waze"}, Text: "قبلاً گفتم کیک شکلاتی بپز",
		Photo: []telegram.PhotoSize{{FileID: "old", Width: 300, Height: 300}}}
	h.send(m)

	req := llmText(h.llm.last())
	if strings.Count(req, "data:image/jpeg;base64") != 2 {
		t.Errorf("the new photo and the replied photo must both be visible to the model:\n%s", req)
	}
	for _, want := range []string{"این چیه؟", "replying to a message from you (your own earlier message)", "قبلاً گفتم کیک شکلاتی بپز", "photo_77.jpg"} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q", want)
		}
	}
	for _, name := range []string{"photo_77.jpg", "photo_5.jpg"} {
		if _, err := h.bot.app.Machine.Stat(sessionFor(12), name); err != nil {
			t.Errorf("%s (the photo, and the photo it replies to) must be saved in the workspace", name)
		}
	}
	mem, _ := h.bot.mem.Load(bg, 12)
	if !strings.Contains(mem.History[0].Content, "[the user sent a photo: photo_77.jpg]") || !strings.Contains(mem.History[0].Content, "[replying to:") {
		t.Errorf("memory of the turn: %q", mem.History[0].Content)
	}
}

func TestModelChoosesReactionAndSecretsAreRedacted(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolReact, `{"emoji":"🔥"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "کلید من sk-provider-secret-1234 و توکن 42:TOKEN-SECRET"}, 0
	}
	h.send(private(13, "پروژه‌م لانچ شد!"))
	reacts := h.tg.of("setMessageReaction")
	last := reacts[len(reacts)-1].body
	if !strings.Contains(fmt.Sprint(last["reaction"]), "🔥") || last["is_big"] != true {
		t.Errorf("the model's reaction must be applied (big for 🔥): %v", last)
	}
	h.noText("sk-provider-secret-1234")
	h.noText("TOKEN-SECRET")
	h.wantText("•••")
}

func TestStopButtonCancels(t *testing.T) {
	h := newHarness(t)
	started := make(chan struct{})
	release := make(chan struct{})
	h.llm.reply = func(map[string]any, int) (map[string]any, int) {
		close(started)
		<-release
		return map[string]any{"role": "assistant", "content": "late"}, 0
	}
	done := make(chan struct{})
	go func() { h.send(private(14, "یه کار طولانی")); close(done) }()
	<-started
	var draftID int64
	h.bot.drafts.Range(func(k, v any) bool { draftID = k.(int64); return false })
	h.bot.handleStop(&telegram.MessageGenerationStopped{Chat: telegram.Chat{ID: 14, Type: "private"}, DraftID: telegram.FlexInt64(draftID)})
	close(release)
	<-done
	h.wantText(textCancelled)
	h.noText("late")
	// A stopped job counts, and costs what the model used for it (nothing
	// here: the call was cut off).
	if n, _ := h.st.UsedToday(bg, 14); n != 1 {
		t.Errorf("a stopped request counts, used=%d", n)
	}
	if n, _ := h.st.TokensToday(bg, 14); n > 120 {
		t.Errorf("tokens: %d", n)
	}
}

func TestGroupsBansAndCommands(t *testing.T) {
	h := newHarness(t)
	group := func(text string) *telegram.Message {
		m := private(15, text)
		m.Chat = telegram.Chat{ID: -100500, Type: "supergroup", Title: "گروه"}
		return m
	}
	h.send(group("سلام بچه‌ها"))
	if h.llm.count() != 0 {
		t.Fatal("unaddressed group messages must be ignored")
	}
	h.send(group("@WazeBot قیمت دلار؟"))
	if h.llm.count() != 1 || !strings.Contains(llmText(h.llm.last()), "group «گروه»") || strings.Contains(llmText(h.llm.last()), "@WazeBot") {
		t.Fatal("a mention must be answered, without the mention text")
	}
	if len(h.tg.of("sendRichMessageDraft")) != 0 {
		t.Error("drafts are for private chats only")
	}

	h.tg.reset()
	h.send(private(16, "/start"))
	h.wantText("من <b>مکرون</b> هستم")
	h.wantText("/new")
	h.send(private(16, "/me"))
	h.wantText("توکن امروز")
	h.send(private(16, "/admin"))
	h.wantText(textUnknownCommand)
	h.send(private(16, "/start@OtherBot"))

	// /new clears the memory.
	h.send(private(17, "یه چیزی"))
	h.send(private(17, "/new"))
	if mem, _ := h.bot.mem.Load(bg, 17); mem.Messages != 0 {
		t.Error("/new must clear the memory")
	}
	h.wantText(textMemoryReset)

	// Blocking the bot is recorded.
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: telegram.Chat{ID: 17, Type: "private"}, From: telegram.User{ID: 17},
		NewChatMember: telegram.ChatMember{Status: "kicked"}})
	if u, _ := h.st.User(bg, 17); !u.BlockedBot {
		t.Error("blocked flag")
	}
}

func TestBusyUserGetsANotice(t *testing.T) {
	h := newHarness(t)
	release, _ := h.bot.gate.TryAcquire("21")
	h.send(private(21, "x"))
	release()
	h.wantText(textBusy)
	if n, _ := h.st.UsedToday(bg, 21); n != 0 {
		t.Error("a refused request must not count")
	}
}

// A greeting goes without the tools; the same words with a photo, or a task,
// get them all.
func TestAGreetingIsLight(t *testing.T) {
	h := newHarness(t)
	h.send(private(61, "سلام خوبی؟"))
	if names := toolNames(h.llm.last()); names != ",use_tools," {
		t.Fatalf("a greeting's tools: %s", names)
	}
	h.send(private(61, "سلام، قیمت دلار امروز چنده؟"))
	if names := toolNames(h.llm.last()); !strings.Contains(names, ",web_search,") {
		t.Fatalf("a question gets the tools: %s", names)
	}
	h.tg.files["ph"] = []byte("\x89PNG\r\n\x1a\n")
	m := private(61, "")
	m.Caption, m.Photo = "سلام", []telegram.PhotoSize{{FileID: "ph", Width: 10, Height: 10}}
	h.send(m)
	if names := toolNames(h.llm.last()); !strings.Contains(names, ",view_image,") && !strings.Contains(names, ",web_search,") {
		t.Fatalf("a photo gets the tools: %s", names)
	}
}

// The note that a long conversation was summarized replies to the message
// it came after, so in a busy group it is clear whose conversation it was.
func TestMemoryNoteRepliesToTheMessage(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetSetting(ctx, store.SettingHistoryTokens, "2000")
	var turns []store.ContextMessage
	for i := 0; i < 30; i++ {
		text := strings.Repeat("یه پیام نسبتاً بلند برای پر کردن حافظه. ", 20)
		turns = append(turns, store.ContextMessage{Role: "user", Content: text, Tokens: 300},
			store.ContextMessage{Role: "assistant", Content: text, Tokens: 300})
	}
	if err := h.st.AppendContext(ctx, 18, turns...); err != nil {
		t.Fatal(err)
	}
	h.tg.reset()
	m := private(18, "ادامه بده")
	h.send(m)
	var note map[string]any
	for _, c := range h.tg.calls {
		if strings.Contains(fmt.Sprint(c.body["text"]), "خلاصه‌شون رو نگه داشتم") {
			note = c.body
		}
	}
	if note == nil {
		t.Fatalf("no memory note: %s", h.tg.allText())
	}
	rp, ok := note["reply_parameters"].(map[string]any)
	if !ok || rp["message_id"].(float64) != float64(m.MessageID) || note["disable_notification"] != true {
		t.Errorf("the note replies quietly to the user's message: %v", note)
	}
}

// In a group, a command for another bot (/warn) is not this bot's business,
// and a channel's post copied into its discussion group is never answered.
func TestGroupIgnoresOtherBotsAndChannelPosts(t *testing.T) {
	h := newHarness(t)
	group := func(text string) *telegram.Message {
		m := private(15, text)
		m.Chat = telegram.Chat{ID: -100500, Type: "supergroup", Title: "گروه"}
		return m
	}
	h.send(group("/warn @spammer"))
	if strings.Contains(h.tg.allText(), textUnknownCommand) || h.llm.count() != 0 {
		t.Error("another bot's command is ignored in a group")
	}
	h.send(group("/warn@WazeBot"))
	if !strings.Contains(h.tg.allText(), textUnknownCommand) {
		t.Error("a command named for this bot is answered")
	}
	h.tg.reset()
	h.send(group("/help"))
	if h.tg.allText() == "" {
		t.Error("this bot's own commands work in groups")
	}

	post := group("مکرون این خبر رو ببین")
	post.From = &telegram.User{ID: telegram.TelegramServiceID, FirstName: "Telegram"}
	post.SenderChat = &telegram.Chat{ID: -100777, Type: "channel", Title: "کانال"}
	post.IsAutomaticForward = true
	before := h.llm.count()
	h.send(post)
	if h.llm.count() != before {
		t.Error("a channel's post is not answered")
	}
	if u, err := h.st.User(bg, telegram.TelegramServiceID); err == nil && u.ID != 0 {
		t.Error("Telegram's service account does not become a user")
	}
}
