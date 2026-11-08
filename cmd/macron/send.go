package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// partChars is the size of one answer message. Telegram accepts 32768
// characters in a rich message, but long answers read better as a few
// consecutive messages.
const partChars = 3800

// sendAnswer delivers the answer as one or more rich messages; the first one
// replies to the user's message. Each part falls back to classic HTML if the
// rich message is refused.
func (b *Bot) sendAnswer(ctx context.Context, m *telegram.Message, answer string) {
	parts := b.answerChunks(b.langOf(m.From), answer)
	for i, part := range parts {
		opts := telegram.SendOptions{}
		if i == 0 {
			opts.ReplyTo = m.MessageID
		}
		if err := b.sendRich(ctx, m.Chat.ID, part, opts); err != nil {
			if telegram.IsUnreachable(err) {
				return
			}
			log.Printf("[chat %d] answer part %d failed: %v", m.Chat.ID, i, err)
		}
		if i < len(parts)-1 {
			time.Sleep(350 * time.Millisecond) // keeps the order and reads naturally
		}
	}
}

func (b *Bot) sendRich(ctx context.Context, chatID int64, md string, opts telegram.SendOptions) error {
	rtl := telegram.IsMostlyRTL(md)
	if !b.richOff.Load() {
		if set := b.premium(); set != nil {
			if _, err := b.api.SendRichMessage(ctx, chatID, telegram.RichMessage{Markdown: set.Markdown(md), IsRTL: rtl}, opts); err == nil {
				return nil
			}
		}
		_, err := b.api.SendRichMessage(ctx, chatID, telegram.RichMessage{Markdown: md, IsRTL: rtl}, opts)
		if err == nil || telegram.IsUnreachable(err) || ctx.Err() != nil {
			return err
		}
		if telegram.IsUnsupported(err) {
			log.Printf("rich messages are not available (%v); using classic messages", err)
			b.richOff.Store(true)
		}
	}
	html := telegram.MarkdownToHTML(md)
	if rtl {
		html = telegram.MarkRTL(html)
	}
	if set := b.premium(); set != nil {
		if _, err := b.api.SendHTMLParts(ctx, chatID, set.HTML(html), opts); err == nil {
			return nil
		}
	}
	_, err := b.api.SendHTMLParts(ctx, chatID, html, opts)
	return err
}

// redact removes secrets that must never reach a chat (provider keys and hosts,
// the bot token), as a last line of defence.
func (b *Bot) redact(s string) string {
	secrets := append(b.prov.Secrets(), b.api.Token, b.app.Search.Treg.Token())
	for _, sec := range secrets {
		if len(sec) >= 6 {
			s = strings.ReplaceAll(s, sec, "•••")
		}
	}
	return s
}
