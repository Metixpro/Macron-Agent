package main

import (
	"fmt"

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

func stopButtonData(h *harness) string {
	for _, method := range []string{"sendRichMessage", "sendMessage", "editMessageText"} {
		for _, c := range h.tg.of(method) {
			kb, _ := c.body["reply_markup"].(map[string]any)
			rows, _ := kb["inline_keyboard"].([]any)
			for _, r := range rows {
				for _, v := range r.([]any) {
					button := v.(map[string]any)
					data, _ := button["callback_data"].(string)
					if strings.HasPrefix(data, "run:stop:") {
						return data
					}
				}
			}
		}
	}
	return ""
}

func blockedActiveJob(t *testing.T, h *harness, uid int64) (proceed func(), wait func()) {
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
	wait = h.background(private(uid, "مکرون یه کار طولانی بکن"))
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
	proceed, wait := blockedActiveJob(t, h, 65)
	data := stopButtonData(h)
	if !strings.HasPrefix(data, "run:stop:65:") || len(data) <= len("run:stop:65:") {
		t.Fatalf("the button names the job: %q", data)
	}
	run, _ := h.bot.runs.Load(int64(65))
	stopped := func() bool { return run.(*activeRun).stopped.Load() }

	h.click(66, 65, 501, data)
	if !strings.Contains(lastCallbackAnswer(h), "مال کسیه") || stopped() {
		t.Fatalf("another member must not stop it: %q", lastCallbackAnswer(h))
	}

	h.click(66, 65, 501, strings.Replace(data, "run:stop:65:", "run:stop:66:", 1))

	h.click(65, 65, 501, "run:stop:65:deadbeef")
	h.click(65, -100999, 501, data)
	h.click(65, 65, 501, "run:stop:65")
	if stopped() {
		t.Fatal("a button stops only its own job, in its own chat")
	}

	other := group(66, "مکرون بیخیال")
	other.MessageID = 90
	h.bot.handleMessage(other)
	if stopped() {
		t.Fatal("another member's «بیخیال» must not stop someone else's job")
	}

	h.click(65, 65, 501, data)
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
	proceed, wait := blockedActiveJob(t, h, 67)
	h.click(ownerID, 67, 501, stopButtonData(h))
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
	proceed, wait := blockedActiveJob(t, h, 68)
	h.click(68, 68, 501, stopButtonData(h))
	proceed()
	wait()
	h.wantText(`<tg-emoji emoji-id="5294096239464295059">✋</tg-emoji>`)
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
