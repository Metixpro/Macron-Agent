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

// background starts a request and returns a wait for its end.
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

// reactedTo returns the reactions set on a message.
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

func TestAMessageDuringAJobJoinsIt(t *testing.T) {
	h := newHarness(t)
	started, proceed := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			close(started)
			<-proceed
			return toolCall("list_files", `{}`), 0
		}
		return map[string]any{"role": "assistant", "content": "گزارش آماده‌ست، با نمودار."}, 0
	}
	first := private(60, "یه گزارش از فروش بساز")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(followUpMsg(first, 78, "یه نمودار هم بذار توش"))
	close(proceed)
	wait()

	if h.llm.count() != 2 {
		t.Fatalf("one job, two model calls: %d", h.llm.count())
	}
	req := llmText(h.llm.last())
	if !strings.Contains(req, "یه نمودار هم بذار توش") || !strings.Contains(req, "while you were working") {
		t.Errorf("the model must see the new message as part of the job:\n%s", req)
	}
	if strings.Contains(h.tg.allText(), "هنوز دارم روی") {
		t.Errorf("no busy message any more:\n%s", h.tg.allText())
	}
	if r := h.reactedTo(78); !strings.Contains(r, "✍") {
		t.Errorf("the added message gets ✍: %q", r)
	}
	h.wantText("گزارش آماده‌ست، با نمودار.")
	if n := len(h.tg.of("sendRichMessage")); n != 1 {
		t.Errorf("one answer for the whole job: %d", n)
	}
	if used, _ := h.st.UsedToday(bg, 60); used != 1 {
		t.Errorf("a message added to a job is not a new request: %d used", used)
	}
	// The conversation remembers what was added.
	mc, _ := h.bot.mem.Load(bg, 60)
	if hist := fmt.Sprint(mc.History); !strings.Contains(hist, "یه نمودار هم بذار توش") {
		t.Errorf("memory misses the added message: %s", hist)
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
	reply := followUpMsg(first, 78, "انگلیسیش رو هم بنویس") // no «مکرون»: a reply to the request itself
	reply.ReplyToMessage = &telegram.Message{MessageID: 77, From: first.From, Chat: first.Chat, Text: first.Text}
	h.bot.handleMessage(reply)
	// Someone else's reply to it is not for the bot.
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
		if strings.Contains(fmt.Sprint(e.body["rich_message"]), "**MACRON** ⟫ live") {
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

// stopButtonData returns the callback data of the stop button in the live messages.
func stopButtonData(h *harness) string {
	re := regexp.MustCompile(`data="(run:stop:[^"]+)"`)
	for _, c := range append(h.tg.of("sendRichMessage"), h.tg.of("editMessageText")...) {
		if m := re.FindStringSubmatch(fmt.Sprint(c.body["rich_message"])); m != nil {
			return m[1]
		}
	}
	return ""
}

// blockedGroupJob starts a group job that waits until the test lets it go.
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

	// Another member: refused, with an alert.
	h.click(66, -100500, 501, data)
	if !strings.Contains(lastCallbackAnswer(h), "مال کسیه") || stopped() {
		t.Fatalf("another member must not stop it: %q", lastCallbackAnswer(h))
	}
	// Forged data in their own name does not reach the job either.
	h.click(66, -100500, 501, strings.Replace(data, "run:stop:65:", "run:stop:66:", 1))
	// The asker with a stale button (another job's key) or from another chat: nothing happens.
	h.click(65, -100500, 501, "run:stop:65:deadbeef")
	h.click(65, -100999, 501, data)
	h.click(65, -100500, 501, "run:stop:65")
	if stopped() {
		t.Fatal("a button stops only its own job, in its own chat")
	}
	// Someone else saying «بیخیال» stops nothing of it.
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

// The text that takes the live message's place when a job is stopped has
// premium emoji, like every other message.
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
	// The notice takes the live message's place.
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
		if method == "sendRichMessage" {
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

func TestALongJobContinuesByItself(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "1")
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch {
		case n < 4:
			return toolCall("list_files", fmt.Sprintf(`{"path":"part%d"}`, n)), 0
		case n == 4:
			return map[string]any{"role": "assistant", "content": "نصف کار انجام شد."}, 0
		}
		return map[string]any{"role": "assistant", "content": "کل پروژه تموم شد ✅"}, 0
	}
	h.send(private(68, "یه پروژه بزرگ بساز"))
	text := h.tg.allText()
	if !strings.Contains(text, "نصف کار انجام شد.") || !strings.Contains(text, "⏩") || !strings.Contains(text, "کل پروژه تموم شد ✅") {
		t.Fatalf("a progress report, then the job goes on and finishes:\n%s", text)
	}
	if strings.Index(text, "⏩") > strings.Index(text, "کل پروژه") {
		t.Error("the progress report comes first")
	}
	h.llm.mu.Lock()
	wrap, next := llmText(h.llm.requests[4]), llmText(h.llm.requests[5])
	h.llm.mu.Unlock()
	if !strings.Contains(wrap, "goes on automatically") {
		t.Errorf("the report does not ask the user to continue:\n%s", wrap)
	}
	if !strings.Contains(next, "automatic continuation") || !strings.Contains(next, "work log before stopping") {
		t.Errorf("the next round starts from the work log:\n%s", next)
	}
	if used, _ := h.st.UsedToday(bg, 68); used != 1 {
		t.Errorf("one request, however many rounds: %d used", used)
	}
}

func TestTheAnswerOfALongJobIsStillDelivered(t *testing.T) {
	h := newHarness(t)
	old := answerSendBudget
	answerSendBudget = 300 * time.Millisecond
	t.Cleanup(func() { answerSendBudget = old })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		time.Sleep(700 * time.Millisecond) // longer than the time to deliver
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
	r := newRun(1, private(1, "x"), func() {}, nil, tierOwner)
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
	// Past the limit nothing is refused: it joins the last one waiting.
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

// Stopping while the progress report is being sent ends the job: it does
// not go on by itself.
func TestStopDuringTheReportEndsTheAutoContinue(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "1")
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch {
		case n < 4:
			return toolCall("list_files", fmt.Sprintf(`{"path":"part%d"}`, n)), 0
		case n == 4:
			return map[string]any{"role": "assistant", "content": "نصف کار انجام شد."}, 0
		}
		return map[string]any{"role": "assistant", "content": "کل پروژه تموم شد ✅"}, 0
	}
	h.tg.mu.Lock()
	h.tg.reject = func(method string, body map[string]any) string {
		if strings.Contains(fmt.Sprint(body), "⏩") {
			if r, ok := h.bot.runs.Load(int64(70)); ok {
				r.(*activeRun).stop() // the user taps stop while the report goes out
			}
		}
		return ""
	}
	h.tg.mu.Unlock()
	h.send(private(70, "یه پروژه بزرگ بساز"))
	if text := h.tg.allText(); strings.Contains(text, "کل پروژه تموم شد") {
		t.Fatalf("the job does not go on after a stop:\n%s", text)
	}
	if n := h.llm.count(); n != 5 {
		t.Errorf("no round after the stop: %d model calls", n)
	}
}

// A message sent between two rounds of a job that goes on by itself joins
// the next round instead of starting a second job beside it.
func TestAMessageBetweenRoundsJoinsTheJob(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingAutoContinue, "1")
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch {
		case n < 4:
			return toolCall("list_files", fmt.Sprintf(`{"path":"part%d"}`, n)), 0
		case n == 4:
			return map[string]any{"role": "assistant", "content": "نصف کار انجام شد."}, 0
		}
		return map[string]any{"role": "assistant", "content": "کل پروژه تموم شد ✅"}, 0
	}
	once := sync.Once{}
	betweenRounds = func() {
		once.Do(func() { h.bot.handleMessage(private(72, "یه README هم بنویس")) })
	}
	t.Cleanup(func() { betweenRounds = nil })
	h.send(private(72, "یه پروژه بزرگ بساز"))
	h.llm.mu.Lock()
	defer h.llm.mu.Unlock()
	joined := false
	for _, r := range h.llm.requests[5:] {
		if strings.Contains(llmText(r), "یه README هم بنویس") {
			joined = true
		}
	}
	if !joined {
		t.Error("the message reaches the next round of the job")
	}
	if n := len(h.llm.requests); n != 6 {
		t.Errorf("one job, not two side by side: %d model calls", n)
	}
}

// A message that comes while the user's job is still starting (it holds the
// key but is not yet open to messages) is not refused as «busy»: it waits
// for the job to open and joins it. That is what a second message sent in the
// same second as the first met.
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
		time.Sleep(700 * time.Millisecond) // the job opens a moment after the message arrived
		h.bot.runs.Store(int64(12), run)
	}()
	h.send(followUpMsg(first, 99, "این رو هم اضافه کن"))
	h.noText("هنوز دارم روی درخواست قبلی")
	h.noText("still working on your previous request")
	xs := run.take()
	if len(xs) != 1 || !strings.Contains(xs[0].text, "این رو هم اضافه کن") {
		t.Errorf("the message joined the job: %v", xs)
	}
	if len(h.llm.requests) != 0 {
		t.Error("it did not start a second job")
	}
}
