package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const partChars = 3800

func (b *Bot) sendAnswer(ctx context.Context, m *telegram.Message, answer string) {
	parts := b.answerChunks(b.langOf(m.From), answer)
	for i, part := range parts {
		opts := telegram.SendOptions{}
		if i == 0 {
			opts.ReplyTo = m.MessageID
		}
		if err := b.sendSure(ctx, m, part, opts); err != nil {
			if telegram.IsUnreachable(err) {
				return
			}
			log.Printf("[chat %d] answer part %d failed: %v", m.Chat.ID, i, err)
		}
		if i < len(parts)-1 {
			time.Sleep(350 * time.Millisecond)
		}
	}
}

func (b *Bot) sendRich(ctx context.Context, chatID int64, md string, opts telegram.SendOptions) error {
	rtl := telegram.IsMostlyRTL(md)
	if !b.richOff.Load() {
		if set := b.premium(); set != nil {
			_, err := b.api.SendRichMessage(ctx, chatID, telegram.RichMessage{Markdown: set.Markdown(md), IsRTL: rtl}, opts)
			if err == nil || netDown(err) {
				return err
			}
		}
		_, err := b.api.SendRichMessage(ctx, chatID, telegram.RichMessage{Markdown: md, IsRTL: rtl}, opts)
		if err == nil || telegram.IsUnreachable(err) || ctx.Err() != nil || netDown(err) {
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
		_, err := b.api.SendHTMLParts(ctx, chatID, set.HTML(html), opts)
		if err == nil || netDown(err) {
			return err
		}
	}
	_, err := b.api.SendHTMLParts(ctx, chatID, html, opts)
	return err
}

func (b *Bot) redact(s string) string {
	secrets := append(b.prov.Secrets(), b.api.Token, b.app.Search.Treg.Token())
	for _, sec := range secrets {
		if len(sec) >= 6 {
			s = strings.ReplaceAll(s, sec, "•••")
		}
	}
	return s
}
