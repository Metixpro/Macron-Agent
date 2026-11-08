package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// In privacy mode a group where the bot is not an admin never sends it
// «مکرون …»: when a message does reach it there (a mention), the group's
// admins hear how to fix that, once in a while, and so do the owners.
func TestGroupWhereTheBotSeesOnlyMentions(t *testing.T) {
	h := newHarness(t)
	h.bot.readsAll.Store(false)
	sentTo := func(chat int64, sub string) int {
		n := 0
		for _, c := range h.tg.of("sendMessage") {
			if int64(c.body["chat_id"].(float64)) == chat && strings.Contains(fmt.Sprint(c.body["text"]), sub) {
				n++
			}
		}
		return n
	}
	waitFor := func(chat int64, sub string) bool {
		for i := 0; i < 100; i++ {
			if sentTo(chat, sub) > 0 {
				return true
			}
			time.Sleep(20 * time.Millisecond)
		}
		return false
	}

	m := group(40, "@WazeBot سلام")
	h.send(m)
	if !waitFor(m.Chat.ID, "یه نکته برای ادمین‌های گروه") || !waitFor(ownerID, "حالت Privacy ربات روشنه") {
		t.Fatalf("the group's admins and the owner are told: %v", h.tg.texts())
	}
	h.tg.reset()
	h.send(group(41, "@WazeBot یه سؤال دیگه"))
	time.Sleep(150 * time.Millisecond)
	if sentTo(m.Chat.ID, "یه نکته برای ادمین‌های گروه") != 0 || sentTo(ownerID, "Privacy") != 0 {
		t.Error("once in a while, not on every message")
	}

	// A group where the bot is an admin sees everything: nothing to say.
	admin := group(40, "@WazeBot سلام")
	admin.Chat.ID = -100600
	h.tg.mu.Lock()
	h.tg.members[fmt.Sprintf("%d:%d", admin.Chat.ID, h.bot.me.ID)] = "administrator"
	h.tg.mu.Unlock()
	h.tg.reset()
	h.send(admin)
	time.Sleep(150 * time.Millisecond)
	if sentTo(admin.Chat.ID, "یه نکته") != 0 {
		t.Error("an admin bot sees all")
	}

	// Added to a group as a member: the hello says it from the start.
	_ = h.st.SetSetting(bg, store.SettingGroupApproval, "false")
	chat := telegram.Chat{ID: -100700, Type: "supergroup", Title: "گروه تازه"}
	h.tg.reset()
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: chat, From: telegram.User{ID: 555, FirstName: "Reza"},
		OldChatMember: telegram.ChatMember{Status: "left"}, NewChatMember: telegram.ChatMember{Status: "member"}})
	if sentTo(chat.ID, "ادمین گروه") == 0 {
		t.Errorf("the hello tells them: %v", h.tg.texts())
	}

	// The admin panel says it too.
	h.tg.reset()
	h.send(private(ownerID, "/admin"))
	h.wantText("حالت Privacy روشنه")
}
