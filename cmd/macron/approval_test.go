package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func addedBy(h *harness, chatID, by int64) {
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: telegram.Chat{ID: chatID, Type: "supergroup", Title: "گروه تست"},
		From: telegram.User{ID: by, FirstName: "Adder"}, OldChatMember: telegram.ChatMember{Status: "left"},
		NewChatMember: telegram.ChatMember{Status: "member"}})
	h.bot.wg.Wait()
}

func groupMsgIn(chatID, user int64, text string) *telegram.Message {
	m := private(user, text)
	m.Chat = telegram.Chat{ID: chatID, Type: "supergroup", Title: "گروه تست"}
	return m
}

func TestGroupsWaitForApproval(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const g1, g2, g3, g4 = -100801, -100802, -100803, -100804

	// Someone adds the bot: the owners are asked, the group is told to wait.
	addedBy(h, g1, 55)
	if state, _, _ := h.st.GroupApproval(ctx, g1); state != store.ApprovalPending {
		t.Fatalf("state: %s", state)
	}
	var notice tgCall
	for _, c := range h.tg.of("sendMessage") {
		if int64(c.body["chat_id"].(float64)) == ownerID {
			notice = c
		}
	}
	if notice.body == nil || !strings.Contains(notice.body["text"].(string), "منتظر تأیید") ||
		!hasButton(buttons(notice), fmt.Sprintf("ga:%d:ok", g1)) || !hasButton(buttons(notice), fmt.Sprintf("ga:%d:no", g1)) {
		t.Fatalf("notice: %v", notice.body)
	}
	h.wantText("مدیر ربات باید تأیید کنه")

	// Silent until approved.
	before := len(h.llm.requests)
	h.send(groupMsgIn(g1, 55, "مکرون سلام"))
	if len(h.llm.requests) != before {
		t.Fatal("a pending group gets no answers")
	}
	h.click(55, ownerID, 900, fmt.Sprintf("ga:%d:ok", g1))
	if state, _, _ := h.st.GroupApproval(ctx, g1); state != store.ApprovalPending {
		t.Fatal("only the bot's admins decide")
	}
	h.tg.reset()
	h.click(ownerID, ownerID, 900, fmt.Sprintf("ga:%d:ok", g1))
	if state, _, _ := h.st.GroupApproval(ctx, g1); state != store.ApprovalApproved {
		t.Fatal("approved")
	}
	h.wantText("سلام به همگی") // the usual introduction
	h.send(groupMsgIn(g1, 55, "مکرون سلام"))
	if len(h.llm.requests) == before {
		t.Error("an approved group gets answers")
	}
	h.tg.reset()
	h.click(ownerID, ownerID, 900, fmt.Sprintf("ga:%d:no", g1))
	if strings.Contains(fmt.Sprint(h.tg.of("leaveChat")), fmt.Sprint(g1)) {
		t.Error("a decision is final")
	}

	// Rejected: the bot says goodbye and leaves.
	addedBy(h, g2, 56)
	h.tg.reset()
	h.click(ownerID, ownerID, 900, fmt.Sprintf("ga:%d:no", g2))
	if l := h.tg.of("leaveChat"); len(l) != 1 || int64(l[0].body["chat_id"].(float64)) != g2 {
		t.Fatalf("leave: %v", l)
	}
	h.wantText("خداحافظ")

	// The bot's own admins need no approval; neither does anyone when it is off.
	addedBy(h, g4, ownerID)
	if state, _, _ := h.st.GroupApproval(ctx, g4); state != store.ApprovalApproved {
		t.Error("an admin's group is approved at once")
	}
	_ = h.st.SetSetting(ctx, store.SettingGroupApproval, "false")
	addedBy(h, -100805, 58)
	if state, _, _ := h.st.GroupApproval(ctx, -100805); state != store.ApprovalApproved {
		t.Error("approvals turned off")
	}
}

func TestSoulInThePanel(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 900, "a:soul")
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "پیش‌فرض") || !hasButton(buttons(v), "a:soul:set") {
		t.Fatalf("soul panel: %v", v.body)
	}
	h.click(ownerID, ownerID, 900, "a:soul:get")
	if d := lastOf(h, "sendDocument"); d.body["document_name"] != "SOUL.md" {
		t.Errorf("download: %v", d.body)
	}
	h.click(ownerID, ownerID, 900, "a:soul:set")
	h.send(private(ownerID, "تو {{name_fa}} ({{name}}) هستی؛ یه دزد دریایی شوخ‌طبع 🏴‍☠️"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		return map[string]any{"role": "assistant", "content": "آهوی!"}, 0
	}
	h.send(private(77, "سلام"))
	p := llmText(h.llm.last())
	if !strings.Contains(p, "تو مکرون (Macron Ai) هستی؛ یه دزد دریایی شوخ‌طبع") || !strings.Contains(p, "technical details are not public") {
		t.Error("the new soul is used and the ground rules stay")
	}
	h.click(ownerID, ownerID, 900, "a:soul:reset")
	h.send(private(77, "سلام"))
	if !strings.Contains(llmText(h.llm.last()), "خط قرمزهام") {
		t.Error("back to the default soul")
	}
}
