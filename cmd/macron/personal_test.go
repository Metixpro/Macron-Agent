package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func personalHarness(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	h.bot.personal = true
	h.bot.workFiles = 30 * 24 * time.Hour
	return h
}

func (h *harness) dispatch(u telegram.Update) {
	h.bot.dispatch(u)
	h.bot.wg.Wait()
}

func TestPersonalEditionAnswersItsOwnerOnly(t *testing.T) {
	h := personalHarness(t)
	h.llm.reply = say("سلام صاحب‌خونه")

	// A stranger gets nothing: no reply, no model call, no trace of the bot.
	h.dispatch(telegram.Update{UpdateID: 1, Message: private(555, "سلام")})
	h.dispatch(telegram.Update{UpdateID: 2, Message: private(555, "/start")})
	h.dispatch(telegram.Update{UpdateID: 3, Message: private(555, "/memory")})
	h.dispatch(telegram.Update{UpdateID: 4, CallbackQuery: &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: 555}, Data: "a:home",
		Message: &telegram.Message{MessageID: 9, Chat: telegram.Chat{ID: 555, Type: "private"}}}})
	h.dispatch(telegram.Update{UpdateID: 5, EditedMessage: private(555, "edited")})
	if len(h.tg.calls) != 0 || h.llm.count() != 0 {
		t.Fatalf("a stranger was answered: %d telegram calls %d model calls; %v", len(h.tg.calls), h.llm.count(), h.tg.allText())
	}

	// The owner is answered, and the stranger's attempt did not change that.
	h.dispatch(telegram.Update{UpdateID: 6, Message: private(ownerID, "سلام")})
	h.wantText("سلام صاحب‌خونه")
}

func TestPersonalEditionLeavesGroupsStrangersAddItTo(t *testing.T) {
	h := personalHarness(t)
	add := func(by int64) telegram.Update {
		return telegram.Update{UpdateID: by, MyChatMember: &telegram.ChatMemberUpdated{
			Chat: telegram.Chat{ID: -100777, Type: "supergroup", Title: "x"}, From: telegram.User{ID: by},
			NewChatMember: telegram.ChatMember{Status: "member"}}}
	}
	h.dispatch(add(ownerID))
	time.Sleep(200 * time.Millisecond)
	if n := len(h.tg.of("leaveChat")); n != 0 {
		t.Fatalf("the owner adds the bot to a group and it stays (%d leaves)", n)
	}
	h.dispatch(add(555))
	deadline := time.Now().Add(3 * time.Second)
	for len(h.tg.of("leaveChat")) == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if len(h.tg.of("leaveChat")) != 1 {
		t.Fatal("a stranger adds the bot to a group and it leaves")
	}
}

func TestPersonalEditionSpeaksForItself(t *testing.T) {
	h := personalHarness(t)
	h.send(private(ownerID, "/start"))
	for _, want := range []string{"SOUL.md", "USER.md", "MEMORY.md", "/admin"} {
		h.wantText(want)
	}
	h.noText("VIP")
	h.noText("Free")
	h.tg.reset()
	h.send(private(ownerID, "/help"))
	h.wantText("سرور")
	h.noText("/plan")
	h.noText("/invite")

	// The panel of one owner: nothing about users, sales, broadcasts or groups.
	h.tg.reset()
	h.send(private(ownerID, "/admin"))
	panel := lastOf(h, "sendMessage")
	text := panel.body["text"].(string)
	if !strings.Contains(text, "پروایدر فعال") || strings.Contains(text, "کاربران") {
		t.Errorf("panel: %s", text)
	}
	btns := strings.Join(buttons(panel), "|")
	for _, banned := range []string{"a:shop", "a:bc", "a:users", "a:gr"} {
		if strings.Contains(btns, banned) {
			t.Errorf("the personal panel offers %s: %s", banned, btns)
		}
	}
	for _, want := range []string{"a:pv", "a:ai", "a:learn", "a:st"} {
		if !strings.Contains(btns, want) {
			t.Errorf("the personal panel lacks %s: %s", want, btns)
		}
	}

	// Its own command menu.
	cmds := map[string]bool{}
	for _, c := range h.bot.userCommands(fa) {
		cmds[c.Command] = true
	}
	for _, gone := range []string{"plan", "providers", "cookies", "github", "servers", "me"} {
		if cmds[gone] {
			t.Errorf("/%s is in the personal menu", gone)
		}
	}
	if !cmds["memory"] || !cmds["reminders"] || !cmds["tasks"] {
		t.Errorf("menu: %v", cmds)
	}
}

func TestPersonalWorkspaceKeepsFilesForTheConfiguredTime(t *testing.T) {
	h := personalHarness(t)
	if got := h.bot.filesTTL(); got != 30*24*time.Hour {
		t.Errorf("files are kept %v", got)
	}
}
