package main

import (
	"context"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (b *Bot) notifyLog(ctx context.Context, html string) {
	for id := range b.owner {
		b.sendHTML(ctx, id, html, telegram.SendOptions{})
	}
}
