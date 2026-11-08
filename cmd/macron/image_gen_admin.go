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

// Picture making in the admin panel (under ⚙️ settings): the key of the
// picture studio, generate_image's first engine (76 styles, ten shapes).
//
//	a:ig         status
//	a:ig:key     set the key (dialog; the message is deleted, the key stored encrypted)
//	a:ig:test    check the key in use
//	a:ig:styles  the styles the studio draws in
//	a:ig:del     remove the panel's key (the environment's, if any, is used again)
//
// Without a key the pictures are made by the fallbacks (the Nano Banana task
// service, then Flux): nothing breaks, they just have no styles.

const settingImageKey = "image_key"

// imageGenKey reads the studio's key from the panel (encrypted), else from the environment.
func imageGenKey(st *store.Store, env string) func() string {
	return func() string {
		if k, err := st.SecretSetting(settingImageKey); err == nil && strings.TrimSpace(k) != "" {
			return k
		}
		return env
	}
}

func (b *Bot) viewImageGen(l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🎨 <b>ساخت عکس با هوش مصنوعی</b>\n\n"))
	panel, _ := b.st.SecretSetting(settingImageKey)
	switch {
	case b.app.Config.ImageAPI == "":
		sb.WriteString(l.t("⛔ سرویس ساخت عکس خاموشه (<code>WAZE_IMAGE_API_URL=off</code>)؛ عکس‌ها فقط با موتورهای پشتیبان ساخته می‌شن.\n"))
	case strings.TrimSpace(panel) != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از پنل\n"))
	case b.app.Config.ImageKey != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از <code>WAZE_IMAGE_API_KEY</code>\n"))
	default:
		sb.WriteString(l.t("🔑 کلید: ❌ تنظیم نشده؛ تا کلید نذاری عکس‌ها با موتورهای پشتیبان (Nano Banana و بعد Flux) ساخته می‌شن، بدون استایل.\n"))
	}
	sb.WriteString(l.t("\nبا کلید، ربات هر عکسی رو توی هر استایل (انیمه، سایبرپانک، رنگ‌روغن، سه‌بعدی، لگو، نئون…) و هر اندازه (۱:۱، ۱۶:۹، ۹:۱۶، ۲۱:۹…) می‌سازه و می‌تونه هر بار دو گزینه تحویل بده. ") +
		l.t("اگه سرویس جواب نده، خودکار می‌ره سراغ موتورهای پشتیبان: سرویس تسک Nano Banana و بعد Flux."))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("🔑 تنظیم کلید"), "a:ig:key", telegram.StyleSuccess), b.button(l.t("🧪 تست کلید"), "a:ig:test", telegram.StylePrimary)),
		row(b.button(l.t("🎨 استایل‌ها"), "a:ig:styles", "")),
	}
	if strings.TrimSpace(panel) != "" {
		rows = append(rows, row(b.button(l.t("🗑 حذف کلید"), "a:ig:del", telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

// testImageGen checks a key with the service and says what happened.
func (b *Bot) testImageGen(ctx context.Context, l lang, key string) string {
	if strings.TrimSpace(key) == "" {
		return l.t("❌ کلیدی تنظیم نشده.")
	}
	n, err := b.app.Registry.CheckImageKey(ctx, key)
	switch {
	case errors.Is(err, tools.ErrImageKey):
		return l.t("❌ سرویس کلید رو قبول نکرد (نامعتبره یا منقضی شده).")
	case err != nil:
		return l.f("⚠️ سرویس جواب نداد: %s", esc(textutil.TruncateRunes(err.Error(), 200, "…")))
	}
	return l.f("✅ کلید معتبره؛ ساخت عکس آماده است (%s استایل).", l.num(n))
}

// styleList is the studio's styles for an admin: id and name, in a message.
func (b *Bot) styleList(ctx context.Context, l lang) string {
	styles := b.app.Registry.StudioStyles(ctx)
	var sb strings.Builder
	sb.WriteString(l.f("🎨 <b>استایل‌های موجود</b> (%s)\n\n", l.num(len(styles))))
	for i, s := range styles {
		if i > 0 {
			sb.WriteString(" · ")
		}
		sb.WriteString("<code>" + esc(s.ID) + "</code>")
		if l == fa && s.Name != "" {
			sb.WriteString(" " + esc(s.Name))
		}
	}
	sb.WriteString(l.t("\n\nمدل خودش استایل مناسب رو انتخاب می‌کنه؛ کاربر هم می‌تونه اسمش رو بگه («به سبک انیمه»، «رنگ روغن»…)."))
	return sb.String()
}

func (b *Bot) handleImageGenPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show showFunc, ask askFunc) {
	l := b.langOf(&q.From)
	arg := ""
	if len(parts) > 2 {
		arg = parts[2]
	}
	switch arg {
	case "key":
		answer("", false)
		ask(&adminInput{kind: "ig_key"}, l.t("🔑 کلید API سرویس ساخت عکس رو بفرست. پیامت بعد از خوندن پاک می‌شه و کلید رمزنگاری‌شده ذخیره می‌شه."))
		return
	case "test":
		answer(l.t("دارم تست می‌کنم…"), false)
		b.sendHTML(ctx, q.Message.Chat.ID, b.testImageGen(ctx, l, b.app.Registry.ImageKey()), telegram.SendOptions{})
	case "styles":
		answer("", false)
		b.sendHTML(ctx, q.Message.Chat.ID, b.styleList(ctx, l), telegram.SendOptions{})
	case "del":
		_ = b.st.SetSecretSetting(ctx, settingImageKey, "")
		answer(l.t("حذف شد"), false)
	default:
		answer("", false)
	}
	show(b.viewImageGen(l))
}

// handleImageGenInput saves a key the admin sent, once the service accepts it.
func (b *Bot) handleImageGenInput(ctx context.Context, m *telegram.Message, done func(panelView)) bool {
	l := b.langOf(m.From)
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // a key is a password: never leave it in the chat
	key := strings.TrimSpace(m.Text)
	if key == "" || strings.ContainsAny(key, " \n\t") || len(key) > 300 {
		b.reply(m, l.t("این شبیه کلید نیست؛ دوباره بفرست."))
		return true
	}
	verdict := b.testImageGen(ctx, l, key)
	if strings.HasPrefix(verdict, "❌") {
		b.reply(m, verdict)
		return true
	}
	if err := b.st.SetSecretSetting(ctx, settingImageKey, key); err != nil {
		b.reply(m, l.t(textError))
		return true
	}
	b.reply(m, l.t("✅ کلید ذخیره شد؛ از همین الان عکس‌ها با استایل ساخته می‌شن.")+"\n"+verdict)
	done(b.viewImageGen(l))
	return true
}
