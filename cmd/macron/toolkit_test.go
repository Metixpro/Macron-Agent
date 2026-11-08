package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func toolNames(req map[string]any) string {
	var names []string
	list, _ := req["tools"].([]any)
	for _, t := range list {
		fn := t.(map[string]any)["function"].(map[string]any)
		names = append(names, fn["name"].(string))
	}
	return "," + strings.Join(names, ",") + ","
}

func TestToolkitInAPrivateChat(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" {
			calls := []any{
				map[string]any{"id": "c1", "type": "function", "function": map[string]any{"name": toolDice, "arguments": `{"emoji":"🎯"}`}},
				map[string]any{"id": "c2", "type": "function", "function": map[string]any{"name": toolEffect, "arguments": `{"effect":"🎉"}`}},
				map[string]any{"id": "c3", "type": "function", "function": map[string]any{"name": toolLocation,
					"arguments": `{"latitude":35.6997,"longitude":51.3380,"title":"برج آزادی","address":"میدان آزادی، تهران"}`}},
				map[string]any{"id": "c4", "type": "function", "function": map[string]any{"name": toolCreatePoll,
					"arguments": `{"question":"پایتخت ایران؟","options":["اصفهان","تهران","تهران","شیراز"],"type":"quiz","correct_option":2,"explanation":"تهران از ۱۱۷۴"}`}},
			}
			return map[string]any{"role": "assistant", "tool_calls": calls}, 0
		}
		return map[string]any{"role": "assistant", "content": "زدی وسط خال! 🎯"}, 0
	}
	h.send(private(71, "یه دارت بنداز و یه سوال بپرس"))

	if names := toolNames(h.llm.last()); !strings.Contains(names, ","+toolEffect+",") || strings.Contains(names, ","+toolChatInfo+",") {
		t.Errorf("private chats get effects, not group info: %s", names)
	}
	if !strings.Contains(llmText(h.llm.last()), "Value 6: bullseye!") {
		t.Error("the model learns where the dart landed")
	}
	var order []string
	for _, c := range h.tg.calls {
		switch c.method {
		case "sendDice", "sendRichMessage", "sendVenue", "sendPoll":
			if rm, ok := c.body["rich_message"].(map[string]any); ok && rm["blocks"] != nil {
				continue
			}
			order = append(order, c.method)
		}
	}
	if strings.Join(order, " ") != "sendDice sendRichMessage sendVenue sendPoll" {
		t.Errorf("the die first, then the answer, then the pin and the quiz: %v", order)
	}
	answer := lastRichAnswer(h)
	if answer.body["message_effect_id"] != telegram.MessageEffects["🎉"] {
		t.Errorf("the answer plays the effect: %v", answer.body["message_effect_id"])
	}
	venue := lastOf(h, "sendVenue")
	if venue.body["title"] != "برج آزادی" || venue.body["latitude"].(float64) != 35.6997 {
		t.Errorf("venue: %v", venue.body)
	}
	quiz := lastOf(h, "sendPoll")
	opts := fmt.Sprint(quiz.body["options"])
	if quiz.body["type"] != "quiz" || quiz.body["correct_option_id"].(float64) != 1 || strings.Count(opts, "تهران") != 1 {
		t.Errorf("quiz (duplicates removed, the right answer still right): %v", quiz.body)
	}
}

func TestEffectRefusedStillSendsTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.tg.setReject(func(method string, body map[string]any) string {
		if body["message_effect_id"] != nil {
			return "Bad Request: EFFECT_ID_INVALID"
		}
		return ""
	})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" {
			return toolCall(toolEffect, `{"effect":"❤️"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "تولدت مبارک!"}, 0
	}
	h.send(private(72, "امروز تولدمه"))
	rich := richAnswers(h)
	last := rich[len(rich)-1]
	if last.body["message_effect_id"] != nil || !strings.Contains(h.tg.allText(), "تولدت مبارک!") {
		t.Errorf("the answer goes without the effect: %v", last.body)
	}
}

func TestToolkitInGroupsAndGuestReplies(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" && strings.Contains(llmText(req), "چند نفریم") {
			return toolCall(toolChatInfo, `{}`), 0
		}
		return map[string]any{"role": "assistant", "content": "۵۷ نفریم."}, 0
	}
	h.send(group(73, "مکرون چند نفریم؟"))
	names := toolNames(h.llm.last())
	if !strings.Contains(names, ","+toolChatInfo+",") || strings.Contains(names, ","+toolEffect+",") || !strings.Contains(names, ","+toolDice+",") {
		t.Errorf("groups: %s", names)
	}
	if !strings.Contains(llmText(h.llm.last()), "members: 57") {
		t.Error("chat_info reports the member count")
	}

	h.bot.dispatch(telegram.Update{UpdateID: 2, GuestMessage: guestMsg(74, "@WazeBot یه تاس بنداز")})
	h.bot.wg.Wait()
	names = toolNames(h.llm.last())
	for _, n := range []string{toolDice, toolLocation, toolCreatePoll, toolEffect, toolChatInfo, toolSendFile} {
		if strings.Contains(names, ","+n+",") {
			t.Errorf("a guest reply is one message; %s is not offered: %s", n, names)
		}
	}
}

func TestDiceMeaning(t *testing.T) {
	for _, c := range []struct {
		emoji string
		v     int
		want  string
	}{
		{"🎰", 64, "JACKPOT 777"}, {"🎰", 1, "three of a kind"}, {"🎰", 2, "no win"}, {"🏀", 5, "scored"}, {"⚽", 2, "missed"},
		{"🎳", 6, "strike"}, {"🎯", 1, "missed the board"}, {"🎲", 3, "rolled 3"},
	} {
		if got := diceMeaning(c.emoji, c.v); !strings.Contains(got, c.want) {
			t.Errorf("%s %d: %q", c.emoji, c.v, got)
		}
	}
}
