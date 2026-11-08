package main

import (
	"context"
	"errors"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const settingMusicKey = "music_key"

func musicKey(st *store.Store, env string) func() string {
	return func() string {
		if k, err := st.SecretSetting(settingMusicKey); err == nil && strings.TrimSpace(k) != "" {
			return k
		}
		return env
	}
}

func (b *Bot) viewMusic(l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🎤 <b>ساخت آهنگ با خواننده</b>\n\n"))
	panel, _ := b.st.SecretSetting(settingMusicKey)
	switch {
	case b.app.Config.MusicAPI == "":
		sb.WriteString(l.t("⛔ خاموشه (<code>WAZE_MUSIC_API_URL=off</code>).\n"))
	case strings.TrimSpace(panel) != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از پنل\n"))
	case b.app.Config.MusicKey != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از <code>WAZE_MUSIC_API_KEY</code>\n"))
	default:
		sb.WriteString(l.t("🔑 کلید: ❌ تنظیم نشده؛ تا کلید نذاری، ربات آهنگ با خواننده نمی‌سازه و قولش رو هم نمی‌ده.\n"))
	}
	sb.WriteString(l.t("\nبا کلید، ربات از روی متن ترانه‌ای که خودش می‌نویسه (یا کاربر می‌ده) یه آهنگ کامل با صدای خواننده می‌سازه: سبک و صدای مرد یا زن رو انتخاب می‌کنه، دو نسخه حدود دو دقیقه‌ای با کاور تحویل می‌ده، و ساختنش حدود دو دقیقه طول می‌کشه."))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("🔑 تنظیم کلید"), "a:mu:key", telegram.StyleSuccess), b.button(l.t("🧪 تست کلید"), "a:mu:test", telegram.StylePrimary)),
	}
	if strings.TrimSpace(panel) != "" {
		rows = append(rows, row(b.button(l.t("🗑 حذف کلید"), "a:mu:del", telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) testMusic(ctx context.Context, l lang, key string) string {
	if strings.TrimSpace(key) == "" {
		return l.t("❌ کلیدی تنظیم نشده.")
	}
	err := b.app.Registry.CheckMusicKey(ctx, key)
	switch {
	case errors.Is(err, tools.ErrMusicKey):
		return l.t("❌ سرویس کلید رو قبول نکرد (نامعتبره یا منقضی شده).")
	case err != nil:
		return l.f("⚠️ سرویس جواب نداد: %s", esc(textutil.TruncateRunes(err.Error(), 200, "…")))
	}
	return l.t("✅ کلید معتبره؛ ساخت آهنگ آماده است (تست هیچ آهنگی نساخت).")
}

func (b *Bot) handleMusicPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show showFunc, ask askFunc) {
	l := b.langOf(&q.From)
	arg := ""
	if len(parts) > 2 {
		arg = parts[2]
	}
	switch arg {
	case "key":
		answer("", false)
		ask(&adminInput{kind: "mu_key"}, l.t("🔑 کلید API سرویس ساخت آهنگ رو بفرست. پیامت بعد از خوندن پاک می‌شه و کلید رمزنگاری‌شده ذخیره می‌شه."))
		return
	case "test":
		answer(l.t("دارم تست می‌کنم…"), false)
		b.sendHTML(ctx, q.Message.Chat.ID, b.testMusic(ctx, l, b.app.Registry.MusicKey()), telegram.SendOptions{})
	case "del":
		_ = b.st.SetSecretSetting(ctx, settingMusicKey, "")
		answer(l.t("حذف شد"), false)
	default:
		answer("", false)
	}
	show(b.viewMusic(l))
}

func (b *Bot) handleMusicInput(ctx context.Context, m *telegram.Message, done func(panelView)) bool {
	l := b.langOf(m.From)
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	key := strings.TrimSpace(m.Text)
	if key == "" || strings.ContainsAny(key, " \n\t") || len(key) > 300 {
		b.reply(m, l.t("این شبیه کلید نیست؛ دوباره بفرست."))
		return true
	}
	verdict := b.testMusic(ctx, l, key)
	if strings.HasPrefix(verdict, "❌") {
		b.reply(m, verdict)
		return true
	}
	if err := b.st.SetSecretSetting(ctx, settingMusicKey, key); err != nil {
		b.reply(m, l.t(textError))
		return true
	}
	b.reply(m, l.t("✅ کلید ذخیره شد؛ از همین الان ربات آهنگ با خواننده می‌سازه.")+"\n"+verdict)
	done(b.viewMusic(l))
	return true
}
