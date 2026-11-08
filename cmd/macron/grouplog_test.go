package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const logChat = -1001234567

type groupChat struct {
	h  *harness
	id int64
}

func (g *groupChat) msg(user int64, name, text string, replyTo int64) *telegram.Message {
	g.id++
	m := &telegram.Message{MessageID: g.id, From: &telegram.User{ID: user, FirstName: name, Username: strings.ToLower(name)},
		Chat: telegram.Chat{ID: logChat, Type: "supergroup", Title: "بچه‌های Go"}, Text: text, Date: time.Now().Unix()}
	if replyTo != 0 {
		m.ReplyToMessage = &telegram.Message{MessageID: replyTo, Chat: m.Chat}
	}
	return m
}

func (g *groupChat) say(user int64, name, text string, replyTo int64) int64 {
	m := g.msg(user, name, text, replyTo)
	g.h.send(m)
	return m.MessageID
}

func TestGroupCatchUp(t *testing.T) {
	h := newHarness(t)
	h.bot.readsAll.Store(false) // privacy mode on: the panel warns while the bot is no admin
	ctx := context.Background()
	g := &groupChat{h: h, id: 1000}
	const ali, sara, reza = 91, 92, 93
	h.tg.mu.Lock()
	h.tg.members[fmt.Sprintf("%d:%d", logChat, ali)] = "administrator"
	h.tg.mu.Unlock()

	// Off by default: nothing is kept, /recap explains how to turn it on.
	g.say(sara, "Sara", "سلام بچه‌ها", 0)
	if n, _, _ := h.st.GroupLogStats(ctx, logChat); n != 0 {
		t.Fatal("nothing is kept before an admin turns it on")
	}
	h.tg.reset()
	h.send(g.msg(sara, "Sara", "/recap", 0))
	h.wantText("/groupsettings")

	// Only group admins open and use the settings.
	h.send(g.msg(sara, "Sara", "/groupsettings", 0))
	h.wantText("فقط ادمین‌های گروه")
	h.tg.reset()
	h.send(g.msg(ali, "Ali", "/groupsettings", 0))
	panel := lastOf(h, "sendMessage")
	if !strings.Contains(panel.body["text"].(string), "Privacy") || !hasButton(buttons(panel), fmt.Sprintf("gl:%d:on:48", logChat)) {
		t.Errorf("panel (with the privacy warning): %s %v", panel.body["text"], buttons(panel))
	}
	h.click(sara, logChat, 900, fmt.Sprintf("gl:%d:on:48", logChat))
	if s, _, _ := h.st.GroupLogSettings(ctx, logChat); s != 0 {
		t.Fatal("a member cannot turn it on")
	}
	h.tg.reset()
	h.click(ali, logChat, 900, fmt.Sprintf("gl:%d:on:48", logChat))
	if s, _, _ := h.st.GroupLogSettings(ctx, logChat); s != 48 {
		t.Fatal("an admin turns it on")
	}
	h.wantText("روشن شد")

	// Messages are kept from now on; /nolog keeps someone out.
	mine := g.say(ali, "Ali", "من میرم ناهار، جلسه رو بندازید عصر", 0)
	g.say(sara, "Sara", "باشه، جلسه ساعت ۵", mine)
	g.say(reza, "Reza", "/nolog", 0)
	g.say(reza, "Reza", "این پیام نباید نگه داشته بشه", 0)
	budget := g.say(sara, "Sara", "@ali بودجه هم تایید شد: ۴۰ میلیون", 0)
	before, _ := h.st.GroupMessages(ctx, logChat, time.Time{}, 0, 100)
	for _, m := range before {
		if m.UserID == reza || strings.HasPrefix(m.Text, "/") {
			t.Errorf("kept something it should not: %+v", m)
		}
	}
	if len(before) != 3 {
		t.Fatalf("kept: %+v", before)
	}

	// "مکرون چی از دستم رفت؟" reads what came after Ali's last message.
	var toolResult string
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" {
			if !strings.Contains(toolNames(req), ","+toolGroupHistory+",") {
				return map[string]any{"role": "assistant", "content": "no tool"}, 0
			}
			return toolCall(toolGroupHistory, `{}`), 0
		}
		toolResult = llmText(req)
		return map[string]any{"role": "assistant", "content": "جلسه افتاد ساعت ۵ و بودجه تایید شد."}, 0
	}
	h.send(g.msg(ali, "Ali", "مکرون چی از دستم رفت؟", 0))
	for _, want := range []string{"since the asker's last message", "2 messages", "جلسه ساعت ۵ (↩ Ali #" + fmt.Sprint(mine) + ") ← TO THE ASKER",
		"@ali بودجه هم تایید شد", "← TO THE ASKER", "https://t.me/c/1234567/"} {
		if !strings.Contains(toolResult, want) {
			t.Errorf("history misses %q:\n%s", want, toolResult)
		}
	}
	if strings.Contains(toolResult, "من میرم ناهار") {
		t.Error("the asker's own earlier message is not news")
	}

	// An edit is reflected; leaving the group forgets everything.
	edit := g.msg(sara, "Sara", "@ali بودجه هم تایید شد: ۵۰ میلیون", 0)
	edit.MessageID = budget
	h.bot.handleEdit(edit)
	msgs, _ := h.st.GroupMessages(ctx, logChat, time.Time{}, 0, 100)
	found := false
	for _, m := range msgs {
		found = found || strings.Contains(m.Text, "۵۰ میلیون (edited)")
	}
	if !found {
		t.Error("edits update the kept text")
	}
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: telegram.Chat{ID: logChat, Type: "supergroup", Title: "x"},
		From:          telegram.User{ID: ali},
		OldChatMember: telegram.ChatMember{Status: "member"}, NewChatMember: telegram.ChatMember{Status: "kicked"}})
	if n, _, _ := h.st.GroupLogStats(ctx, logChat); n != 0 {
		t.Error("removing the bot deletes the kept messages")
	}
	if s, _, _ := h.st.GroupLogSettings(ctx, logChat); s != 0 {
		t.Error("and the settings")
	}
}

func TestLongHistoryIsSummarizedInParts(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetChatPref(ctx, logChat, "log_hours", "24")
	g := &groupChat{h: h, id: 5000}
	for i := 0; i < 300; i++ {
		g.say(int64(100+i%5), fmt.Sprintf("User%d", i%5), fmt.Sprintf("پیام شماره %d درباره پروژه و برنامه‌ریزی هفته آینده و کارهای باقی‌مونده", i), 0)
	}
	var toolResult string
	summaries := 0
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		text := llmText(req)
		if strings.Contains(text, "Summarize this part of the Telegram group") {
			h.llm.mu.Lock()
			summaries++
			h.llm.mu.Unlock()
			return map[string]any{"role": "assistant", "content": "- همه درباره پروژه حرف زدن"}, 0
		}
		if lastRole(req) == "user" {
			return toolCall(toolGroupHistory, `{"since":"all"}`), 0
		}
		toolResult = text
		return map[string]any{"role": "assistant", "content": "خلاصه"}, 0
	}
	h.send(g.msg(777, "New", "مکرون خلاصه گروه رو بگو", 0))
	if summaries < 2 || summaries > historyMaxParts {
		t.Errorf("%d part summaries", summaries)
	}
	for _, want := range []string{"300 messages", "Part 1 (", "همه درباره پروژه حرف زدن", "The latest messages, word for word", "پیام شماره 299"} {
		if !strings.Contains(toolResult, want) {
			t.Errorf("result misses %q", want)
		}
	}
}

func TestPurgeExpiredGroupMessages(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetChatPref(ctx, logChat, "log_hours", "24")
	g := &groupChat{h: h, id: 9000}
	old := g.msg(1, "A", "قدیمی", 0)
	old.Date = time.Now().Add(-30 * time.Hour).Unix()
	h.send(old)
	g.say(1, "A", "تازه", 0)
	if n, _ := h.st.PurgeGroupMessages(ctx); n != 1 {
		t.Errorf("purged %d", n)
	}
	if n, _, _ := h.st.GroupLogStats(ctx, logChat); n != 1 {
		t.Errorf("left %d", n)
	}
}
