package main

import (
	"context"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	modeFast = "fast"
	modeDeep = "deep"

	fastSteps = 2

	fastNote = "[Fast mode] The user chose fast mode: answer right away, directly and briefly, without plans or preambles. " +
		"Use web_search at most once, only when the answer needs fresh or exact facts; otherwise answer from what you know. " +
		"If the request needs heavy work (downloads, running code, making files or images, browsing sites, long research), " +
		"do not try it: say in one short line that it needs deep mode 🧠 (/mode)."
)

var fastTools = map[string]bool{
	"web_search": true, "crypto_currency_price": true, toolReact: true, toolRemember: true, toolForget: true,
	toolSchedule: true, toolListSchedules: true, toolCancelSchedule: true, toolLookup: true,
	tools.ToolListSkills: true, tools.ToolReadSkill: true,
}

func fastTool(name string) bool { return fastTools[name] }

func (b *Bot) fastMode(ctx context.Context, uid int64) bool {
	v, err := b.st.UserPref(ctx, uid, store.PrefMode)
	return err == nil && v == modeFast
}

func (b *Bot) setMode(ctx context.Context, uid int64, fast bool) error {
	v := ""
	if fast {
		v = modeFast
	}
	return b.st.SetUserPref(ctx, uid, store.PrefMode, v)
}

func modeName(l lang, fast bool) string {
	if fast {
		return l.t("⚡ سریع")
	}
	return l.t("🧠 عمیق")
}

func modeText(l lang, fast bool) string {
	return l.f("<b>حالت پاسخ</b>: %s\n\n", modeName(l, fast)) +
		l.t("⚡ <b>سریع</b>: پاسخ کوتاه و ابزارهای سبک.\n") +
		l.t("🧠 <b>عمیق</b>: ابزارهای کامل برای کارهای چندمرحله‌ای.")
}

func (b *Bot) modeKeyboard(l lang, fast bool) *telegram.InlineKeyboardMarkup {
	mark := func(on bool, label string) string {
		if on {
			return "✓ " + label
		}
		return label
	}
	rows := [][]telegram.InlineKeyboardButton{row(
		b.button(mark(fast, l.t("⚡ سریع")), "mode:set:fast", map[bool]string{true: telegram.StylePrimary}[fast]),
		b.button(mark(!fast, l.t("🧠 عمیق")), "mode:set:deep", map[bool]string{true: telegram.StylePrimary}[!fast]),
	)}

	return keyboard(rows...)
}

func (b *Bot) sendMode(ctx context.Context, m *telegram.Message) {
	l := b.langOf(m.From)
	fast := b.fastMode(ctx, m.From.ID)
	b.sendPanel(ctx, m, modeText(l, fast), b.modeKeyboard(l, fast))
}

func (b *Bot) handleModeCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	uid := q.From.ID
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		answer("", false)
		return
	}
	if parts[1] == "t" {

		answer(l.t("حالت جواب رو با /mode عوض کن."), false)
		if q.Message != nil {
			ectx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			_ = b.api.EditMarkup(ectx, q.Message.Chat.ID, q.Message.MessageID, nil)
		}
		return
	}
	if parts[1] == "live" {
		answer("", false)
		if q.Message != nil {
			fast := b.fastMode(ctx, uid)
			_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, modeText(l, fast), b.modeKeyboard(l, fast))
		}
		return
	}
	if parts[1] != "set" {
		answer("", false)
		return
	}
	fast := parts[2] == modeFast
	if err := b.setMode(ctx, uid, fast); err != nil {
		answer(l.t(textError), true)
		return
	}
	if fast {
		answer(l.t("⚡ حالت سریع روشن شد: جواب‌ها فوری و کوتاه می‌رسن."), false)
	} else {
		answer(l.t("🧠 حالت عمیق روشن شد: ایجنت کامل با همه ابزارها."), false)
	}
	if q.Message == nil {
		return
	}

	_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, modeText(l, fast), b.modeKeyboard(l, fast))
}
