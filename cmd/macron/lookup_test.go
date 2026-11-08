package main

import (
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestLookupTargets(t *testing.T) {
	sara := &telegram.User{ID: 777000123, FirstName: "Sara", Username: "sara_dev"}
	m := group(40, "آمار اینو دربیار")
	m.ReplyToMessage = &telegram.Message{MessageID: 9, From: sara, Chat: m.Chat, Text: "hi"}
	for target, want := range map[string]int64{"": 777000123, "reply": 777000123, "me": 40, "123456789": 123456789, "id:42": 42,
		"-1001234567": -1001234567, "https://t.me/c/1234567/89": -1001234567} {
		lt, err := resolveTarget(m, target, botID)
		if err != nil || lt.id != want {
			t.Errorf("%q → %+v %v, want id %d", target, lt, err, want)
		}
	}
	for target, want := range map[string]string{"@durov": "durov", "t.me/telegram": "telegram", "https://t.me/s/news_fa/12": "news_fa", "some_user": "some_user"} {
		if lt, err := resolveTarget(m, target, botID); err != nil || lt.username != want {
			t.Errorf("%q → %+v %v, want @%s", target, lt, err, want)
		}
	}
	if _, err := resolveTarget(private(1, "x"), "reply", botID); err == nil {
		t.Error("'reply' without a replied message")
	}
	if _, err := resolveTarget(private(1, "x"), "این یارو", botID); err == nil {
		t.Error("words are not a target")
	}
	fw := private(41, "")
	fw.ForwardOrigin = &telegram.MessageOrigin{Type: "user", SenderUser: &telegram.User{ID: 5555, FirstName: "Reza"}}
	if lt, _ := resolveTarget(fw, "", botID); lt.id != 5555 {
		t.Errorf("a forwarded message's author by default: %+v", lt)
	}

	onBot := group(43, "آمار منو بده")
	onBot.ReplyToMessage = &telegram.Message{MessageID: 10, From: &telegram.User{ID: botID, IsBot: true, Username: "WazeBot"}, Chat: onBot.Chat, Text: "…"}
	if lt, _ := resolveTarget(onBot, "", botID); lt.id != 43 {
		t.Errorf("a reply to the bot looks up the asker: %+v", lt)
	}
}

const botID = 42

func TestLookupTool(t *testing.T) {
	h := newHarness(t)
	h.tg.mu.Lock()
	h.tg.members["-100500:777000123"] = "member"
	h.tg.mu.Unlock()
	var result string
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolLookup, `{"target":"reply"}`), 0
		}
		result = llmText(req)
		return map[string]any{"role": "assistant", "content": "آمارش 👇"}, 0
	}
	m := group(40, "مکرون آمار اینو دربیار")
	m.ReplyToMessage = &telegram.Message{MessageID: 9, From: &telegram.User{ID: 777000123, FirstName: "Sara", IsPremium: true}, Chat: m.Chat, Text: "hi"}
	h.send(m)
	for _, want := range []string{"id: 777000123", "name: Ali", "bio: عاشق Go", "telegram premium: yes", "profile photos: 1",
		"account made: about", "in this group: member"} {
		if !strings.Contains(result, want) {
			t.Errorf("lookup result misses %q:\n%s", want, result)
		}
	}
	if strings.Contains(result, "this bot's records") {
		t.Error("no records for someone the bot does not know")
	}

	h.send(private(55, "سلام"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolLookup, `{"target":"55"}`), 0
		}
		result = llmText(req)
		return map[string]any{"role": "assistant", "content": "ok"}, 0
	}
	h.llm.mu.Lock()
	h.llm.requests = nil
	h.llm.mu.Unlock()
	h.send(private(ownerID, "آمار 55 رو بده"))
	if !strings.Contains(result, "this bot's records") || !strings.Contains(result, "messages to the bot: ") {
		t.Errorf("an admin sees the bot's records:\n%s", result)
	}
	h.llm.mu.Lock()
	h.llm.requests = nil
	h.llm.mu.Unlock()
	h.send(private(56, "آمار 55 رو بده"))
	if strings.Contains(result, "this bot's records") || !strings.Contains(result, "is private") {
		t.Errorf("another member does not:\n%s", result)
	}

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolLookup, `{"target":"@our_channel"}`), 0
		}
		result = llmText(req)
		return map[string]any{"role": "assistant", "content": "ok"}, 0
	}
	h.llm.mu.Lock()
	h.llm.requests = nil
	h.llm.mu.Unlock()
	h.send(private(56, "آمار @our_channel"))
	for _, want := range []string{"type: channel", "title: کانال ما", "members: 57"} {
		if !strings.Contains(result, want) {
			t.Errorf("channel lookup misses %q:\n%s", want, result)
		}
	}
}
