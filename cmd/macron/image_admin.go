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

// AI image editing in the admin panel (under ⚙️ settings): the key of the
// Nano Banana task service, which edits pictures and is generate_image's
// second engine.
//
//	a:ie             status
//	a:ie:key         set the key (dialog; the message is deleted, the key stored encrypted)
//	a:ie:test        check the key in use
//	a:ie:del         remove the panel's key (the environment's, if any, is used again)

const settingImageEditKey = "image_edit_key"

// imageEditKey reads the key from the panel (encrypted), else from the environment.
func imageEditKey(st *store.Store, env string) func() string {
	return func() string {
		if k, err := st.SecretSetting(settingImageEditKey); err == nil && strings.TrimSpace(k) != "" {
			return k
		}
		return env
	}
}

func (b *Bot) viewImageEdit(l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🖼 <b>ویرایش عکس با هوش مصنوعی</b> (Nano Banana)\n\n"))
	panel, _ := b.st.SecretSetting(settingImageEditKey)
	switch {
	case b.app.Config.ImageEditAPI == "":
		sb.WriteString(l.t("⛔ خاموشه (<code>WAZE_IMAGE_EDIT_URL=off</code>).\n"))
	case strings.TrimSpace(panel) != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از پنل\n"))
	case b.app.Config.ImageEditKey != "":
		sb.WriteString(l.t("🔑 کلید: ✅ از <code>WAZE_IMAGE_EDIT_KEY</code>\n"))
	default:
		sb.WriteString(l.t("🔑 کلید: ❌ تنظیم نشده؛ ربات فقط ویرایش‌های ساده (برش، نور، متن…) رو با کد انجام می‌ده.\n"))
	}
	sb.WriteString(l.t("\nبا کلید، ربات عکس‌های کاربرها رو با یه دستور ساده ویرایش می‌کنه: عوض کردن پس‌زمینه، سبک، نور و فصل، اضافه یا حذف کردن چیزها، رتوش، رنگی کردن عکس قدیمی و ترکیب چندتا عکس. ") +
		l.t("همین سرویس موتور دوم ساخت عکسه: اگه موتور اول جواب نده، قبل از Flux امتحان می‌شه. هر ویرایش معمولاً یکی دو دقیقه طول می‌کشه."))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("🔑 تنظیم کلید"), "a:ie:key", telegram.StyleSuccess), b.button(l.t("🧪 تست کلید"), "a:ie:test", telegram.StylePrimary)),
	}
	if strings.TrimSpace(panel) != "" {
		rows = append(rows, row(b.button(l.t("🗑 حذف کلید"), "a:ie:del", telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

// testImageEdit checks a key with the service and says what happened.
func (b *Bot) testImageEdit(ctx context.Context, l lang, key string) string {
	if strings.TrimSpace(key) == "" {
		return l.t("❌ کلیدی تنظیم نشده.")
	}
	err := b.app.Registry.CheckImageEditKey(ctx, key)
	switch {
	case errors.Is(err, tools.ErrImageEditKey):
		return l.t("❌ سرویس کلید رو قبول نکرد (نامعتبره یا منقضی شده).")
	case err != nil:
		return l.f("⚠️ سرویس جواب نداد: %s", esc(textutil.TruncateRunes(err.Error(), 200, "…")))
	}
	return l.t("✅ کلید معتبره؛ ویرایش عکس آماده است.")
}

func (b *Bot) handleImageEditPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show showFunc, ask askFunc) {
	l := b.langOf(&q.From)
	arg := ""
	if len(parts) > 2 {
		arg = parts[2]
	}
	switch arg {
	case "key":
		answer("", false)
		ask(&adminInput{kind: "ie_key"}, l.t("🔑 کلید API سرویس ویرایش عکس رو بفرست. پیامت بعد از خوندن پاک می‌شه و کلید رمزنگاری‌شده ذخیره می‌شه."))
		return
	case "test":
		answer(l.t("دارم تست می‌کنم…"), false)
		b.sendHTML(ctx, q.Message.Chat.ID, b.testImageEdit(ctx, l, b.app.Registry.ImageEditKey()), telegram.SendOptions{})
	case "del":
		_ = b.st.SetSecretSetting(ctx, settingImageEditKey, "")
		answer(l.t("حذف شد"), false)
	default:
		answer("", false)
	}
	show(b.viewImageEdit(l))
}

// handleImageEditInput saves a key the admin sent, once the service accepts it.
func (b *Bot) handleImageEditInput(ctx context.Context, m *telegram.Message, done func(panelView)) bool {
	l := b.langOf(m.From)
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // a key is a password: never leave it in the chat
	key := strings.TrimSpace(m.Text)
	if key == "" || strings.ContainsAny(key, " \n\t") || len(key) > 300 {
		b.reply(m, l.t("این شبیه کلید نیست؛ دوباره بفرست."))
		return true
	}
	verdict := b.testImageEdit(ctx, l, key)
	if strings.HasPrefix(verdict, "❌") {
		b.reply(m, verdict)
		return true
	}
	if err := b.st.SetSecretSetting(ctx, settingImageEditKey, key); err != nil {
		b.reply(m, l.t(textError))
		return true
	}
	b.reply(m, l.t("✅ کلید ذخیره شد؛ از همین الان ویرایش عکس فعاله.")+"\n"+verdict)
	done(b.viewImageEdit(l))
	return true
}
