package main

import (
	"context"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

var (
	packLinkRe = regexp.MustCompile(`(?i)(?:t(?:elegram)?\.me/add(?:emoji|stickers)/|tg://addemoji\?set=)([A-Za-z0-9_]{1,64})`)
	packNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

func packName(s string) string {
	s = strings.TrimSpace(s)
	if m := packLinkRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}
	if packNameRe.MatchString(s) {
		return s
	}
	return ""
}

type packImport struct{ added, replaced, same, twice int }

func (b *Bot) importEmojiPack(ctx context.Context, set *telegram.StickerSet) (packImport, error) {
	existing, err := b.st.PremiumEmoji(ctx)
	if err != nil {
		return packImport{}, err
	}
	have, variants := map[string]string{}, map[string][]string{}
	for e, id := range existing {
		k := emoji.Key(e)
		have[k] = id
		variants[k] = append(variants[k], e)
	}
	var r packImport
	seen := map[string]bool{}
	for _, s := range set.Stickers {
		if s.CustomEmojiID == "" || s.Emoji == "" {
			continue
		}
		k := emoji.Key(s.Emoji)
		if seen[k] {
			r.twice++
			continue
		}
		seen[k] = true
		switch id, ok := have[k]; {
		case ok && id == s.CustomEmojiID:
			r.same++
			continue
		case ok:
			r.replaced++
		default:
			r.added++
		}
		for _, v := range variants[k] {
			_ = b.st.DeletePremiumEmoji(ctx, v)
		}
		if err := b.st.SetPremiumEmoji(ctx, s.Emoji, s.CustomEmojiID); err != nil {
			b.reloadEmoji(ctx)
			return r, err
		}
	}
	b.reloadEmoji(ctx)
	return r, nil
}

func (b *Bot) handleEmojiPackInput(ctx context.Context, m *telegram.Message, done func(panelView)) bool {
	l := b.langOf(m.From)
	name := packName(m.Text)
	if name == "" {
		b.reply(m, l.t("لینک پک رو بفرست، مثلاً <code>https://t.me/addemoji/vector_icons_by_fStikBot</code>، یا /cancel"))
		return true
	}
	set, err := b.api.GetStickerSet(ctx, name)
	if err != nil {
		b.reply(m, l.f("❌ پک <code>%s</code> پیدا نشد (%s). لینک رو چک کن و دوباره بفرست، یا /cancel", esc(name), esc(err.Error())))
		return true
	}
	if set.StickerType != "custom_emoji" {
		b.reply(m, l.t("این یه پک استیکره، نه ایموجی؛ لینک یه پک ایموجی (<code>t.me/addemoji/…</code>) رو بفرست، یا /cancel"))
		return true
	}
	r, err := b.importEmojiPack(ctx, set)
	if err != nil {
		b.reply(m, l.t("ذخیره نشد: ")+esc(err.Error()))
		return true
	}
	msg := l.f("✅ پک «%s» وارد شد: %s ایموجی جدید، %s جایگزین و %s از قبل همین بود.", esc(set.Title), l.num(r.added), l.num(r.replaced), l.num(r.same))
	if r.twice > 0 {
		msg += l.f("\n♻️ %s تا ایموجی توی خود پک تکراری بود؛ اولی ثبت شد.", l.num(r.twice))
	}
	if !b.st.SettingBool(store.SettingPremiumEmoji, false) {
		msg += l.t("\n⚠️ ایموجی پریمیوم خاموشه؛ با «🟢 روشن کن» توی همین پنل فعالش کن.")
	}
	b.reply(m, msg)
	done(b.viewEmoji(ctx, l))
	return true
}
