package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// A whole premium emoji pack at once: the admin sends its link
// (t.me/addemoji/NAME) and each of its emoji takes the place of the ordinary
// emoji its creator gave it. The pack wins over earlier mappings of the same
// emoji; inside the pack the first one of an emoji counts.

var (
	packLinkRe = regexp.MustCompile(`(?i)(?:t(?:elegram)?\.me/add(?:emoji|stickers)/|tg://addemoji\?set=)([A-Za-z0-9_]{1,64})`)
	packNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,63}$`)
)

// packName reads a pack's name from its link or as it is ("" if neither).
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

// packImport counts what an import did.
type packImport struct{ added, replaced, same, twice int }

// importEmojiPack maps each emoji of a custom emoji set to its premium emoji.
func (b *Bot) importEmojiPack(ctx context.Context, set *telegram.StickerSet) (packImport, error) {
	existing, err := b.st.PremiumEmoji(ctx)
	if err != nil {
		return packImport{}, err
	}
	have, variants := map[string]string{}, map[string][]string{} // by emoji.Key: its premium emoji, the spellings stored
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
		for _, v := range variants[k] { // "❤" and "❤️" are one emoji: the older spelling would win
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

// handleEmojiPackInput imports the pack whose link an admin sent.
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
	have, total := b.consoleCoverage()
	msg += l.f("\n🖥 آیکون‌های ترمینال: %s از %s پریمیوم دارن.", l.num(have), l.num(total))
	if !b.st.SettingBool(store.SettingPremiumEmoji, false) {
		msg += l.t("\n⚠️ ایموجی پریمیوم خاموشه؛ با «🟢 روشن کن» توی همین پنل فعالش کن.")
	}
	b.reply(m, msg)
	done(b.viewEmoji(ctx, l))
	return true
}

// consoleCoverage counts the console's icons that have a premium emoji
// (mapped, whether premium emoji are on or not).
func (b *Bot) consoleCoverage() (have, total int) {
	ic := iconPicker{b.emojis.Load()}
	for _, c := range consoleIconList() {
		total++
		if ic.premium(c.cands) {
			have++
		}
	}
	return have, total
}

// viewConsoleIcons lists the console's icons and which have a premium emoji.
func (b *Bot) viewConsoleIcons(l lang) panelView {
	ic := iconPicker{b.emojis.Load()}
	have, total := b.consoleCoverage()
	var sb strings.Builder
	sb.WriteString(l.f("🖥 <b>آیکون‌های ترمینال</b>: %s از %s پریمیوم دارن\n\n", l.num(have), l.num(total)))
	for _, c := range consoleIconList() {
		state, alts := "✅", ""
		if !ic.premium(c.cands) {
			state = "▫️"
			if len(c.cands) > 1 {
				alts = l.f(" (یا %s)", strings.Join(c.cands[1:], " "))
			}
		}
		fmt.Fprintf(&sb, "%s %s %s%s\n", state, ic.pick(c.cands), esc(c.name), alts)
	}
	sb.WriteString(l.t("\nهر کدوم که پریمیوم نداره رو با «➕ افزودن» به یه ایموجی پریمیوم وصل کن (هر کدوم از جایگزین‌هاش هم قبوله)، یا یه پک کامل رو با «📦 وارد کردن پک» بیار."))
	return panelView{html: sb.String(), kb: keyboard(
		row(b.button(l.t("📦 وارد کردن پک"), "a:em:pack", telegram.StylePrimary), b.button(l.t("➕ افزودن"), "a:em:add", "")),
		row(b.back(l, "a:em")))}
}
