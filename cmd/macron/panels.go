package main

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const textNotYourPanel = "این دکمه‌ها مال کسیه که دستور رو زده؛ اگه خودت می‌خوای، همون دستور رو بفرست 🙂"

const panelOwnerTTL = 7 * 24 * time.Hour

type panelOwner struct {
	id int64
	at time.Time
}

var panelsNoted atomic.Int64

func ownedPanel(data string) bool {
	for _, p := range []string{"lang:", "mode:", "sc:", "m:", "f:", "shop:", "me:", "gl:"} {
		if strings.HasPrefix(data, p) {
			return true
		}
	}
	return false
}

func panelKey(chatID, msgID int64) string { return fmt.Sprintf("%d:%d", chatID, msgID) }

func (b *Bot) notePanel(chat telegram.Chat, sent *telegram.Message, owner int64) {
	if sent == nil || chat.Private() || owner == 0 {
		return
	}
	b.panels.Store(panelKey(chat.ID, sent.MessageID), panelOwner{id: owner, at: time.Now()})
	if panelsNoted.Add(1)%200 == 0 {
		b.panels.Range(func(k, v any) bool {
			if time.Since(v.(panelOwner).at) > panelOwnerTTL {
				b.panels.Delete(k)
			}
			return true
		})
	}
}

func (b *Bot) sendPanel(ctx context.Context, m *telegram.Message, html string, kb *telegram.InlineKeyboardMarkup) *telegram.Message {
	sent := b.sendHTML(ctx, m.Chat.ID, html, telegram.SendOptions{ReplyTo: m.MessageID, Markup: kb})
	if m.From != nil {
		b.notePanel(m.Chat, sent, m.From.ID)
	}
	return sent
}

func (b *Bot) panelOwnerOf(msg *telegram.Message) int64 {
	if v, ok := b.panels.Load(panelKey(msg.Chat.ID, msg.MessageID)); ok {
		return v.(panelOwner).id
	}
	if r := msg.RepliedInChat(); r != nil && r.From != nil && !r.From.IsBot {
		return r.From.ID
	}
	return 0
}

func (b *Bot) foreignPanel(q *telegram.CallbackQuery) bool {
	if q.Message == nil || q.Message.Chat.Private() || !ownedPanel(q.Data) {
		return false
	}
	owner := b.panelOwnerOf(q.Message)
	return owner != 0 && owner != q.From.ID
}
