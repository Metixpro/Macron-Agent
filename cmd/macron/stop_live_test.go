package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

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
	stopLiveMessage(h, uid)
	close(release)
	<-done
	h.noText("the late answer")
	h.wantText("متوقفش کردم")
	notice := lastTextDelivery(h)
	if !hasButton(buttons(notice), fmt.Sprintf("run:go:%d", uid)) {
		t.Fatalf("no ▶️ continue button: %v", buttons(notice))
	}

	h.llm.reply = say("بقیه‌ش رو هم انجام دادم ✅")
	h.click(uid, uid, 700, fmt.Sprintf("run:go:%d", uid))
	h.wantText("بقیه‌ش رو هم انجام دادم")
	last := llmText(h.llm.last())
	if !strings.Contains(last, "work log before stopping") || !strings.Contains(last, "list_files") || !strings.Contains(last, "مرتب کن") {
		t.Fatalf("the continued job does not know the stopped one: %s", last)
	}

	h.tg.reset()
	h.click(4444, uid, 700, fmt.Sprintf("run:go:%d", uid))
	if h.llm.count() > 4 {
		t.Fatal("another user's press continued the job")
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
		time.Sleep(600 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "گزارش آماده‌ست ✅"}, 0
	}
	h.send(private(uid, "فایل‌هامو پیدا کن و یه گزارش بنویس"))
	h.wantText("گزارش آماده‌ست")
	found := false
	for _, md := range liveFrames(h) {
		if strings.Contains(md, "طرح کار · ۰/۲") && strings.Contains(md, "☐ ⏳ پیدا کردن فایل‌ها") && strings.Contains(md, "☐ نوشتن گزارش") {
			found = true
		}
	}
	if !found {
		for _, frame := range liveFrames(h) {
			t.Logf("frame: %s", frame)
		}
		t.Fatal("the plan is not in the live card")
	}
	h.noText("update_plan")
}

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
