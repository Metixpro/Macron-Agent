package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/sched"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func newUser(h *harness, id int64) *store.User {
	u, _, err := h.st.TouchUser(bg, store.UserInfo{ID: id, FirstName: "Mahdi", Username: "mahdi"})
	if err != nil {
		h.t.Fatal(err)
	}
	return u
}

func TestCreateScheduleValidates(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 60)
	m := private(60, "")
	now, _ := sched.ParseLocal("2026-10-19 09:00", h.bot.loc)
	try := func(args map[string]any) string { return h.bot.createSchedule(bg, u, m, args, now) }

	out := try(map[string]any{"kind": "reminder", "text": "به مهدی زنگ بزن", "at": "2026-10-20 11:00"})
	if !strings.HasPrefix(out, "Scheduled #") || !strings.Contains(out, "Tuesday 2026-10-20 11:00") || !strings.Contains(out, "یک‌بار") {
		t.Errorf("tomorrow 11:00: %s", out)
	}
	if out := try(map[string]any{"kind": "reminder", "text": "x", "at": "2026-10-18 11:00"}); !strings.Contains(out, "already passed") {
		t.Errorf("a past time is refused with the current time: %s", out)
	}
	out = try(map[string]any{"kind": "task", "text": "قیمت تتر و طلا رو بگیر و خلاصه بفرست", "at": "2026-10-19 10:00", "repeat": "daily", "title": "قیمت‌ها"})
	if !strings.Contains(out, "Monday 2026-10-19 10:00") || !strings.Contains(out, "هر روز ساعت ۱۰:۰۰") {
		t.Errorf("daily task: %s", out)
	}
	out = try(map[string]any{"kind": "reminder", "text": "ورزش", "at": "2026-10-19 07:00", "repeat": "weekly", "days": []any{"sat", "wed"}})
	if !strings.Contains(out, "Wednesday 2026-10-21 07:00") {
		t.Errorf("weekly: the next matching weekday: %s", out)
	}
	if out := try(map[string]any{"kind": "task", "text": "x", "repeat": "interval", "every_minutes": 30}); !strings.Contains(out, "at least 60") {
		t.Errorf("tasks repeat at most hourly: %s", out)
	}
	if out := try(map[string]any{"kind": "reminder", "text": "آب بخور", "in_minutes": 20}); !strings.Contains(out, "2026-10-19 09:20") {
		t.Errorf("in 20 minutes: %s", out)
	}
	if out := try(map[string]any{"kind": "alarm", "text": "x"}); !strings.HasPrefix(out, "error:") {
		t.Errorf("bad kind: %s", out)
	}
}

func TestReminderFiresOnceAtItsTime(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 61)
	m := private(61, "")
	m.Chat = telegram.Chat{ID: -100600, Type: "supergroup", Title: "خانواده"}
	now := time.Now()
	out := h.bot.createSchedule(bg, u, m, map[string]any{"kind": "reminder", "text": "به <مهدی> زنگ بزن", "in_minutes": 1}, now)
	if !strings.HasPrefix(out, "Scheduled #") {
		t.Fatal(out)
	}
	h.bot.runDueSchedules(bg, now)
	h.bot.wg.Wait()
	if len(h.tg.of("sendMessage")) != 0 {
		t.Fatal("too early")
	}
	later := now.Add(2 * time.Minute)
	h.bot.runDueSchedules(bg, later)
	h.bot.runDueSchedules(bg, later)
	h.bot.wg.Wait()
	sent := h.tg.of("sendMessage")
	if len(sent) != 1 || int64(sent[0].body["chat_id"].(float64)) != -100600 {
		t.Fatalf("one reminder in the group: %v", sent)
	}
	text := sent[0].body["text"].(string)
	for _, want := range []string{"⏰", `tg://user?id=61`, "یادآوری", "به &lt;مهدی&gt; زنگ بزن"} {
		if !strings.Contains(text, want) {
			t.Errorf("reminder misses %q: %s", want, text)
		}
	}
	if list, _ := h.st.UserSchedules(bg, 61); len(list) != 0 {
		t.Error("a one-time reminder is done after it fires")
	}
}

func TestScheduledTaskPostsTheResult(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 62)
	m := private(62, "")
	h.llm.reply = say("تتر: ۷۰٬۰۰۰ تومان · طلا: ۵٬۰۰۰٬۰۰۰ تومان")
	now := time.Now()
	out := h.bot.createSchedule(bg, u, m, map[string]any{"kind": "task", "text": "قیمت تتر و طلا رو بگیر", "title": "قیمت‌ها",
		"at": now.In(h.bot.loc).Add(time.Minute).Format("2006-01-02 15:04"), "repeat": "daily"}, now)
	if !strings.HasPrefix(out, "Scheduled #") {
		t.Fatal(out)
	}
	h.bot.runDueSchedules(bg, now.Add(2*time.Minute))
	h.bot.wg.Wait()
	h.wantText("⏰ **قیمت‌ها**")
	h.wantText("تتر: ۷۰٬۰۰۰ تومان")
	req := llmText(h.llm.last())
	if !strings.Contains(req, "scheduled task #") || !strings.Contains(req, "قیمت تتر و طلا رو بگیر") || strings.Contains(req, `"name":"ask_user"`) {
		t.Errorf("the task runs unattended:\n%s", req)
	}
	if mem, _ := h.bot.mem.Load(bg, 62); len(mem.History) != 0 {
		t.Error("scheduled runs stay out of the conversation memory")
	}
	if n, _ := h.st.UsedToday(bg, 62); n != 1 {
		t.Errorf("a scheduled run counts against the daily quota: %d", n)
	}
	list, _ := h.st.UserSchedules(bg, 62)
	if len(list) != 1 || !list[0].NextRun.After(now.Add(23*time.Hour)) {
		t.Errorf("a daily task moves to tomorrow: %+v", list)
	}
}

func TestModelSchedulesAndUserCancels(t *testing.T) {
	h := newHarness(t)
	tomorrow := time.Now().In(h.bot.loc).AddDate(0, 0, 1).Format("2006-01-02")
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolSchedule, fmt.Sprintf(`{"kind":"reminder","text":"به مهدی زنگ بزن","at":"%s 11:00","title":"زنگ به مهدی"}`, tomorrow)), 0
		}
		return map[string]any{"role": "assistant", "content": "باشه، فردا ساعت ۱۱ یادت می‌ندازم ⏰"}, 0
	}
	h.send(private(63, "مکرون فردا ساعت ۱۱ صبح بهم یادآوری کن به مهدی زنگ بزنم"))
	h.wantText("فردا ساعت ۱۱ یادت می‌ندازم")
	list, _ := h.st.UserSchedules(bg, 63)
	if len(list) != 1 || list[0].Title != "زنگ به مهدی" || list[0].NextRun.In(h.bot.loc).Format("2006-01-02 15:04") != tomorrow+" 11:00" {
		t.Fatalf("the reminder is stored: %+v", list)
	}
	if !strings.Contains(llmText(h.llm.last()), "Scheduled #") {
		t.Error("the model sees the confirmation")
	}

	h.tg.reset()
	h.send(private(63, "/reminders"))
	h.wantText("زنگ به مهدی")
	if !strings.Contains(fmt.Sprint(h.tg.of("sendMessage")[0].body["reply_markup"]), fmt.Sprintf("sc:x:%d", list[0].ID)) {
		t.Error("each schedule has a cancel button")
	}
	h.click(64, 63, 900, fmt.Sprintf("sc:x:%d", list[0].ID))
	if l, _ := h.st.UserSchedules(bg, 63); len(l) != 1 {
		t.Fatal("only the owner can cancel")
	}
	h.click(63, 63, 900, fmt.Sprintf("sc:x:%d", list[0].ID))
	if l, _ := h.st.UserSchedules(bg, 63); len(l) != 0 {
		t.Error("the owner cancels with the button")
	}
}

func guestMsg(userID int64, text string) *telegram.Message {
	m := private(userID, text)
	m.Chat = telegram.Chat{ID: -100999, Type: "supergroup", Title: "گروهی که ربات توش نیست"}
	m.GuestQueryID = fmt.Sprintf("gq-%d", userID)
	return m
}

func TestStripMention(t *testing.T) {
	for in, want := range map[string]string{
		"@WazeBot 25 * 40":            "25 * 40",
		"@wazebot: py print('hello')": "py print('hello')",
		"این چیه @WazeBot":            "این چیه",
		"@WazeBot":                    "",
		"@WazeBotX hi":                "@WazeBotX hi",
	} {
		if got := stripMention(in, "WazeBot"); got != want {
			t.Errorf("stripMention(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGuestQueryGetsOneInPlaceAnswer(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = say("**۱۰۰۰** 🙂")
	h.bot.dispatch(telegram.Update{UpdateID: 1, GuestMessage: guestMsg(70, "@WazeBot 25 * 40")})
	h.bot.wg.Wait()

	req := llmText(h.llm.last())
	if !strings.Contains(req, `"content":"25 * 40"`) || !strings.Contains(req, "guest mode") || strings.Contains(req, "@WazeBot") {
		t.Errorf("the model gets the request without the mention, knowing it is a guest:\n%s", req)
	}
	answers := h.tg.of("answerGuestQuery")
	if len(answers) != 1 || answers[0].body["guest_query_id"] != "gq-70" {
		t.Fatalf("exactly one guest answer: %v", answers)
	}

	res := answers[0].body["result"].(map[string]any)
	content := res["input_message_content"].(map[string]any)
	if res["type"] != "article" || res["id"] == "" || content["rich_message"].(map[string]any)["markdown"] != thinkingStatus(fa) {
		t.Errorf("a rich «thinking» placeholder: %v", res)
	}

	edits := h.tg.of("editMessageText")
	if len(edits) == 0 {
		t.Fatal("the placeholder is edited with the answer")
	}
	last := edits[len(edits)-1].body
	if last["inline_message_id"] != "im-gq-70" || last["chat_id"] != nil || last["rich_message"].(map[string]any)["markdown"] != "**۱۰۰۰** 🙂" {
		t.Errorf("the answer replaces the placeholder: %v", last)
	}
	for _, m := range []string{"sendMessage", "sendRichMessage", "sendRichMessageDraft", "sendChatAction", "setMessageReaction"} {
		if n := len(h.tg.of(m)); n != 0 {
			t.Errorf("%d × %s: the bot is not in this chat", n, m)
		}
	}
	if n, _ := h.st.UsedToday(bg, 70); n != 1 {
		t.Errorf("a guest query counts as a message: %d", n)
	}
}

func TestGuestRepliesSeeTheRepliedMessage(t *testing.T) {
	h := newHarness(t)
	h.tg.files["ph"] = testJPEG(50, 50)
	m := guestMsg(71, "@WazeBot این رو ترجمه کن")
	m.ReplyToMessage = &telegram.Message{MessageID: 5, From: &telegram.User{ID: 99, FirstName: "Sara"}, Text: "Good morning everyone",
		Chat: m.Chat, Photo: []telegram.PhotoSize{{FileID: "ph", Width: 50, Height: 50}}}
	h.bot.handleGuest(m)
	req := llmText(h.llm.last())
	if !strings.Contains(req, "replying to a message from Sara") || !strings.Contains(req, "Good morning everyone") || !strings.Contains(req, "data:image/jpeg") {
		t.Errorf("the replied message (text and photo) reaches the model:\n%s", req)
	}

	m2 := guestMsg(72, "چرا؟")
	m2.ReplyToMessage = &telegram.Message{MessageID: 6, From: &telegram.User{ID: 72}, ViaBot: &telegram.User{ID: 42}, Text: "۱۰۰۰", Chat: m2.Chat}
	h.bot.handleGuest(m2)
	if req := llmText(h.llm.last()); !strings.Contains(req, "you (your own earlier message)") {
		t.Errorf("a reply to the bot's guest answer is recognised:\n%s", req)
	}
}

func TestGuestFallbacksAndLimits(t *testing.T) {
	h := newHarness(t)
	h.bot.richOff.Store(true)
	h.llm.reply = say("**جواب** " + strings.Repeat("خیلی طولانی ", 900))
	h.bot.handleGuest(guestMsg(73, "@WazeBot یه متن بلند بنویس"))
	answers := h.tg.of("answerGuestQuery")
	if len(answers) != 1 {
		t.Fatalf("one answer: %d", len(answers))
	}
	content := answers[0].body["result"].(map[string]any)["input_message_content"].(map[string]any)
	if content["parse_mode"] != "HTML" || content["message_text"] != thinkingStatus(fa) {
		t.Errorf("an HTML «thinking» placeholder: %v", content)
	}
	edits := h.tg.of("editMessageText")
	if len(edits) == 0 {
		t.Fatal("the placeholder is edited with the answer")
	}
	last := edits[len(edits)-1].body
	text, _ := last["text"].(string)
	if last["inline_message_id"] != "im-gq-73" || last["parse_mode"] != "HTML" || last["rich_message"] != nil || len([]rune(text)) > telegram.MaxGuestTextChars || !strings.HasPrefix(text, "<b>جواب</b>") || !strings.Contains(text, "پیوی @WazeBot") {
		t.Errorf("an HTML answer that fits one message and points to the private chat: %d chars, %.80q…", len([]rune(text)), text)
	}

	h.bot.guestOnce("gq-74")
	if h.bot.guestOnce("gq-74") {
		t.Error("a guest query cannot be answered twice")
	}
}

func TestGuestPlaceholderShowsTheWork(t *testing.T) {
	h := newHarness(t)
	old := guestEditEvery
	guestEditEvery = 0
	t.Cleanup(func() { guestEditEvery = old })
	m := guestMsg(75, "@WazeBot قیمت دلار؟")
	g := h.bot.guestThinking(bg, m.GuestQueryID, fa)
	if g == nil || g.inlineID != "im-gq-75" || !g.rich {
		t.Fatalf("a rich placeholder with its inline id: %+v", g)
	}
	if h.bot.guestThinking(bg, m.GuestQueryID, fa) != nil || len(h.tg.of("answerGuestQuery")) != 1 {
		t.Error("one placeholder per query")
	}
	live := h.bot.newLive(m, 75)
	if live.muted || live.guest != g {
		t.Fatal("the live view of a guest query edits its placeholder")
	}
	live.toolStart(0, "web_search")
	live.flush(bg)
	edits := h.tg.of("editMessageText")
	if len(edits) != 1 || edits[0].body["inline_message_id"] != "im-gq-75" || !strings.Contains(fmt.Sprint(edits[0].body["rich_message"]), "دارم توی اینترنت می‌گردم") {
		t.Fatalf("the status line replaces «thinking»: %v", edits)
	}
	live.flush(bg)
	if n := len(h.tg.of("editMessageText")); n != 1 {
		t.Errorf("an unchanged status is not sent again: %d edits", n)
	}
	h.bot.answerGuestMarkdown(bg, m.GuestQueryID, "**۶۰ هزار تومن**", fa)
	live.toolStart(0, "web_fetch")
	live.flush(bg)
	h.bot.answerGuestHTML(bg, m.GuestQueryID, "late notice", nil)
	edits = h.tg.of("editMessageText")
	if len(edits) != 2 || edits[1].body["rich_message"].(map[string]any)["markdown"] != "**۶۰ هزار تومن**" {
		t.Errorf("the answer is the last word: %v", edits)
	}
	for _, m := range []string{"sendChatAction", "sendMessage", "sendRichMessageDraft"} {
		if n := len(h.tg.of(m)); n != 0 {
			t.Errorf("%d × %s: the bot is not in this chat", n, m)
		}
	}

	h.llm.reply = say("")
	h.tg.reset()
	h.bot.handleGuest(guestMsg(76, "@WazeBot ؟"))
	edits = h.tg.of("editMessageText")
	if len(edits) == 0 || strings.Contains(fmt.Sprint(edits[len(edits)-1].body), thinkingStatus(fa)) {
		t.Errorf("an empty run ends the placeholder with a short note: %v", edits)
	}
	g = &guestReply{b: h.bot, queryID: "gq-x", inlineID: "im-x", rich: true}
	h.bot.guestReplies.Store("gq-x", g)
	h.tg.reset()
	h.bot.finishGuest(g, fa)
	if edits = h.tg.of("editMessageText"); len(edits) != 1 || !strings.Contains(fmt.Sprint(edits[0].body), "جوابی آماده نشد") {
		t.Errorf("a placeholder left alone gets a short note: %v", edits)
	}
	h.bot.finishGuest(g, fa)
	if n := len(h.tg.of("editMessageText")); n != 1 {
		t.Errorf("a final placeholder stays: %d edits", n)
	}

	h.bot.guestStubOff.Store(true)
	h.llm.reply = say("۴")
	h.tg.reset()
	h.bot.handleGuest(guestMsg(77, "@WazeBot ۲+۲"))
	answers := h.tg.of("answerGuestQuery")
	if len(answers) != 1 || !strings.Contains(fmt.Sprint(answers[0].body["result"]), "markdown:۴") || len(h.tg.of("editMessageText")) != 0 {
		t.Errorf("without a placeholder the answer is the one reply: %v", answers)
	}
}

func TestGuestTelegramShapes(t *testing.T) {
	r := telegram.GuestHTML("<b>x</b>", nil)
	c := r["input_message_content"].(map[string]any)
	if r["type"] != "article" || r["title"] == "" || c["message_text"] != "<b>x</b>" || c["parse_mode"] != "HTML" {
		t.Errorf("html article: %v", r)
	}
}

func TestSchedulesStopWhereTheBotCannotPost(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 65)
	m := private(65, "")
	m.Chat = telegram.Chat{ID: -100700, Type: "supergroup", Title: "رفته"}
	now := time.Now()
	h.bot.createSchedule(bg, u, m, map[string]any{"kind": "task", "text": "اخبار", "in_minutes": 1}, now)
	_ = h.st.SaveChatLeave(bg, -100700, "kicked", "رفته")
	h.bot.runDueSchedules(bg, now.Add(2*time.Minute))
	h.bot.wg.Wait()
	if h.llm.count() != 0 {
		t.Error("no model call for a group the bot was removed from")
	}
	if list, _ := h.st.UserSchedules(bg, 65); len(list) != 0 {
		t.Errorf("the schedule is cancelled: %+v", list)
	}
}

func TestScheduleInATopic(t *testing.T) {
	h := newHarness(t)
	u := newUser(h, 66)
	m := private(66, "")
	m.Chat = telegram.Chat{ID: -100610, Type: "supergroup", Title: "فروم"}
	m.IsTopicMessage, m.MessageThreadID = true, 77
	now := time.Now()
	if out := h.bot.createSchedule(bg, u, m, map[string]any{"kind": "reminder", "text": "جلسه", "in_minutes": 1}, now); !strings.HasPrefix(out, "Scheduled #") {
		t.Fatal(out)
	}
	h.bot.runDueSchedules(bg, now.Add(2*time.Minute))
	h.bot.wg.Wait()
	sent := h.tg.of("sendMessage")
	if len(sent) != 1 || sent[0].body["message_thread_id"] != float64(77) {
		t.Fatalf("the reminder goes to topic 77: %v", sent)
	}
}

func TestFailingSchedulesRetryAndPause(t *testing.T) {
	h := newHarness(t)
	old := onceRetryAfter
	onceRetryAfter = func(int) time.Duration { return time.Minute }
	t.Cleanup(func() { onceRetryAfter = old })
	u := newUser(h, 67)
	m := private(67, "")
	now := time.Now()
	h.bot.createSchedule(bg, u, m, map[string]any{"kind": "reminder", "text": "قرص", "in_minutes": 1}, now)
	h.tg.mu.Lock()
	h.tg.fail["sendMessage"] = 500
	h.tg.mu.Unlock()
	h.bot.runDueSchedules(bg, now.Add(2*time.Minute))
	h.bot.wg.Wait()
	list, _ := h.st.UserSchedules(bg, 67)
	if len(list) != 1 || list[0].Status != store.ScheduleActive {
		t.Fatalf("the one-time reminder is tried again: %+v", list)
	}
	h.tg.mu.Lock()
	delete(h.tg.fail, "sendMessage")
	h.tg.mu.Unlock()
	h.bot.runDueSchedules(bg, time.Now().Add(2*time.Minute))
	h.bot.wg.Wait()
	h.wantText("قرص")
	if list, _ := h.st.UserSchedules(bg, 67); len(list) != 0 {
		t.Errorf("done once it went out: %+v", list)
	}

	h.llm.reply = func(map[string]any, int) (map[string]any, int) { return nil, 503 }
	h.bot.createSchedule(bg, u, m, map[string]any{"kind": "task", "text": "خبرها", "title": "خبر",
		"at": now.In(h.bot.loc).Add(time.Minute).Format("2006-01-02 15:04"), "repeat": "daily"}, now)
	for day := 0; day < scheduleMaxFailures; day++ {
		h.bot.runDueSchedules(bg, now.Add(time.Duration(day)*24*time.Hour+2*time.Minute))
		h.bot.wg.Wait()
	}
	list, _ = h.st.UserSchedules(bg, 67)
	if len(list) != 1 || list[0].Status != store.SchedulePaused {
		t.Fatalf("paused after %d failures: %+v", scheduleMaxFailures, list)
	}
	h.wantText("متوقفش کردم")
	v := h.bot.viewSchedules(bg, 67)
	if !strings.Contains(v.html, "⏸") || !strings.Contains(fmt.Sprint(v.kb), fmt.Sprintf("sc:r:%d", list[0].ID)) {
		t.Errorf("/reminders shows it paused, with a button to turn it on: %s %v", v.html, v.kb)
	}
	if ok, _ := h.st.ResumeSchedule(bg, list[0].ID, 67, time.Now()); !ok {
		t.Error("it can be turned on again")
	}
	if list, _ := h.st.UserSchedules(bg, 67); list[0].Status != store.ScheduleActive || list[0].Failures != 0 {
		t.Errorf("active again: %+v", list[0])
	}
}
