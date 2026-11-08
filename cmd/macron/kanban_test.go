package main

import (
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestKanbanBoard(t *testing.T) {
	h := newHarness(t)
	step := 0
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		text := llmText(req)
		if strings.Contains(text, "card #1 of the owner's board") { // the background task itself
			return map[string]any{"role": "assistant", "content": "گزارش: انجام شد"}, 0
		}
		switch {
		case lastRole(req) == "user" && step == 0:
			step++
			return toolCall(toolKanban, `{"action":"add","title":"مهاجرت دیتابیس","notes":"دیتابیس رو به نسخه جدید ببر و تست کن"}`), 0
		case step == 1:
			step++
			return toolCall(toolKanban, `{"action":"run","id":1}`), 0
		}
		return map[string]any{"role": "assistant", "content": "شروع شد"}, 0
	}
	h.send(private(ownerID, "یه کارت برای مهاجرت دیتابیس بساز و اجراش کن"))
	h.bot.wg.Wait()
	cards, _ := h.st.Cards(bg, ownerID, 7)
	if len(cards) != 1 || cards[0].Title != "مهاجرت دیتابیس" || cards[0].BgTask == 0 {
		t.Fatalf("cards: %+v", cards)
	}
	h.tg.reset()
	h.send(private(ownerID, "/board"))
	h.wantText("[+] تموم شده")
	h.wantText("#1 مهاجرت دیتابیس")
	if c, _ := h.st.Card(bg, ownerID, 1); c.Status != store.CardDone || !strings.Contains(c.Notes, "finished") {
		t.Fatalf("the card follows its task: %+v", c)
	}
	// Someone else has no board.
	h.tg.reset()
	h.send(private(555, "/board"))
	h.noText("تخته کار")
}
