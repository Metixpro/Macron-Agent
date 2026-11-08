package main

import (
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (h *harness) background(m *telegram.Message) func() {
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.send(m)
	}()
	return func() {
		select {
		case <-done:
		case <-time.After(20 * time.Second):
			h.t.Fatal("the request did not finish")
		}
	}
}

func (h *harness) reactedTo(msgID int64) string {
	var out []string
	for _, c := range h.tg.of("setMessageReaction") {
		if id, _ := c.body["message_id"].(float64); int64(id) == msgID {
			out = append(out, fmt.Sprint(c.body["reaction"]))
		}
	}
	return strings.Join(out, " ")
}

func followUpMsg(m *telegram.Message, id int64, text string) *telegram.Message {
	n := *m
	n.MessageID, n.Text, n.ReplyToMessage = id, text, nil
	return &n
}

func TestPrivateMessageDuringJobRunsNext(t *testing.T) {
	h := newHarness(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-proceed
			return toolCall("list_files", `{}`), 0
		}
		if n == 1 {
			return map[string]any{"role": "assistant", "content": "پوشه بررسی شد"}, 0
		}
		return map[string]any{"role": "assistant", "content": "این پاسخ بعدی است"}, 0
	}
	first := private(60, "فایل‌های پوشه را بررسی کن")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "/queue حالا پیام بعدی را پاسخ بده"))
	close(proceed)
	wait()
	if h.llm.count() != 3 {
		t.Fatalf("expected separate requests: %d", h.llm.count())
	}
	if strings.Contains(llmText(h.llm.requests[1]), "حالا پیام بعدی را پاسخ بده") {
		t.Fatal("queued message leaked into active task")
	}
	if !strings.Contains(llmText(h.llm.last()), "حالا پیام بعدی را پاسخ بده") || strings.Contains(llmText(h.llm.last()), "while you were working") {
		t.Fatal("queued message did not start a separate turn")
	}
	h.wantText("پوشه بررسی شد")
	h.wantText("این پاسخ بعدی است")
	if used, _ := h.st.UsedToday(bg, 60); used != 2 {
		t.Fatalf("two requests must be counted, got %d", used)
	}
}

func TestSayingStopDuringAJobStopsIt(t *testing.T) {
	h := newHarness(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(proceed) })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-proceed
		}
		return map[string]any{"role": "assistant", "content": "نباید برسه"}, 0
	}
	first := private(61, "یه سایت کامل برام بساز")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "نه داداش بیخیال تسک شو"))
	wait()
	h.wantText(textCancelled)
	h.noText("نباید برسه")
	if r := h.reactedTo(78); !strings.Contains(r, "👌") {
		t.Errorf("the stop message gets 👌: %q", r)
	}
	if used, _ := h.st.UsedToday(bg, 61); used != 1 {
		t.Errorf("a stopped job counts: %d used", used)
	}
}

func TestReplyingToYourOwnRequestInAGroupJoinsTheJob(t *testing.T) {
	h := newHarness(t)
	old := groupEditEvery
	groupEditEvery = 0
	t.Cleanup(func() { groupEditEvery = old })
	started, proceed := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-proceed
			return toolCall("list_files", `{}`), 0
		}
		return map[string]any{"role": "assistant", "content": "انجام شد، به انگلیسی هم نوشتم."}, 0
	}
	first := group(62, "مکرون یه متن تبریک بنویس")
	wait := h.background(first)
	<-started
	reply := followUpMsg(first, 78, "انگلیسیش رو هم بنویس")
	reply.ReplyToMessage = &telegram.Message{MessageID: 77, From: first.From, Chat: first.Chat, Text: first.Text}
	h.bot.handleMessage(reply)

	other := followUpMsg(first, 79, "منم می‌خوام")
	other.From = &telegram.User{ID: 63, FirstName: "Sara"}
	other.ReplyToMessage = reply.ReplyToMessage
	h.bot.handleMessage(other)
	close(proceed)
	wait()
	if req := llmText(h.llm.last()); !strings.Contains(req, "انگلیسیش رو هم بنویس") || strings.Contains(req, "منم می‌خوام") {
		t.Errorf("the reply joins the job, the other member's does not:\n%s", req)
	}
	h.wantText("انجام شد، به انگلیسی هم نوشتم.")
}

func TestTheGroupLiveMessageBecomesTheAnswer(t *testing.T) {
	h := newHarness(t)
	old := groupEditEvery
	groupEditEvery = 0
	t.Cleanup(func() { groupEditEvery = old })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			time.Sleep(400 * time.Millisecond)
			return toolCall("list_files", `{}`), 0
		}
		time.Sleep(600 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "فضای کاری **خالیه**."}, 0
	}
	h.send(group(64, "مکرون فایل‌هام چیه؟"))

	sent := h.tg.of("sendRichMessage")
	if len(sent) != 1 {
		t.Fatalf("one message from the start to the answer, got %d:\n%s", len(sent), h.tg.allText())
	}
	body := sent[0].body
	if md := fmt.Sprint(body["rich_message"]); !strings.Contains(md, "💭") || !strings.Contains(md, thinkingText) {
		t.Errorf("it starts by thinking: %s", md)
	}
	if rp, _ := body["reply_parameters"].(map[string]any); rp == nil || rp["message_id"].(float64) != 77 {
		t.Errorf("it replies to the request: %v", body["reply_parameters"])
	}
	if body["reply_markup"] != nil {
		t.Errorf("a stop button only once the job takes a while: %v", body["reply_markup"])
	}
	edits := h.tg.of("editMessageText")
	if len(edits) < 2 {
		t.Fatalf("edited along: %d edits", len(edits))
	}
	first := fmt.Sprint(edits[0].body["rich_message"])
	if !strings.Contains(first, `<tg-button type="callback_data" style="link" data="run:stop:64:`) || edits[0].body["reply_markup"] != nil {
		t.Errorf("working with tools: a borderless stop link inside the message, no keyboard under it: %s %v", first, edits[0].body["reply_markup"])
	}
	terminal := false
	for _, e := range edits {
		if strings.Contains(fmt.Sprint(e.body["rich_message"]), "MACRON ⟫ live") {
			terminal = true
		}
	}
	if !terminal {
		t.Errorf("the same message shows the terminal:\n%s", h.tg.allText())
	}
	last := edits[len(edits)-1].body
	if md := fmt.Sprint(last["rich_message"]); !strings.Contains(md, "فضای کاری **خالیه**.") || strings.Contains(md, "tg-button") {
		t.Errorf("it ends as the answer: %s", md)
	}
	if kb := fmt.Sprint(last["reply_markup"]); strings.Contains(kb, "run:stop") {
		t.Errorf("the answer has no stop button: %s", kb)
	}
	if n := len(h.tg.of("deleteMessage")); n != 0 {
		t.Errorf("nothing is deleted: %d", n)
	}
}

func TestTheStopButtonMovesUnderTheMessageOnOlderServers(t *testing.T) {
	h := newHarness(t)
	old := groupEditEvery
	groupEditEvery = 0
	t.Cleanup(func() { groupEditEvery = old })
	h.tg.setReject(func(method string, body map[string]any) string {
		if strings.Contains(fmt.Sprint(body["rich_message"]), "tg-button-row") {
			return "Bad Request: can't parse rich message: unknown tag tg-button-row"
		}
		return ""
	})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			time.Sleep(300 * time.Millisecond)
			return toolCall("list_files", `{}`), 0
		}
		time.Sleep(600 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "تموم شد."}, 0
	}
	h.send(group(70, "مکرون فایل‌هام چیه؟"))
	if !h.bot.stopRowOff.Load() {
		t.Fatal("the server refused buttons inside the message: remember it")
	}
	withKB := false
	for _, e := range h.tg.of("editMessageText") {
		if kb := fmt.Sprint(e.body["reply_markup"]); strings.Contains(kb, "run:stop:70:") {
			withKB = true
			if strings.Contains(kb, "danger") {
				t.Errorf("a calm button, not a red alarm: %s", kb)
			}
		}
	}
	if !withKB {
		t.Errorf("the stop button goes under the message instead:\n%s", h.tg.allText())
	}
	h.wantText("تموم شد.")
}

func stopButtonData(h *harness) string {
	re := regexp.MustCompile(`data="(run:stop:[^"]+)"`)
	for _, c := range append(h.tg.of("sendRichMessage"), h.tg.of("editMessageText")...) {
		if m := re.FindStringSubmatch(fmt.Sprint(c.body["rich_message"])); m != nil {
			return m[1]
		}
	}
	return ""
}

func blockedGroupJob(t *testing.T, h *harness, uid int64) (proceed func(), wait func()) {
	old := stopButtonAfter
	stopButtonAfter = 0
	t.Cleanup(func() { stopButtonAfter = old })
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	proceed = func() { once.Do(func() { close(release) }) }
	t.Cleanup(proceed)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
		}
		return map[string]any{"role": "assistant", "content": "کار تموم شد ✅"}, 0
	}
	wait = h.background(group(uid, "مکرون یه کار طولانی بکن"))
	<-started
	for i := 0; i < 50 && stopButtonData(h) == ""; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	return proceed, wait
}

func lastCallbackAnswer(h *harness) string {
	a := h.tg.of("answerCallbackQuery")
	if len(a) == 0 {
		return ""
	}
	return fmt.Sprint(a[len(a)-1].body["text"])
}

func TestOnlyTheAskerOrABotAdminCanStopAJob(t *testing.T) {
	h := newHarness(t)
	proceed, wait := blockedGroupJob(t, h, 65)
	data := stopButtonData(h)
	if !strings.HasPrefix(data, "run:stop:65:") || len(data) <= len("run:stop:65:") {
		t.Fatalf("the button names the job: %q", data)
	}
	run, _ := h.bot.runs.Load(int64(65))
	stopped := func() bool { return run.(*activeRun).stopped.Load() }

	h.click(66, -100500, 501, data)
	if !strings.Contains(lastCallbackAnswer(h), "مال کسیه") || stopped() {
		t.Fatalf("another member must not stop it: %q", lastCallbackAnswer(h))
	}

	h.click(66, -100500, 501, strings.Replace(data, "run:stop:65:", "run:stop:66:", 1))

	h.click(65, -100500, 501, "run:stop:65:deadbeef")
	h.click(65, -100999, 501, data)
	h.click(65, -100500, 501, "run:stop:65")
	if stopped() {
		t.Fatal("a button stops only its own job, in its own chat")
	}

	other := group(66, "مکرون بیخیال")
	other.MessageID = 90
	h.bot.handleMessage(other)
	if stopped() {
		t.Fatal("another member's «بیخیال» must not stop someone else's job")
	}

	h.click(65, -100500, 501, data)
	if !strings.Contains(lastCallbackAnswer(h), "متوقفش کردم") {
		t.Errorf("the asker stops it: %q", lastCallbackAnswer(h))
	}
	proceed()
	wait()
	if !stopped() {
		t.Fatal("stopped")
	}
	h.wantText(textCancelled)
}

func TestABotAdminCanStopAnyJob(t *testing.T) {
	h := newHarness(t)
	proceed, wait := blockedGroupJob(t, h, 67)
	h.click(ownerID, -100500, 501, stopButtonData(h))
	if !strings.Contains(lastCallbackAnswer(h), "متوقفش کردم") {
		t.Errorf("a bot admin may stop it: %q", lastCallbackAnswer(h))
	}
	proceed()
	wait()
	h.wantText(textCancelled)
	h.noText("کار تموم شد ✅")
}

func TestAStoppedJobsTextHasPremiumEmoji(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingPremiumEmoji, "true")
	_ = h.st.SetPremiumEmoji(bg, "✋", "5294096239464295059")
	h.bot.reloadEmoji(bg)
	proceed, wait := blockedGroupJob(t, h, 68)
	h.click(68, -100500, 501, stopButtonData(h))
	proceed()
	wait()
	h.wantText(`<tg-emoji emoji-id="5294096239464295059">✋</tg-emoji>`)
}

func TestTheStopButtonUnderTheGroupLiveMessage(t *testing.T) {
	h := newHarness(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	t.Cleanup(func() { close(proceed) })
	old := stopButtonAfter
	stopButtonAfter = 0
	t.Cleanup(func() { stopButtonAfter = old })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-proceed
		}
		return map[string]any{"role": "assistant", "content": "نباید برسه"}, 0
	}
	wait := h.background(group(65, "مکرون یه کار طولانی بکن"))
	<-started
	for i := 0; i < 50 && stopButtonData(h) == ""; i++ {
		time.Sleep(20 * time.Millisecond)
	}
	h.click(65, -100500, 501, stopButtonData(h))
	wait()
	h.wantText(textCancelled)
	h.noText("نباید برسه")

	edits := h.tg.of("editMessageText")
	if len(edits) == 0 || !strings.Contains(fmt.Sprint(edits[len(edits)-1].body["rich_message"]), "ولش کردم") {
		t.Errorf("the live message says it stopped: %v", edits)
	}
}

func TestAMessageAfterTheAnswerRunsNext(t *testing.T) {
	h := newHarness(t)
	var once sync.Once
	answering, proceed := make(chan struct{}), make(chan struct{})
	h.tg.setReject(func(method string, body map[string]any) string {
		if method == "editMessageText" && body["rich_message"] != nil && !strings.Contains(fmt.Sprint(body["reply_markup"]), "run:stop:") {
			once.Do(func() { close(answering); <-proceed })
		}
		return ""
	})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		return map[string]any{"role": "assistant", "content": fmt.Sprintf("جواب %d", n+1)}, 0
	}
	first := private(67, "سوال اول")
	wait := h.background(first)
	<-answering
	h.bot.handleMessage(followUpMsg(first, 78, "سوال دوم"))
	close(proceed)
	wait()
	if h.llm.count() != 2 {
		t.Fatalf("the second message is its own request: %d calls", h.llm.count())
	}
	if req := llmText(h.llm.last()); !strings.Contains(req, "سوال دوم") || strings.Contains(req, "while you were working") {
		t.Errorf("asked as a new request:\n%s", req)
	}
	if r := h.reactedTo(78); !strings.Contains(r, "👀") {
		t.Errorf("a queued message gets 👀: %q", r)
	}
	h.wantText("جواب 1")
	h.wantText("جواب 2")
	h.noText("هنوز دارم روی")
}

func TestALongJobRunsToTheEnd(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "0")
	const parts = 30
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n < parts {
			return toolCall("list_files", fmt.Sprintf(`{"path":"part%d"}`, n)), 0
		}
		return map[string]any{"role": "assistant", "content": "کل پروژه تموم شد ✅"}, 0
	}
	h.send(private(68, "یه پروژه بزرگ بساز"))
	text := h.tg.allText()
	if !strings.Contains(text, "کل پروژه تموم شد ✅") || strings.Contains(text, "⏩") || strings.Contains(text, "⏸") {
		t.Fatalf("the job runs to its end without stopping:\n%s", text)
	}
	if n := h.llm.count(); n != parts+1 {
		t.Errorf("%d model calls, want %d: one run, no wrap-ups", n, parts+1)
	}
	h.llm.mu.Lock()
	last := llmText(h.llm.requests[len(h.llm.requests)-1])
	h.llm.mu.Unlock()
	if !strings.Contains(last, "got 25 more tool rounds") || !strings.Contains(last, "Progress so far") {
		t.Errorf("the model is told its budget grew, with the progress so far:\n%s", last)
	}
	if used, _ := h.st.UsedToday(bg, 68); used != 1 {
		t.Errorf("one request: %d used", used)
	}
}

func TestAutoContinueLimitIsKept(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "1")
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if req["tools"] == nil {
			return map[string]any{"role": "assistant", "content": "تا اینجا انجام شد."}, 0
		}
		return toolCall("list_files", fmt.Sprintf(`{"path":"part%d"}`, n)), 0
	}
	h.send(private(73, "یه پروژه بزرگ بساز"))

	if n := h.llm.count(); n != 4+25+1 {
		t.Errorf("%d model calls", n)
	}
}

func TestTheAnswerOfALongJobIsStillDelivered(t *testing.T) {
	h := newHarness(t)
	old := answerSendBudget
	answerSendBudget = 300 * time.Millisecond
	t.Cleanup(func() { answerSendBudget = old })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		time.Sleep(700 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "بعد از یه کار طولانی، اینم جواب."}, 0
	}
	h.send(private(69, "یه کار طولانی"))
	h.wantText("بعد از یه کار طولانی، اینم جواب.")
}

func TestStopRequests(t *testing.T) {
	for _, s := range []string{"بیخیال", "بیخیال شو", "ولش کن داداش", "نه دیگه ادامه نده", "کنسلش کن!", "نمی‌خواد", "نمی خواد",
		"وایسا", "بسه دیگه", "مکرون بیخیالش", "stop", "Cancel it", "never mind", "nvm 🙏", "ok stop"} {
		s = strings.TrimPrefix(s, "مکرون ")
		if !isStopRequest(s) {
			t.Errorf("%q should stop the job", s)
		}
	}
	for _, s := range []string{"بیخیال اون بخش شو فقط عکس رو بفرست", "یه نمودار هم اضافه کن", "stop words in python",
		"چرا متوقف شد؟", "", "ادامه بده", "how do I cancel a subscription"} {
		if isStopRequest(s) {
			t.Errorf("%q is not a stop request", s)
		}
	}
}

func TestActiveRunInbox(t *testing.T) {
	m := private(1, "x")
	m.Chat.Type = "supergroup"
	r := newRun(1, m, func() {}, nil, tierOwner)
	x := &interjection{text: "a", memo: "a"}
	if ok, queued := r.push(x); !ok || queued {
		t.Fatalf("push: %v %v", ok, queued)
	}
	if got := r.take(); len(got) != 1 || r.memo() != "- a" {
		t.Fatalf("take: %v %q", got, r.memo())
	}
	r.answered()
	if _, queued := r.push(&interjection{text: "b"}); !queued {
		t.Error("after the answer, messages wait for the next request")
	}
	if got := r.take(); got != nil {
		t.Error("the model no longer takes messages")
	}
	for i := 0; i < maxPending; i++ {
		r.push(&interjection{text: "c"})
	}

	more := &interjection{text: "LATE-1", memo: "late", msg: llm.Message{Role: "user", Content: agent.InterjectionNote + "LATE-1"}}
	if ok, _ := r.push(more); !ok {
		t.Fatal("a message past the limit is refused")
	}
	left := r.finish()
	if len(left) != maxPending || !strings.Contains(left[maxPending-1].text, "LATE-1") || !strings.Contains(left[maxPending-1].msg.Text(), "LATE-1") ||
		strings.Count(left[maxPending-1].msg.Text(), agent.InterjectionNote) > 1 {
		t.Errorf("leftovers: %d, last %+v", len(left), left[len(left)-1])
	}
	if ok, _ := r.push(x); ok {
		t.Error("a finished run takes nothing")
	}
	if agent.InterjectionNote == "" {
		t.Error("the note")
	}
}

func TestAMessageDuringJobStartJoinsItInsteadOfBusy(t *testing.T) {
	h := newHarness(t)
	release, why := h.bot.gate.TryAcquire("12")
	if why != 0 {
		t.Fatalf("the key is free: %v", why)
	}
	defer release()
	first := private(12, "اولین پیام")
	run := newRun(12, first, func() {}, nil, tierOwner)
	go func() {
		time.Sleep(700 * time.Millisecond)
		h.bot.runs.Store(int64(12), run)
	}()
	h.send(followUpMsg(first, 99, "این رو هم اضافه کن"))
	h.noText("هنوز دارم روی درخواست قبلی")
	h.noText("still working on your previous request")
	xs := run.finish()
	if len(xs) != 1 || !strings.Contains(xs[0].text, "این رو هم اضافه کن") {
		t.Errorf("the message joined the job: %v", xs)
	}
	if len(h.llm.requests) != 0 {
		t.Error("it did not start a second job")
	}
}

func TestPrivateQueueKeepsFIFOAndSurvivesStop(t *testing.T) {
	r := newRun(1, private(1, "first"), func() {}, nil, tierOwner)
	for i := 0; i < maxPending; i++ {
		ok, queued := r.push(&interjection{text: fmt.Sprint(i)})
		if !ok || !queued {
			t.Fatal("private message did not queue")
		}
	}
	if ok, full := r.push(&interjection{text: "overflow"}); ok || !full {
		t.Fatal("full queue did not reject the extra message")
	}
	if r.take() != nil {
		t.Fatal("active task consumed the next task's messages")
	}
	r.stop()
	queued := r.finish()
	if len(queued) != maxPending {
		t.Fatal("stop lost queued tasks")
	}
	for i, x := range queued {
		if x.text != fmt.Sprint(i) {
			t.Fatal("queue order changed")
		}
	}
}

func TestPrivateMessagesRunOneAtATimeInArrivalOrder(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
		}
		return map[string]any{"role": "assistant", "content": fmt.Sprintf("پاسخ نوبت %d", n)}, 0
	}
	first := private(63, "درخواست شماره اول")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "/queue درخواست شماره دوم"))
	h.bot.handleMessage(followUpMsg(first, 79, "/queue درخواست شماره سوم"))
	if h.llm.count() != 1 {
		t.Error("queued requests started before the current one ended")
	}
	close(release)
	wait()
	if h.llm.count() != 3 {
		t.Fatalf("expected three separate turns, got %d", h.llm.count())
	}
	for i, want := range []string{"درخواست شماره اول", "درخواست شماره دوم", "درخواست شماره سوم"} {
		messages := h.llm.requests[i]["messages"].([]any)
		last := messages[len(messages)-1].(map[string]any)
		if !strings.Contains(fmt.Sprint(last["content"]), want) {
			t.Fatalf("wrong queued turn %d: %v", i, last)
		}
	}
}

func TestStoppingPrivateTaskStillRunsTheQueuedMessage(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-release
			return map[string]any{"role": "assistant", "content": "late"}, 0
		}
		return map[string]any{"role": "assistant", "content": "پاسخ پیام بعدی"}, 0
	}
	first := private(64, "اولین درخواست را انجام بده")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "/queue پیام منتظر در صف را پاسخ بده"))
	stopLiveMessage(h, 64)
	close(release)
	wait()
	h.wantText(textCancelled)
	h.wantText("پاسخ پیام بعدی")
	h.noText("late")
	if h.llm.count() != 2 || !strings.Contains(llmText(h.llm.last()), "پیام منتظر در صف را پاسخ بده") {
		t.Fatalf("stopping current task did not preserve the next turn; calls=%d", h.llm.count())
	}
}
