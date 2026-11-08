package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// blockingJob starts a job that lists the workspace, then waits in its
// second model call until release is closed.
func blockingJob(t *testing.T, h *harness, uid int64) (started, release, done chan struct{}) {
	t.Helper()
	started, release, done = make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once bool
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("list_files", `{}`), 0
		}
		if !once {
			once = true
			close(started)
			<-release
		}
		return map[string]any{"role": "assistant", "content": "the late answer"}, 0
	}
	go func() {
		h.send(private(uid, "همه فایل‌هامو مرتب کن و یه گزارش بده"))
		close(done)
	}()
	return started, release, done
}

func TestAnOldDraftNeverStopsTheJob(t *testing.T) {
	h := newHarness(t)
	started, release, done := blockingJob(t, h, 31)
	<-started
	// A stop for a draft this chat no longer shows (an earlier answer's): ignored.
	h.bot.handleStop(&telegram.MessageGenerationStopped{Chat: telegram.Chat{ID: 31, Type: "private"}, DraftID: 987654})
	close(release)
	<-done
	h.wantText("the late answer")
	h.noText("ولش کردم")
	h.noText("متوقفش کردم")
}

func TestAStoppedJobKeepsItsWorkAndGoesOn(t *testing.T) {
	h := newHarness(t)
	const uid = 32
	started, release, done := blockingJob(t, h, uid)
	<-started
	var draftID int64
	h.bot.drafts.Range(func(k, v any) bool {
		if v.(draftOwner).userID == uid {
			draftID = k.(int64)
		}
		return true
	})
	if draftID == 0 {
		t.Fatal("the job's draft is not registered")
	}
	h.bot.handleStop(&telegram.MessageGenerationStopped{Chat: telegram.Chat{ID: uid, Type: "private"}, DraftID: telegram.FlexInt64(draftID)})
	close(release)
	<-done
	h.noText("the late answer")
	h.wantText("متوقفش کردم")
	notice := lastOf(h, "sendMessage")
	if !hasButton(buttons(notice), fmt.Sprintf("run:go:%d", uid)) {
		t.Fatalf("no ▶️ continue button: %v", buttons(notice))
	}

	// ▶️: the job goes on, knowing what it asked and what it had done.
	h.llm.reply = say("بقیه‌ش رو هم انجام دادم ✅")
	h.click(uid, uid, 700, fmt.Sprintf("run:go:%d", uid))
	h.wantText("بقیه‌ش رو هم انجام دادم")
	last := llmText(h.llm.last())
	if !strings.Contains(last, "work log before stopping") || !strings.Contains(last, "list_files") || !strings.Contains(last, "مرتب کن") {
		t.Fatalf("the continued job does not know the stopped one: %s", last)
	}
	// Someone else's button does nothing.
	h.tg.reset()
	h.click(4444, uid, 700, fmt.Sprintf("run:go:%d", uid))
	if h.llm.count() > 4 {
		t.Fatal("another user's press continued the job")
	}
}

func TestLiveMessageModeKeepsTheInputFree(t *testing.T) {
	h := newHarness(t)
	const uid = 33
	if err := h.st.SetUserPref(bg, uid, store.PrefLive, liveMsg); err != nil {
		t.Fatal(err)
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("list_files", `{}`), 0
		}
		return map[string]any{"role": "assistant", "content": "فایل‌هات مرتبه ✅"}, 0
	}
	h.send(private(uid, "فایل‌هامو چک کن"))
	h.wantText("فایل‌هات مرتبه")
	if n := len(h.tg.of("sendRichMessageDraft")); n != 0 {
		t.Fatalf("the live message mode sent %d drafts (they lock the input on Android)", n)
	}
	rich := h.tg.of("sendRichMessage")
	if len(rich) == 0 {
		t.Fatal("no live message")
	}
	reply, _ := rich[0].body["reply_parameters"].(map[string]any)
	if reply == nil || reply["message_id"] != float64(77) {
		t.Fatalf("the live message must reply to the request: %v", rich[0].body)
	}
	answered := false
	for _, e := range h.tg.of("editMessageText") {
		if strings.Contains(fmt.Sprint(e.body["rich_message"]), "فایل‌هات مرتبه") && e.body["message_id"] != nil {
			answered = true
		}
	}
	if !answered || len(rich) != 1 {
		t.Fatalf("the live message must become the answer (edited, %d rich messages sent)", len(rich))
	}
	actions := h.tg.of("sendChatAction")
	if len(actions) == 0 || actions[0].body["action"] != "typing" {
		t.Fatal("no typing… dots")
	}
}

func TestModePanelChoosesTheLiveView(t *testing.T) {
	h := newHarness(t)
	const uid = 34
	h.send(private(uid, "/mode"))
	h.wantText("نمایش کار توی پیوی")
	panel := lastOf(h, "sendMessage")
	for _, v := range liveModes {
		if !hasButton(buttons(panel), "mode:live:"+v) {
			t.Fatalf("no %s button: %v", v, buttons(panel))
		}
	}
	h.click(uid, uid, 900, "mode:live:"+liveDots)
	if v, _ := h.st.UserPref(bg, uid, store.PrefLive); v != liveDots {
		t.Fatalf("stored %q", v)
	}
	h.tg.reset()
	h.llm.reply = say("سلام 👋")
	h.send(private(uid, "یه سوال: پایتخت فرانسه کجاست؟"))
	drafts := h.tg.of("sendRichMessageDraft")
	if len(drafts) == 0 {
		t.Fatal("the dots mode shows a draft")
	}
	for _, d := range drafts {
		if d.body["can_stop"] == true {
			t.Fatal("the dots mode has no stop button")
		}
	}
	h.click(uid, uid, 900, "mode:live:bogus")
	if v, _ := h.st.UserPref(bg, uid, store.PrefLive); v != liveDots {
		t.Fatalf("an unknown mode was stored: %q", v)
	}
}

func TestThePlanShowsInTheLiveCard(t *testing.T) {
	h := newHarness(t)
	const uid = 35
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "p1", "type": "function", "function": map[string]any{"name": "update_plan",
					"arguments": `{"items":[{"text":"پیدا کردن فایل‌ها","status":"in_progress"},{"text":"نوشتن گزارش","status":"pending"}]}`}},
				map[string]any{"id": "l1", "type": "function", "function": map[string]any{"name": "list_files", "arguments": `{}`}},
			}}, 0
		}
		time.Sleep(600 * time.Millisecond) // the card refreshes meanwhile
		return map[string]any{"role": "assistant", "content": "گزارش آماده‌ست ✅"}, 0
	}
	h.send(private(uid, "فایل‌هامو پیدا کن و یه گزارش بنویس"))
	h.wantText("گزارش آماده‌ست")
	found := false
	for _, d := range h.tg.of("sendRichMessageDraft") {
		md := fmt.Sprint(d.body["rich_message"])
		if strings.Contains(md, "📋 `0/2`") && strings.Contains(md, "- [ ] ⏳ **پیدا کردن فایل‌ها**") && strings.Contains(md, "- [ ] نوشتن گزارش") {
			found = true
		}
	}
	if !found {
		for _, d := range h.tg.of("sendRichMessageDraft") {
			t.Logf("draft: %v", d.body["rich_message"])
		}
		t.Fatal("the plan is not in the live card")
	}
	h.noText("update_plan")
}

// TestTheNextRequestKnowsWhatWasDone: a follow-up sees the steps behind the
// last answer and the files in the workspace, so «همون فایل» needs no search.
func TestTheNextRequestKnowsWhatWasDone(t *testing.T) {
	h := newHarness(t)
	const uid = 36
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("write_file", `{"filename":"poem.txt","content":"یه شعر کوتاه"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "شعرت آماده‌ست ✍️"}, 0
	}
	h.send(private(uid, "یه شعر کوتاه بنویس و ذخیره کن"))
	h.wantText("شعرت آماده‌ست")
	h.llm.reply = say("باشه، PDF‌ش می‌کنم")
	h.send(private(uid, "همونو PDF کن"))
	last := llmText(h.llm.last())
	if !strings.Contains(last, `\u003cwork_notes\u003e`) || !strings.Contains(last, "write_file") || !strings.Contains(last, "poem.txt") {
		t.Fatalf("the follow-up does not see what was done: %s", last)
	}
	if !strings.Contains(last, "Files in the user's workspace now") || strings.Contains(last, "In the workspace now:") {
		t.Fatalf("the follow-up does not see the workspace: %s", last)
	}
	h.noText("work_notes")
}
