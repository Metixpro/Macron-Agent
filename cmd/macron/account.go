package main

import (
	"context"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (b *Bot) showPanel(ctx context.Context, msg *telegram.Message, v panelView) int64 {
	err := b.editHTML(ctx, msg.Chat.ID, msg.MessageID, v.html, v.kb)
	if err == nil || telegram.IsNotModified(err) {
		return msg.MessageID
	}
	sent := b.sendHTML(ctx, msg.Chat.ID, v.html, telegram.SendOptions{Markup: v.kb})
	if sent == nil {
		return msg.MessageID
	}
	return sent.MessageID
}

func (b *Bot) viewMe(ctx context.Context, u *store.User) panelView {
	l := b.lang(u.ID)
	used, _ := b.st.TokensToday(ctx, u.ID)
	mem, _ := b.mem.Load(ctx, u.ID)
	memLine := l.t("خالی")
	if mem != nil && mem.Tokens > 0 {
		pct := float64(mem.Tokens) * 100 / float64(max(mem.Budget, 1))
		memLine = l.f("%s از %s توکن (%.1f٪)", humanInt(mem.Tokens), humanInt(mem.Budget), pct)
		if mem.Compressions > 0 {
			memLine += l.f(" · %d بار خلاصه شده", mem.Compressions)
		}
	}
	files, _ := b.app.Machine.ListFiles(sessionFor(u.ID))
	facts, _ := b.memoryItems()
	factLine := l.f("%d مورد (/memory)", len(facts))
	if !b.factsOn(ctx, u.ID) {
		factLine = l.t("خاموش (/memory)")
	}
	kept, _ := b.st.ArchiveCount(ctx, u.ID)
	html := l.f(`<b>%s</b>

🔋 توکن امروز: <b>%s</b>
🧠 حافظه گفتگو: %s
📌 چیزهایی که درباره‌ات یادمه: %s
🗂 گفتگوهای بایگانی‌شده: %s پیام
📁 فایل‌ها: %d (%s)`, esc(u.DisplayName()), tokenWord(l, used), memLine, factLine, humanInt(kept), len(files), keptFor(l, b.filesTTL()))
	return panelView{html: html, kb: keyboard(row(b.button(l.t("🔄 بروزرسانی"), "me:home", ""), b.button(l.t("✖️ بستن"), "me:x", telegram.StyleDanger)))}
}

func (b *Bot) sendMe(ctx context.Context, m *telegram.Message, u *store.User) {
	v := b.viewMe(ctx, u)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func (b *Bot) handleMeCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	answer("", false)
	if strings.TrimPrefix(q.Data, "me:") == "x" {
		_ = b.api.DeleteMessage(ctx, q.Message.Chat.ID, q.Message.MessageID)
		return
	}
	u, err := b.st.User(ctx, q.From.ID)
	if err != nil {
		return
	}
	b.showPanel(ctx, q.Message, b.viewMe(ctx, u))
}
