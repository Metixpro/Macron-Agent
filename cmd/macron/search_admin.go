package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Web search in the admin panel: the treg.to token (searched first; free
// TinyFish, then paid vendors within a one-cent cap) with DuckDuckGo behind it.

const settingTregToken = "treg_token"

// tregToken reads the token from the panel (encrypted), else from the environment.
func tregToken(st *store.Store, env string) func() string {
	return func() string {
		if t, err := st.SecretSetting(settingTregToken); err == nil && strings.TrimSpace(t) != "" {
			return t
		}
		return env
	}
}

func (b *Bot) viewSearch(l lang) panelView {
	t := b.app.Search.Treg
	s := t.Status()
	var sb strings.Builder
	sb.WriteString(l.t("🔎 <b>جستجوی وب</b>\n\n"))
	panel, _ := b.st.SecretSetting(settingTregToken)
	switch {
	case strings.TrimSpace(panel) != "":
		sb.WriteString(l.t("1️⃣ <b>treg.to</b>: ✅ روشن (توکن از پنل)\n"))
	case s.On:
		sb.WriteString(l.t("1️⃣ <b>treg.to</b>: ✅ روشن (توکن از <code>WAZE_TREG_TOKEN</code>)\n"))
	default:
		sb.WriteString(l.t("1️⃣ <b>treg.to</b>: ❌ خاموش (توکن نداره)\n"))
	}
	sb.WriteString(l.t("2️⃣ <b>DuckDuckGo</b> و 3️⃣ <b>Bing</b>: ✅ همیشه، هم‌زمان\n"))
	sb.WriteString(l.t("📰 اخبار: <b>Bing News</b> و <b>Google News</b> (با تاریخ، جدیدترین اول)\n"))
	sb.WriteString(l.t("⚡ جواب سریع: <b>DuckDuckGo</b> و <b>ویکی‌پدیا</b>\n"))
	if engines := b.app.Search.EngineStatus(); len(engines) > 0 {
		sb.WriteString(l.t("\n📊 <b>وضعیت موتورها</b> (از آخرین ری‌استارت)\n"))
		now := time.Now()
		for _, x := range engines {
			mark := "✅"
			switch {
			case now.Before(x.RestUntil):
				mark = "😴"
			case x.InARow > 0:
				mark = "⚠️"
			}
			fmt.Fprint(&sb, l.f("%s <code>%s</code>: %s موفق، %s خطا", mark, esc(x.Name), l.num(int(x.Hits)), l.num(int(x.Fails))))
			if now.Before(x.RestUntil) {
				fmt.Fprint(&sb, l.f(" · استراحت تا %s", x.RestUntil.In(b.loc).Format("15:04")))
			}
			if x.LastErr != "" && x.InARow > 0 {
				fmt.Fprintf(&sb, " · <i>%s</i>", esc(textutil.TruncateRunes(x.LastErr, 80, "…")))
			}
			sb.WriteString("\n")
		}
	}
	if s.Hits+s.Misses+s.Fails > 0 {
		fmt.Fprint(&sb, l.f("\n📈 از آخرین ری‌استارت: %s جستجوی موفق با treg، %s بی‌نتیجه، %s خطا\n",
			l.num(int(s.Hits)), l.num(int(s.Misses)), l.num(int(s.Fails))))
		if s.LastServedBy != "" {
			fmt.Fprint(&sb, l.f("آخرین جواب از: <code>%s</code>\n", esc(s.LastServedBy)))
		}
		if s.LastErr != "" {
			fmt.Fprint(&sb, l.f("آخرین خطا (%s): <code>%s</code>\n", s.LastAt.In(b.loc).Format("15:04"), esc(textutil.TruncateRunes(s.LastErr, 160, "…"))))
		}
	}
	sb.WriteString(l.t("\nهر جستجو هم‌زمان از همه موتورها پرسیده می‌شه و نتیجه‌ها با هم ادغام می‌شن: صفحه‌ای که چند موتور پیدا کردن بالاتر میاد، تکراری‌ها یکی می‌شن و هیچ سایتی کل لیست رو نمی‌گیره. ") +
		l.t("موتوری که خطا بده یا نتیجه بی‌ربط بده (یعنی به سرور اعتماد نکرده) ده دقیقه استراحت می‌کنه تا جستجوها کند نشن. ") +
		l.t("treg (وقتی توکن داره) اول TinyFish (رایگان) رو امتحان می‌کنه و بعد سرویس‌های دیگه‌ش تا سقف ۱ سنت برای هر جستجو؛ توکن رو از treg.to (ورود با گیت‌هاب، گوگل یا ایمیل) بگیر."))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("🔑 تنظیم توکن treg"), "a:sr:token", telegram.StyleSuccess), b.button(l.t("🧪 تست جستجو"), "a:sr:test", telegram.StylePrimary)),
	}
	if strings.TrimSpace(panel) != "" {
		rows = append(rows, row(b.button(l.t("🗑 حذف توکن"), "a:sr:del", telegram.StyleDanger)))
	}
	rows = append(rows, row(b.button(l.t("🔄 بروزرسانی"), "a:sr", ""), b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

// testSearch runs one search through treg and reports what happened.
func (b *Bot) testSearch(ctx context.Context, l lang, token string) string {
	t := b.app.Search.Treg
	if token == "" {
		return l.t("❌ توکن treg تنظیم نشده؛ جستجوها با DuckDuckGo و Bing انجام می‌شن.")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	start := time.Now()
	res, served, err := t.Search(ctx, b.app.Search.HTTPClient, token, "Telegram Bot API", 3)
	took := time.Since(start).Round(100 * time.Millisecond)
	var te *search.TregError
	switch {
	case errors.As(err, &te) && te.Unauthorized():
		return l.t("❌ treg توکن رو قبول نکرد (نامعتبره یا منقضی شده).")
	case err != nil:
		return l.f("⚠️ treg جواب نداد: %s", esc(textutil.TruncateRunes(err.Error(), 200, "…")))
	case len(res) == 0:
		return l.f("⚠️ treg وصل شد ولی چیزی پیدا نکرد (%s). جستجوها با DuckDuckGo و Bing ادامه پیدا می‌کنن.", took)
	}
	return l.f("✅ treg کار می‌کنه: %s نتیجه در %s، از <code>%s</code>\nاولی: %s", l.num(len(res)), took, esc(served),
		esc(textutil.TruncateRunes(res[0].Title, 80, "…")))
}
