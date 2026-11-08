package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// The statuses with a premium emoji of their own: the live card's
// «🎙 listening» while a voice message is written down and transcribe_audio's
// «🎧 listening». A slot holds a custom emoji id for that status line only;
// the same emoji anywhere else keeps the general mapping. «✨» in the panel
// turns the bot's own animated ones (emoji.StatusArt: its avatar listening)
// into a custom emoji set and fills the slots with them.
const settingStatusEmoji = "status_emoji_" // + the slot's key

type statusSlot struct{ key, emoji, text string }

var statusSlots = []statusSlot{
	{"listen", "🎙", "دارم می‌شنوم…"},
	{"transcribe", "🎧", "دارم گوش می‌دم…"},
}

func slotByKey(key string) (statusSlot, bool) {
	for _, s := range statusSlots {
		if s.key == key {
			return s, true
		}
	}
	return statusSlot{}, false
}

// statusIcons maps each status's emoji to its own premium emoji. It goes on
// status lines only, before the general mapping.
func (b *Bot) statusIcons() *emoji.Set {
	m := map[string]string{}
	for _, s := range statusSlots {
		if id, _ := b.st.Setting(settingStatusEmoji + s.key); id != "" {
			m[s.emoji] = id
		}
	}
	return emoji.New(m)
}

// statusSetName is the bot's own custom emoji set; Telegram wants the bot's
// username at the end.
func statusSetName(bot string) string {
	return fmt.Sprintf("status_v%d_by_%s", emoji.StatusArtVersion, bot)
}

// makeStatusEmoji creates the bot's custom emoji set from its status emoji
// (or finds the one made before), owned by the admin who asked, and puts
// each emoji in its slot. They keep the avatar's own colors.
func (b *Bot) makeStatusEmoji(ctx context.Context, owner int64) (string, error) {
	name := statusSetName(b.me.Username)
	art := emoji.StatusArt()
	set, err := b.api.GetStickerSet(ctx, name)
	if err != nil {
		if !telegram.IsBadRequest(err) {
			return name, err
		}
		list := make([]telegram.AnimatedEmoji, len(art))
		for i, a := range art {
			list[i] = telegram.AnimatedEmoji{Emoji: a.Emoji, TGS: a.TGS}
		}
		title := textutil.TruncateRunes(strings.TrimSpace(b.me.FirstName+" status"), 64, "")
		if err := b.api.CreateEmojiSet(ctx, owner, name, title, list, false); err != nil {
			return name, err
		}
		if set, err = b.api.GetStickerSet(ctx, name); err != nil {
			return name, err
		}
	}
	ids := map[string]string{}
	for _, s := range set.Stickers {
		if s.CustomEmojiID != "" {
			ids[emoji.Key(s.Emoji)] = s.CustomEmojiID
		}
	}
	for _, a := range art {
		id := ids[emoji.Key(a.Emoji)]
		if id == "" {
			return name, fmt.Errorf("the set %s has no %s", name, a.Emoji)
		}
		if err := b.st.SetSetting(ctx, settingStatusEmoji+a.Key, id); err != nil {
			return name, err
		}
	}
	return name, nil
}

// statusEmojiLines is the panel's part on the statuses' own emoji.
func (b *Bot) statusEmojiLines(l lang) string {
	var sb strings.Builder
	sb.WriteString(l.t("\n\n🎛 <b>ایموجی وضعیت‌ها</b> (فقط روی خط همون وضعیت):"))
	for _, s := range statusSlots {
		state := l.t("از نگاشت کلی")
		if id, _ := b.st.Setting(settingStatusEmoji + s.key); id != "" {
			state = l.t("✅ ایموجی خودش")
		}
		fmt.Fprintf(&sb, "\n%s %s %s", s.emoji, esc(l.t(s.text)), state)
	}
	return sb.String()
}

// sendStatusSample shows each status with its own emoji, as the live card would.
func (b *Bot) sendStatusSample(ctx context.Context, chatID int64, l lang) {
	lines := make([]string, len(statusSlots))
	for i, s := range statusSlots {
		lines[i] = s.emoji + " " + esc(l.t(s.text))
	}
	sample := b.statusIcons().HTML(strings.Join(lines, "\n"))
	if _, err := b.api.SendHTML(ctx, chatID, sample, telegram.SendOptions{}); err != nil {
		b.sendHTML(ctx, chatID, l.t("❌ تلگرام قبول نکرد: <code>")+esc(err.Error())+l.t("</code>\nاحتمالاً صاحب ربات پریمیوم نداره."), telegram.SendOptions{})
	}
}

func (b *Bot) handleStatusEmojiPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), ask askFunc) {
	l := b.langOf(&q.From)
	chatID := q.Message.Chat.ID
	if parts[2] == "slot" {
		answer("", false)
		s, ok := slotByKey(strings.Join(parts[3:], ":"))
		if !ok {
			return
		}
		ask(&adminInput{kind: "em_slot", mode: s.key}, l.f("ایموجی پریمیوم «%s %s» رو بفرست (یا آیدی عددی‌اش). فقط روی همین وضعیت می‌شینه؛ ", s.emoji, esc(l.t(s.text)))+
			l.t("<code>0</code> = برگشت به نگاشت کلی."))
		return
	}
	answer(l.t("⏳ دارم می‌سازم…"), false)
	name, err := b.makeStatusEmoji(ctx, q.From.ID)
	if err != nil {
		b.sendHTML(ctx, chatID, l.t("❌ ساخته نشد: <code>")+esc(err.Error())+"</code>", telegram.SendOptions{})
		return
	}
	msg := l.f("✨ ایموجی‌های وضعیت ساخته شدن و هر کدوم سر جای خودش نشست: <a href=\"https://t.me/addemoji/%s\">%s</a>", name, name)
	if !b.st.SettingBool(store.SettingPremiumEmoji, false) {
		msg += l.t("\n\n⚠️ ایموجی پریمیوم خاموشه؛ روشنش کن تا روی کارت زنده دیده بشن.")
	}
	b.sendHTML(ctx, chatID, msg, telegram.SendOptions{})
	b.sendStatusSample(ctx, chatID, l)
}

func (b *Bot) handleStatusEmojiInput(ctx context.Context, m *telegram.Message, in *adminInput, done func(panelView)) bool {
	l := b.langOf(m.From)
	s, ok := slotByKey(in.mode)
	if !ok {
		done(b.viewEmoji(ctx, l))
		return true
	}
	text := strings.TrimSpace(m.Text)
	id := ""
	for _, e := range m.Entities {
		if e.Type == "custom_emoji" && e.CustomEmojiID != "" {
			id = e.CustomEmojiID
			break
		}
	}
	if _, err := strconv.ParseUint(text, 10, 64); id == "" && err == nil && text != "0" {
		id = text
	}
	switch {
	case id != "":
		if err := b.st.SetSetting(ctx, settingStatusEmoji+s.key, id); err != nil {
			b.reply(m, l.t(textError))
			return true
		}
		b.reply(m, l.f("✅ «%s %s» از این به بعد ایموجی خودش رو داره.", s.emoji, esc(l.t(s.text))))
	case text == "0":
		_ = b.st.DeleteSetting(ctx, settingStatusEmoji+s.key)
		b.reply(m, l.f("↩️ «%s %s» دوباره از نگاشت کلی استفاده می‌کنه.", s.emoji, esc(l.t(s.text))))
	default:
		b.reply(m, l.t("یه ایموجی پریمیوم یا آیدی عددی‌اش رو بفرست (یا <code>0</code>)، یا /cancel"))
		return true
	}
	done(b.viewEmoji(ctx, l))
	return true
}
