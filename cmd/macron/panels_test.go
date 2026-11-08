package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func groupClick(h *harness, userID int64, msg *telegram.Message, data string) {
	h.bot.handleCallback(&telegram.CallbackQuery{ID: "q", From: telegram.User{ID: userID}, Data: data, Message: msg})
	h.bot.wg.Wait()
}

// panelIn finds the message id of the panel the bot noted in a chat.
func panelIn(h *harness, chatID int64) int64 {
	h.t.Helper()
	var id int64
	h.bot.panels.Range(func(k, _ any) bool {
		var chat, msg int64
		if _, err := fmt.Sscanf(k.(string), "%d:%d", &chat, &msg); err == nil && chat == chatID {
			id = msg
		}
		return true
	})
	if id == 0 {
		h.t.Fatal("no panel noted in the group")
	}
	return id
}

func TestGroupPanelsAnswerOnlyTheirOwner(t *testing.T) {
	h := newHarness(t)
	const owner, other = 21, 22
	chat := telegram.Chat{ID: -100500, Type: "supergroup", Title: "بچه‌های Go"}

	h.send(group(owner, "/mode"))
	h.wantText("حالت پاسخ")
	panel := &telegram.Message{MessageID: panelIn(h, chat.ID), Chat: chat}

	edits := len(h.tg.of("editMessageText"))
	groupClick(h, other, panel, "mode:set:fast")
	if a := lastOf(h, "answerCallbackQuery"); !strings.Contains(fmt.Sprint(a.body["text"]), "مال کسیه که دستور رو زده") || a.body["show_alert"] != true {
		t.Fatalf("someone else is told the panel is not theirs: %v", a.body)
	}
	if h.bot.fastMode(bg, other) || h.bot.fastMode(bg, owner) || len(h.tg.of("editMessageText")) != edits {
		t.Fatal("a press on someone else's panel changes nothing")
	}

	groupClick(h, owner, panel, "mode:set:fast")
	if !h.bot.fastMode(bg, owner) {
		t.Fatal("the owner uses their own panel")
	}

	// After a restart the registry is empty: the command the panel replies to names the owner.
	h.bot.panels.Delete(panelKey(chat.ID, panel.MessageID))
	panel.ReplyToMessage = &telegram.Message{MessageID: 77, From: &telegram.User{ID: owner}, Chat: chat}
	groupClick(h, other, panel, "mode:set:deep")
	if !h.bot.fastMode(bg, owner) || h.bot.fastMode(bg, other) {
		t.Fatal("the reply names the owner after a restart")
	}
	groupClick(h, owner, panel, "mode:set:deep")
	if h.bot.fastMode(bg, owner) {
		t.Fatal("the owner still uses it after a restart")
	}

	// Others' panels in private chats and buttons meant for everyone are untouched.
	if (&Bot{}).foreignPanel(&telegram.CallbackQuery{From: telegram.User{ID: other}, Data: "mode:set:fast",
		Message: &telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: other, Type: "private"}}}) {
		t.Error("private chats have no owners to check")
	}
	if h.bot.foreignPanel(&telegram.CallbackQuery{From: telegram.User{ID: other}, Data: "fj:21", Message: panel}) {
		t.Error("buttons that check their own owner are left alone")
	}

	// The reminders panel is locked the same way.
	h.send(group(owner, "/reminders"))
	list := &telegram.Message{MessageID: panelIn(h, chat.ID), Chat: chat}
	if !h.bot.foreignPanel(&telegram.CallbackQuery{From: telegram.User{ID: other}, Data: "sc:list", Message: list}) {
		t.Error("the reminders panel belongs to whoever opened it")
	}
}
