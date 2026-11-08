package main

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
)

// bgModel plays a model that sends a build to the background: the task's own
// run waits until the test lets it go; the user's other messages are answered
// meanwhile.
func bgModel(t *testing.T, h *harness) (started <-chan struct{}, release func()) {
	t.Helper()
	old := stopButtonAfter
	stopButtonAfter = 0
	t.Cleanup(func() { stopButtonAfter = old })
	st, rel := make(chan struct{}), make(chan struct{})
	var once, first sync.Once
	release = func() { once.Do(func() { close(rel) }) }
	t.Cleanup(release) // before the fake model's server closes
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		msgs, _ := req["messages"].([]any)
		last := fmt.Sprint(msgs[len(msgs)-1])
		switch {
		case strings.Contains(llmText(req), "This is background task #"):
			first.Do(func() { close(st) })
			<-rel
			return say("APK ساخته شد ✅ فایلش توی فضای کاریته.")(req, n)
		case strings.Contains(last, "Started background task"):
			return say("شروع کردم؛ نتیجه رو جدا برات می‌فرستم.")(req, n)
		case strings.Contains(last, "refused:"):
			return say("الان دو تا کار در جریانه؛ صبر کن یکیش تموم بشه.")(req, n)
		case strings.Contains(last, "پایتخت"):
			return say("پایتخت فرانسه پاریسه.")(req, n)
		}
		return toolCall(toolBackground, `{"title":"بیلد APK اپ صدا","goal":"Build the Android app in app1 with ./gradlew assembleDebug and send the APK"}`), 0
	}
	return st, release
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 600; i++ {
		if cond() {
			return
		}
		time.Sleep(25 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func waitChan(t *testing.T, what string, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(15 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// callsWith are the bot's calls whose text has sub.
func callsWith(h *harness, sub string) []tgCall {
	var out []tgCall
	for _, method := range []string{"sendMessage", "sendRichMessage", "editMessageText", "editRichMessage"} {
		for _, c := range h.tg.of(method) {
			if strings.Contains(fmt.Sprint(c.body), sub) {
				out = append(out, c)
			}
		}
	}
	return out
}

func onlyTask(t *testing.T, h *harness, uid int64) store.BgTask {
	t.Helper()
	tasks, err := h.st.UserBgTasks(bg, uid, 10)
	if err != nil || len(tasks) == 0 {
		t.Fatalf("no background task: %v", err)
	}
	return tasks[0]
}

func TestABackgroundTaskRunsWhileTheUserChats(t *testing.T) {
	h := newHarness(t)
	const uid = 71
	started, release := bgModel(t, h)
	request := private(uid, "اپ اندرویدم رو بیلد کن و APK رو بفرست")
	h.bot.handleMessage(request)
	waitUntil(t, "the request's own answer", func() bool { return strings.Contains(h.tg.allText(), "شروع کردم") })
	waitChan(t, "the task to start", started)
	task := onlyTask(t, h, uid)
	if task.Status != store.BgRunning || task.Title != "بیلد APK اپ صدا" || task.MsgID != 77 || task.ChatID != uid {
		t.Fatalf("task: %+v", task)
	}

	// The user goes on chatting: answered right away, never "busy".
	h.bot.handleMessage(followUpMsg(request, 78, "پایتخت فرانسه کجاست؟"))
	waitUntil(t, "the chat answer", func() bool { return strings.Contains(h.tg.allText(), "پاریسه") })
	h.noText(textBusy)

	// /tasks lists it, with its stop button.
	h.bot.handleMessage(followUpMsg(request, 79, "/tasks"))
	waitUntil(t, "/tasks", func() bool { return len(callsWith(h, "کارهای پس‌زمینه</b>")) > 0 })
	list := callsWith(h, "کارهای پس‌زمینه</b>")
	if !hasButton(buttons(list[len(list)-1]), fmt.Sprintf("bg:stop:%d:%d", uid, task.ID)) {
		t.Fatalf("no ⏹ for the running task: %v", buttons(list[len(list)-1]))
	}

	// Its card is a live message of its own, with its own stop button.
	waitUntil(t, "the task's card", func() bool { return len(callsWith(h, fmt.Sprintf("bg:stop:%d:%d", uid, task.ID))) > 1 })
	if len(callsWith(h, "کار پس‌زمینه: بیلد APK")) == 0 {
		t.Fatalf("the card does not name the task:\n%s", h.tg.allText())
	}

	release()
	h.bot.wg.Wait()
	done := callsWith(h, "APK ساخته شد")
	if len(done) == 0 {
		t.Fatalf("no result:\n%s", h.tg.allText())
	}
	for _, c := range done {
		if strings.HasPrefix(c.method, "edit") {
			t.Fatalf("the result must be a new message (Telegram tells the user), not an edit: %s", c.method)
		}
	}
	if r, _ := done[0].body["reply_parameters"].(map[string]any); r == nil || r["message_id"] != float64(77) {
		t.Fatalf("the result replies to the request: %v", done[0].body)
	}
	h.wantText("کار پس‌زمینه تموم شد")
	if got := onlyTask(t, h, uid); got.Status != store.BgDone || got.FinishedAt == nil {
		t.Fatalf("task after the end: %+v", got)
	}
	// The conversation keeps what the task did.
	mc, err := h.bot.mem.Load(bg, uid)
	if err != nil {
		t.Fatal(err)
	}
	var kept bool
	for _, m := range mc.History {
		kept = kept || strings.Contains(fmt.Sprint(m.Content), "background task #")
	}
	if !kept {
		t.Fatalf("the memory does not keep the task: %+v", mc.History)
	}
}

func TestABackgroundTaskStopsAndStartsAgain(t *testing.T) {
	h := newHarness(t)
	const uid = 72
	started, release := bgModel(t, h)
	h.bot.handleMessage(private(uid, "اپ رو بیلد کن"))
	waitChan(t, "the task to start", started)
	task := onlyTask(t, h, uid)
	stop := fmt.Sprintf("bg:stop:%d:%d", uid, task.ID)

	// Someone else's press does nothing.
	h.clickAsync(uid+1, uid, 900, stop)
	if _, ok := h.bot.bgRuns.Load(task.ID); !ok {
		t.Fatal("another user stopped the task")
	}
	h.clickAsync(uid, uid, 900, stop)
	waitUntil(t, "the stop", func() bool {
		got, _ := h.st.BgTaskByID(bg, task.ID)
		return got != nil && got.Status == store.BgStopped
	})
	waitUntil(t, "the stop notice", func() bool { return len(callsWith(h, "⚠️ <b>کار پس‌زمینه:</b>")) > 0 })

	// /tasks offers it again; 🔁 runs it with the same goal.
	release()
	h.bot.handleMessage(private(uid, "/tasks"))
	h.bot.wg.Wait()
	list := callsWith(h, "کارهای پس‌زمینه</b>")
	retry := fmt.Sprintf("bg:retry:%d:%d", uid, task.ID)
	if len(list) == 0 || !hasButton(buttons(list[len(list)-1]), retry) {
		t.Fatalf("no 🔁 for the stopped task:\n%s", h.tg.allText())
	}
	h.click(uid+1, uid, 901, retry) // only its user
	if tasks, _ := h.st.UserBgTasks(bg, uid, 10); len(tasks) != 1 {
		t.Fatal("another user restarted the task")
	}
	h.click(uid, uid, 901, retry)
	h.bot.wg.Wait()
	tasks, _ := h.st.UserBgTasks(bg, uid, 10)
	if len(tasks) != 2 || tasks[0].Status != store.BgDone || tasks[0].Goal != task.Goal {
		t.Fatalf("the retry: %+v", tasks)
	}
	h.wantText("APK ساخته شد")
}

func TestARestartTellsAboutTheBackgroundTask(t *testing.T) {
	h := newHarness(t)
	const uid = 73
	if _, _, err := h.st.TouchUser(bg, store.UserInfo{ID: uid, FirstName: "Sara", LanguageCode: "fa"}); err != nil {
		t.Fatal(err)
	}
	id, err := h.st.AddBgTask(bg, store.BgTask{UserID: uid, ChatID: uid, ChatType: "private", MsgID: 640, Title: "رندر ویدیو تبلیغاتی",
		Goal: "Render promo.mp4 from scenes/ at 1080p and send it"})
	if err != nil {
		t.Fatal(err)
	}
	_ = h.st.StartBgTask(bg, id)
	tasks := h.bot.orphanedBgTasks(bg)
	h.bot.tellBgRestarted(bg, tasks)
	h.wantText("رندر ویدیو تبلیغاتی")
	h.wantText("ری‌استارت شد")
	notice := lastOf(h, "sendMessage")
	if !hasButton(buttons(notice), fmt.Sprintf("bg:retry:%d:%d", uid, id)) {
		t.Fatalf("no 🔁: %v", buttons(notice))
	}
	if r, _ := notice.body["reply_parameters"].(map[string]any); r == nil || r["message_id"] != float64(640) {
		t.Fatalf("the notice replies to the request: %v", notice.body)
	}
	if got, _ := h.st.BgTaskByID(bg, id); got.Status != store.BgInterrupted {
		t.Fatalf("status %s", got.Status)
	}
	// A second start finds nothing to tell.
	if again := h.bot.orphanedBgTasks(bg); len(again) != 0 {
		t.Fatalf("told twice: %+v", again)
	}

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if !strings.Contains(llmText(req), "Render promo.mp4") {
			t.Errorf("the retried task does not know its goal")
		}
		return say("ویدیو رندر شد 🎬")(req, n)
	}
	h.click(uid, uid, 901, fmt.Sprintf("bg:retry:%d:%d", uid, id))
	h.wantText("ویدیو رندر شد")
	h.wantText("کار پس‌زمینه تموم شد")
}

func TestTooManyBackgroundTasksAreRefused(t *testing.T) {
	h := newHarness(t)
	const uid = 74
	for i := 0; i < maxBgPerUser; i++ {
		if _, err := h.st.AddBgTask(bg, store.BgTask{UserID: uid, ChatID: uid, ChatType: "private", MsgID: int64(10 + i), Title: "x", Goal: "a long job here"}); err != nil {
			t.Fatal(err)
		}
	}
	bgModel(t, h)
	h.send(private(uid, "اپ رو بیلد کن"))
	h.wantText("صبر کن یکیش تموم بشه")
	if tasks, _ := h.st.UserBgTasks(bg, uid, 10); len(tasks) != maxBgPerUser {
		t.Fatalf("a task over the limit was started: %d", len(tasks))
	}
}

// A background task never offers background_task, a poll or a schedule.
func TestABackgroundTaskHasNoBackgroundTool(t *testing.T) {
	h := newHarness(t)
	u := &store.User{ID: 75}
	m := private(75, "x")
	names := func(opt promptOptions) string {
		var out []string
		for _, tool := range h.bot.localTools(u, m, h.bot.newReactor(m), &outbox{}, opt) {
			out = append(out, tool.Def.Function.Name)
		}
		return " " + strings.Join(out, " ") + " "
	}
	if !strings.Contains(names(promptOptions{}), " "+toolBackground+" ") {
		t.Fatal("a request can start a background task")
	}
	inside := names(promptOptions{bg: &bgRun{task: store.BgTask{ID: 1, UserID: 75}}})
	for _, name := range []string{toolBackground, toolAskUser, toolSchedule} {
		if strings.Contains(inside, " "+name+" ") {
			t.Errorf("a background task offers %s", name)
		}
	}
	guest := *m
	guest.GuestQueryID = "g1"
	for _, tool := range h.bot.localTools(u, &guest, h.bot.newReactor(&guest), &outbox{}, promptOptions{}) {
		if tool.Def.Function.Name == toolBackground {
			t.Error("a guest reply offers background tasks")
		}
	}
}

func TestBgBudgetIsLarger(t *testing.T) {
	steps, timeout := bgBudget(40, 10*time.Minute)
	if steps != 120 || timeout != bgMinTimeout {
		t.Fatalf("%d %s", steps, timeout)
	}
	steps, timeout = bgBudget(200, 2*time.Hour)
	if steps != bgMaxSteps || timeout != bgMaxTimeout {
		t.Fatalf("%d %s", steps, timeout)
	}
}
