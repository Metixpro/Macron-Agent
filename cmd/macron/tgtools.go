package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"strings"
	"sync"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	toolDice        = "roll_dice"
	toolLocation    = "send_location"
	toolCreatePoll  = "create_poll"
	toolEffect      = "message_effect"
	toolChatInfo    = "chat_info"
	maxExtraPolls   = 5
	maxExtraPlaces  = 5
	maxDicePerReply = 6
)

type extras struct {
	mu     sync.Mutex
	later  []func(ctx context.Context)
	polls  int
	places int
	dice   int
	effect string
}

func (e *extras) add(fn func(ctx context.Context)) {
	e.mu.Lock()
	e.later = append(e.later, fn)
	e.mu.Unlock()
}

func (e *extras) run(ctx context.Context) {
	e.mu.Lock()
	list := e.later
	e.later = nil
	e.mu.Unlock()
	for _, fn := range list {
		if ctx.Err() != nil {
			return
		}
		fn(ctx)
	}
}

func (e *extras) effectID() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.effect
}

func boolArg(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.EqualFold(strings.TrimSpace(x), "true")
	}
	return false
}

func diceMeaning(emoji string, v int) string {
	switch emoji {
	case "🎯":
		if v == 6 {
			return "bullseye!"
		}
		if v == 1 {
			return "missed the board"
		}
		return fmt.Sprintf("hit ring %d of 6 (6 = bullseye)", v)
	case "🏀":
		if v >= 4 {
			return "scored!"
		}
		return "missed"
	case "⚽":
		if v >= 3 {
			return "goal!"
		}
		return "missed"
	case "🎳":
		switch v {
		case 6:
			return "strike! all pins down"
		case 1:
			return "gutter ball, no pins"
		}
		return fmt.Sprintf("knocked down some pins (%d of 6)", v)
	case "🎰":
		symbols := []string{"bar", "grapes", "lemon", "seven"}
		x := v - 1
		reels := []string{symbols[x%4], symbols[x/4%4], symbols[x/16%4]}
		res := "reels: " + strings.Join(reels, " | ")
		if reels[0] == reels[1] && reels[1] == reels[2] {
			if reels[0] == "seven" {
				return res + " — JACKPOT 777!"
			}
			return res + " — three of a kind, a win!"
		}
		return res + " — no win"
	}
	return fmt.Sprintf("rolled %d", v)
}

func (b *Bot) toolkit(m *telegram.Message, ex *extras) []agent.LocalTool {
	chatID := m.Chat.ID
	group := !m.Chat.Private()
	out := []agent.LocalTool{{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolDice,
			Description: "Throw an animated die and get its real value: 🎲 1–6, 🎯 6 bullseye, 🏀 4–5 in, ⚽ 3–5 goal, 🎳 6 strike, " +
				"🎰 64 jackpot. Games, bets, fair picks; then tell the result.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"emoji": map[string]any{"type": "string", "enum": telegram.DiceEmoji},
			}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			emoji, _ := args["emoji"].(string)
			emoji = strings.TrimSpace(strings.ReplaceAll(emoji, "️", ""))
			valid := false
			for _, d := range telegram.DiceEmoji {
				valid = valid || d == emoji
			}
			if !valid {
				emoji = "🎲"
			}
			ex.mu.Lock()
			ex.dice++
			n := ex.dice
			ex.mu.Unlock()
			if n > maxDicePerReply {
				return agent.ToolOutput{Text: "error: that's enough dice for one reply"}, nil
			}
			opts := telegram.SendOptions{}
			if group {
				opts.ReplyTo = m.MessageID
			}
			v, err := b.api.SendDice(ctx, chatID, emoji, opts)
			if err != nil {
				return agent.ToolOutput{Text: "error: the die could not be sent: " + err.Error()}, nil
			}
			return agent.ToolOutput{Text: fmt.Sprintf("%s was thrown in the chat. Value %d: %s. The animation plays for a few seconds; announce the result.", emoji, v, diceMeaning(emoji, v))}, nil
		},
	}, {
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolLocation,
			Description: "A map pin after your answer (a venue card with title and address). Only coordinates you are sure of.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"latitude":  map[string]any{"type": "number"},
				"longitude": map[string]any{"type": "number"},
				"title":     map[string]any{"type": "string"},
				"address":   map[string]any{"type": "string"},
			}, "required": []string{"latitude", "longitude"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			lat, lon := numArg(args["latitude"]), numArg(args["longitude"])
			if math.Abs(lat) > 90 || math.Abs(lon) > 180 || (lat == 0 && lon == 0) {
				return agent.ToolOutput{Text: "error: give a real latitude (-90…90) and longitude (-180…180)"}, nil
			}
			title := textutil.TruncateRunes(strings.TrimSpace(textutil.OneLine(fmt.Sprint(orEmpty(args["title"])))), 100, "…")
			address := textutil.TruncateRunes(strings.TrimSpace(textutil.OneLine(fmt.Sprint(orEmpty(args["address"])))), 200, "…")
			ex.mu.Lock()
			ex.places++
			n := ex.places
			ex.mu.Unlock()
			if n > maxExtraPlaces {
				return agent.ToolOutput{Text: "error: at most 5 places per reply"}, nil
			}
			ex.add(func(ctx context.Context) {
				var err error
				if title != "" {
					if address == "" {
						address = fmt.Sprintf("%.5f, %.5f", lat, lon)
					}
					err = b.api.SendVenue(ctx, chatID, lat, lon, title, address, telegram.SendOptions{})
				} else {
					err = b.api.SendLocation(ctx, chatID, lat, lon, telegram.SendOptions{})
				}
				if err != nil && !telegram.IsUnreachable(err) {
					log.Printf("[chat %d] location: %v", chatID, err)
				}
			})
			return agent.ToolOutput{Text: "The map pin will be sent right after your answer; don't paste the coordinates."}, nil
		},
	}, {
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolCreatePoll,
			Description: "A poll or quiz in the chat after your answer (votes, quiz games, tests); ≤5 per reply. To ask for yourself: ask_user.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "≤300 characters"},
				"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 2, "maxItems": pollMaxOptions,
					"description": "≤100 characters each"},
				"type":             map[string]any{"type": "string", "enum": []string{"regular", "quiz"}},
				"correct_option":   map[string]any{"type": "integer", "description": "quiz: 0-based"},
				"explanation":      map[string]any{"type": "string", "description": "quiz: shown after (≤200)"},
				"multiple_answers": map[string]any{"type": "boolean"},
				"anonymous":        map[string]any{"type": "boolean", "description": "Default true"},
			}, "required": []string{"question", "options"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			raw, _ := args["options"].([]any)
			var original []string
			for _, o := range raw {
				original = append(original, strings.TrimSpace(fmt.Sprint(o)))
			}
			q, err := parsePollArgs(map[string]any{"question": args["question"], "options": raw, "multiple": boolArg(args["multiple_answers"])})
			if err != nil {
				return agent.ToolOutput{Text: "error: " + err.Error()}, nil
			}
			quiz := strings.EqualFold(fmt.Sprint(args["type"]), "quiz")
			correct := -1
			if quiz {
				i := int(numArg(args["correct_option"]))
				if i < 0 || i >= len(original) {
					return agent.ToolOutput{Text: "error: correct_option must be the index (from 0) of one of the options"}, nil
				}
				want := textutil.TruncateRunes(strings.TrimSpace(textutil.OneLine(original[i])), pollOptionLen, "…")
				for j, o := range q.Options {
					if strings.EqualFold(o, want) {
						correct = j
					}
				}
				if correct < 0 {
					return agent.ToolOutput{Text: "error: the correct option is empty or a duplicate"}, nil
				}
			}
			anonymous := true
			if v, ok := args["anonymous"]; ok {
				anonymous = boolArg(v)
			}
			explanation := strings.TrimSpace(fmt.Sprint(orEmpty(args["explanation"])))
			ex.mu.Lock()
			ex.polls++
			n := ex.polls
			ex.mu.Unlock()
			if n > maxExtraPolls {
				return agent.ToolOutput{Text: fmt.Sprintf("error: at most %d polls per reply", maxExtraPolls)}, nil
			}
			ex.add(func(ctx context.Context) {
				var err error
				if quiz {
					_, err = b.api.SendQuiz(ctx, chatID, q.Question, q.Options, telegram.Quiz{Correct: correct, Explanation: explanation}, telegram.SendOptions{})
				} else {
					_, err = b.api.SendOpenPoll(ctx, chatID, q.Question, q.Options, q.Multiple, anonymous, telegram.SendOptions{})
				}
				if err != nil && !telegram.IsUnreachable(err) {
					log.Printf("[chat %d] poll: %v", chatID, err)
				}
			})
			kind := "poll"
			if quiz {
				kind = "quiz"
			}
			return agent.ToolOutput{Text: fmt.Sprintf("The %s will be sent right after your answer; don't repeat its options in your text.", kind)}, nil
		},
	}}
	if !group {
		out = append(out, agent.LocalTool{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolEffect,
				Description: "A full-screen effect with your reply, rarely, for special moments (birthdays, big news): 🎉 ❤ 🔥 👍 👎 💩.",
				Parameters: map[string]any{"type": "object", "properties": map[string]any{
					"effect": map[string]any{"type": "string", "enum": telegram.EffectNames},
				}, "required": []string{"effect"}},
			}},
			Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
				e, _ := args["effect"].(string)
				e = strings.TrimSpace(strings.ReplaceAll(e, "️", ""))
				id, ok := telegram.MessageEffects[e]
				if !ok {
					return agent.ToolOutput{Text: "error: pick one of " + strings.Join(telegram.EffectNames, " ")}, nil
				}
				ex.mu.Lock()
				ex.effect = id
				ex.mu.Unlock()
				return agent.ToolOutput{Text: "Your reply will play the " + e + " effect."}, nil
			},
		})
	} else {
		out = append(out, agent.LocalTool{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolChatInfo,
				Description: "This group's title, @username, description and member count.",
				Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
			}},
			Handler: func(ctx context.Context, _ map[string]any) (agent.ToolOutput, error) {
				info, err := b.api.GetChat(ctx, chatID)
				if err != nil {
					return agent.ToolOutput{}, err
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "title: %s\ntype: %s", info.Title, info.Type)
				if info.Username != "" {
					sb.WriteString("\nusername: @" + info.Username)
				}
				if n, err := b.api.GetChatMemberCount(ctx, chatID); err == nil {
					fmt.Fprintf(&sb, "\nmembers: %d", n)
				}
				if d := strings.TrimSpace(info.Description); d != "" {
					sb.WriteString("\ndescription: " + textutil.TruncateRunes(d, 500, "…"))
				}
				return agent.ToolOutput{Text: sb.String()}, nil
			},
		})
	}
	return out
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}
