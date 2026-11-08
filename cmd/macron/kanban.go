package main

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// The board: the owner's jobs that outlive one conversation, in to do,
// doing, blocked and done. The agent keeps it with kanban_board (add, move,
// note, run); a card that runs on its own is a background task, with a budget
// of its own and its report when it ends, and the card follows it. /board
// shows it.

const (
	toolKanban    = "kanban_board"
	boardDoneDays = 7
)

var bgIDRe = regexp.MustCompile(`#(\d+)`)

func (b *Bot) kanbanTool(u *store.User, m *telegram.Message) agent.LocalTool {
	uid := u.ID
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolKanban,
			Description: "The owner's board of jobs that outlive this conversation (todo, doing, blocked, done). add a card for work to come; " +
				"move it as it goes; note what happened; run starts a card as a background task with its own budget (its report comes when " +
				"it ends, and the card follows it); list shows the board.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"action": map[string]any{"type": "string", "enum": []string{"add", "list", "move", "note", "run", "delete"}},
				"id":     map[string]any{"type": "integer", "description": "The card (move, note, run, delete)"},
				"title":  map[string]any{"type": "string", "description": "add: a short title"},
				"notes":  map[string]any{"type": "string", "description": "add: the goal in full; note: what to add; run: the whole goal if the notes lack it"},
				"status": map[string]any{"type": "string", "enum": []string{store.CardTodo, store.CardDoing, store.CardBlocked, store.CardDone}},
			}, "required": []string{"action"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			action, _ := args["action"].(string)
			id := int64(numArg(args["id"]))
			title, _ := args["title"].(string)
			notes, _ := args["notes"].(string)
			status, _ := args["status"].(string)
			text := func(s string) (agent.ToolOutput, error) { return agent.ToolOutput{Text: s}, nil }
			switch action {
			case "add":
				title = textutil.TruncateRunes(textutil.OneLine(strings.TrimSpace(title)), 80, "…")
				if title == "" {
					return text("error: a card needs a title")
				}
				cid, err := b.st.AddCard(ctx, uid, title, strings.TrimSpace(notes))
				if err != nil {
					return text("error: " + err.Error())
				}
				return text(fmt.Sprintf("Card #%d «%s» is in todo.", cid, title))
			case "move":
				if err := b.st.MoveCard(ctx, uid, id, status, ""); err != nil {
					return text("error: " + err.Error())
				}
				return text(fmt.Sprintf("Card #%d is %s now.", id, status))
			case "note":
				c, err := b.st.Card(ctx, uid, id)
				if err != nil {
					return text("error: no card #" + strconv.FormatInt(id, 10))
				}
				if err := b.st.MoveCard(ctx, uid, id, c.Status, strings.TrimSpace(notes)); err != nil {
					return text("error: " + err.Error())
				}
				return text(fmt.Sprintf("Noted on card #%d.", id))
			case "delete":
				if err := b.st.DeleteCard(ctx, uid, id); err != nil {
					return text("error: " + err.Error())
				}
				return text(fmt.Sprintf("Card #%d removed.", id))
			case "run":
				c, err := b.st.Card(ctx, uid, id)
				if err != nil {
					return text("error: no card #" + strconv.FormatInt(id, 10))
				}
				goal := strings.TrimSpace(c.Notes + "\n" + notes)
				if len([]rune(goal)) < 10 {
					goal = c.Title + ". " + goal
				}
				out := b.startBg(ctx, u, m, c.Title, goal+fmt.Sprintf("\n\n(This is card #%d of the owner's board.)", c.ID))
				if sub := bgIDRe.FindStringSubmatch(out); strings.HasPrefix(out, "Started") && len(sub) == 2 {
					task, _ := strconv.ParseInt(sub[1], 10, 64)
					_ = b.st.SetCardTask(ctx, uid, c.ID, task)
				}
				return text(out)
			default:
				return text(b.boardText(ctx, uid, en))
			}
		},
	}
}

// syncBoard moves the cards whose background task ended.
func (b *Bot) syncBoard(ctx context.Context, uid int64, cards []store.Card) []store.Card {
	for i, c := range cards {
		if c.BgTask == 0 || c.Status != store.CardDoing {
			continue
		}
		t, err := b.st.BgTaskByID(ctx, c.BgTask)
		if err != nil || t == nil || t.Active() {
			continue
		}
		next, note := store.CardDone, "background task #"+strconv.FormatInt(t.ID, 10)+" finished"
		if t.Status != store.BgDone {
			next, note = store.CardBlocked, "background task #"+strconv.FormatInt(t.ID, 10)+" "+t.Status+": "+textutil.TruncateRunes(t.Note, 200, "…")
		}
		if b.st.MoveCard(ctx, uid, c.ID, next, note) == nil {
			cards[i].Status = next
		}
	}
	return cards
}

// boardText is the board in four columns.
func (b *Bot) boardText(ctx context.Context, uid int64, l lang) string {
	cards, err := b.st.Cards(ctx, uid, boardDoneDays)
	if err != nil {
		return "error: " + err.Error()
	}
	cards = b.syncBoard(ctx, uid, cards)
	cols := []struct{ status, name string }{{store.CardTodo, l.t("[ ] انجام نشده")}, {store.CardDoing, l.t("[*] در جریان")},
		{store.CardBlocked, l.t("[!] گیر کرده")}, {store.CardDone, l.t("[+] تموم شده")}}
	var sb strings.Builder
	empty := true
	for _, col := range cols {
		var lines []string
		for _, c := range cards {
			if c.Status == col.status {
				line := fmt.Sprintf("#%d %s", c.ID, c.Title)
				if c.BgTask > 0 {
					line += fmt.Sprintf(" (bg #%d)", c.BgTask)
				}
				lines = append(lines, line)
			}
		}
		if len(lines) == 0 {
			continue
		}
		empty = false
		fmt.Fprintf(&sb, "%s\n  %s\n", col.name, strings.Join(lines, "\n  "))
	}
	if empty {
		return l.t("تخته کار خالیه.")
	}
	return strings.TrimSpace(sb.String())
}

// boardCommand is /board.
func (b *Bot) boardCommand(ctx context.Context, m *telegram.Message, l lang) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	b.reply(m, l.t("📋 <b>تخته کار</b>")+"\n<pre>"+esc(b.boardText(cctx, m.From.ID, l))+"</pre>")
}
