package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestFeedbackAndTheWeeklyReport(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if strings.Contains(llmText(req), "You review a job") {
			return map[string]any{"role": "assistant", "content": `{"none": true}`}, 0
		}
		switch toolResults(req) {
		case 0:
			return toolCall("read_file", `{"filename":"x.txt"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "اینم جواب"}, 0
	}
	h.send(private(90, "فایلم رو بخون"))
	sent := h.tg.of("sendRichMessage")
	if len(sent) == 0 {
		t.Fatal("an answer")
	}
	h.bot.fb.mu.Lock()
	ids := h.bot.fb.sent[90]
	h.bot.fb.mu.Unlock()
	if len(ids) == 0 {
		t.Fatal("the bot's messages are noted as they are sent")
	}
	answerID := ids[len(ids)-1]

	react := func(msgID int64, emoji string) {
		h.bot.handleReaction(&telegram.MessageReactionUpdated{Chat: telegram.Chat{ID: 90, Type: "private"}, MessageID: msgID,
			User: &telegram.User{ID: 90}, NewReaction: []telegram.ReactionType{{Type: "emoji", Emoji: emoji}}})
	}
	react(answerID, "👎")
	react(answerID+1000, "👍")
	h.send(private(90, "اشتباهه"))
	h.send(private(90, "فایلم رو بخون"))
	h.send(private(90, "فایلم رو بخون"))
	h.send(private(90, "ادامه بده"))
	h.send(private(90, "ادامه بده"))

	sums, _ := h.st.DailySums(bg, time.Now().Add(-24*time.Hour))
	got := map[string]int{}
	for _, s := range sums {
		got[s.Kind+"/"+s.Tool] = s.N
	}
	if got["down/"] != 1 || got["up/"] != 0 || got["down/read_file"] != 1 {
		t.Errorf("only the reaction to the answer counts, with its tools: %v", got)
	}
	if got["complaint/"] != 1 || got["repeat/"] != 1 || got["fail/read_file"] < 1 || got["call/read_file"] < 1 || got["job/"] < 3 {
		t.Errorf("complaints, repeats, calls and failures: %v", got)
	}

	report := h.bot.weeklyReport(bg, fa, time.Now().Add(-7*24*time.Hour))
	for _, want := range []string{"گزارش هفتگی", "👎 ۱", "<code>read_file</code>", "آخرین خطا", "اعتراض: ۱"} {
		if !strings.Contains(report, want) {
			t.Errorf("the report shows %q:\n%s", want, report)
		}
	}

	sat := time.Date(2026, 10, 31, 10, 0, 0, 0, h.bot.loc)
	h.tg.reset()
	if h.bot.maybeWeeklyReport(bg, sat.Add(-2*time.Hour)) {
		t.Error("not before 9:00")
	}
	if !h.bot.maybeWeeklyReport(bg, sat) || !strings.Contains(h.tg.allText(), "گزارش هفتگی") {
		t.Fatal("sent on Saturday morning")
	}
	if h.bot.maybeWeeklyReport(bg, sat.Add(time.Hour)) {
		t.Error("once a week")
	}
	_ = h.st.SetSetting(bg, settingWeeklyReport, "false")
	if h.bot.maybeWeeklyReport(bg, sat.Add(7*24*time.Hour)) {
		t.Error("off: never")
	}
	h.click(ownerID, ownerID, 9, "a:learn:report")
	h.wantText("ابزارهایی که بیشتر خطا دادن")
	_ = store.DailyJob
}
