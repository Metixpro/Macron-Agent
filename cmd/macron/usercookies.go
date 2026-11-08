package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const cookieWaitTTL = 10 * time.Minute

type cookieWait struct {
	until         time.Time
	chatID, msgID int64
}

func (b *Bot) showCookies(ctx context.Context, chatID, msgID int64, v panelView) int64 {
	err := b.editHTML(ctx, chatID, msgID, v.html, v.kb)
	if err == nil || telegram.IsNotModified(err) {
		return msgID
	}
	if sent := b.sendHTML(ctx, chatID, v.html, telegram.SendOptions{Markup: v.kb}); sent != nil {
		return sent.MessageID
	}
	return msgID
}

func (b *Bot) cookieAsk(l lang, uid int64, notice string) panelView {
	html := l.t("🍪 فایل <code>cookies.txt</code> (فرمت Netscape) رو بفرست، یا متنش رو کپی کن. ") +
		l.t("پیامت بعد از خوندن پاک می‌شه. کوکی تازه یه سایت جای قبلیِ همون سایت رو می‌گیره.") + l.t("\n\n<i>برای انصراف /cancel رو بفرست.</i>")
	if notice != "" {
		html = notice + "\n\n" + html
	}
	return panelView{html: html, kb: keyboard(row(b.button(l.t("❌ انصراف"), "ck:c:"+strconv.FormatInt(uid, 10), telegram.StyleDanger)))}
}

func withNotice(v panelView, notice string) panelView {
	if notice != "" {
		v.html = notice + "\n\n" + v.html
	}
	return v
}

func (b *Bot) openCookiesPanel(ctx context.Context, m *telegram.Message) {
	l := b.langOf(m.From)
	if !m.Chat.Private() {
		b.reply(m, l.f("🍪 کوکی‌ها حکم اطلاعات ورود به حسابت رو دارن؛ فقط توی پیوی ربات بفرستشون: @%s", b.me.Username))
		return
	}
	v := b.viewUserCookies(ctx, m.From.ID)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func cookieSites(data string, domains []string) []string {
	var sites []string
	for _, lg := range tools.CookieLogins(data, time.Now()) {
		sites = append(sites, lg.Site)
	}
	if len(sites) > 0 {
		return sites
	}
	for _, d := range domains {
		if strings.Count(d, ".") == 1 {
			sites = append(sites, d)
		}
	}
	if len(sites) == 0 && len(domains) > 0 {
		sites = domains[:1]
	}
	return sites
}

func cookieMainSite(data string, domains []string) string {
	sites := cookieSites(data, domains)
	for _, s := range sites {
		if s == "youtube.com" {
			return s
		}
	}
	if len(sites) > 0 {
		return sites[0]
	}
	return ""
}

func loginStatus(l lang, data string, loc *time.Location) string {
	logins := tools.CookieLogins(data, time.Now())
	if len(logins) == 0 {
		return l.t("⚪️ لاگینِ شناخته‌شده‌ای توش نیست")
	}
	var parts []string
	for _, lg := range logins {
		switch {
		case lg.Expired:
			parts = append(parts, l.f("❌ %s (لاگین تموم شده)", lg.Site))
		case lg.Expires.IsZero():
			parts = append(parts, "✅ "+lg.Site)
		default:
			parts = append(parts, l.f("✅ %s (تا %s)", lg.Site, lg.Expires.In(loc).Format("2006-01-02")))
		}
	}
	return strings.Join(parts, l.t("، "))
}

func (b *Bot) viewUserCookies(ctx context.Context, uid int64) panelView {
	l := b.lang(uid)
	list, err := b.st.UserCookies(ctx, uid)
	if err != nil {
		return panelView{html: l.t(textError)}
	}
	id := strconv.FormatInt(uid, 10)
	var sb strings.Builder
	sb.WriteString(l.t("🍪 <b>کوکی‌های من</b>\n\n"))
	sb.WriteString(l.t("اگه سایتی (یوتیوب، اینستاگرام، تیک‌تاک، توییتر…) برای دانلود لاگین بخواد، اول با کوکی‌های خودت امتحان می‌کنم. ") +
		l.t("فقط برای دانلودهای خودت استفاده می‌شن و رمزنگاری‌شده نگه داشته می‌شن.\n"))
	var rows [][]telegram.InlineKeyboardButton
	if len(list) == 0 {
		sb.WriteString(l.t("\nهنوز کوکی‌ای نذاشتی.\n"))
	}
	for i, c := range list {
		fmt.Fprintf(&sb, "\n<b>%s.</b> %s\n%s\n", l.num(i+1), esc(c.Label), loginStatus(l, c.Data, b.loc))
		rows = append(rows, row(
			b.button(l.f("🩺 تست %s", l.num(i+1)), fmt.Sprintf("ck:t:%s:%d", id, c.ID), ""),
			b.button(l.f("🗑 حذف %s", l.num(i+1)), fmt.Sprintf("ck:d:%s:%d", id, c.ID), telegram.StyleDanger)))
	}
	sb.WriteString(l.t("\n<b>چطوری بگیرمش؟</b> توی مرورگری که توی سایت لاگینی، با افزونه «Get cookies.txt LOCALLY» فایل <code>cookies.txt</code> رو بگیر و بفرست. ") +
		l.t("بهتره از یه اکانت فرعی باشه."))
	var last []telegram.InlineKeyboardButton
	if len(list) < store.MaxUserCookies {
		last = append(last, b.button(l.t("➕ افزودن کوکی"), "ck:a:"+id, telegram.StyleSuccess))
	}
	rows = append(rows, append(last, b.button(l.t("✖️ بستن"), "ck:x:"+id, "")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) handleCookiesCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		answer("", false)
		return
	}
	uid, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || uid != q.From.ID {
		answer(l.t(textNotYourPanel), true)
		return
	}
	var cookieID int64
	if len(parts) > 3 {
		cookieID, _ = strconv.ParseInt(parts[3], 10, 64)
	}
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	show := func(v panelView) { b.showCookies(ctx, chatID, msgID, v) }
	notice := ""
	switch parts[1] {
	case "a":
		answer("", false)
		b.cookieWaits.Store(uid, cookieWait{until: time.Now().Add(cookieWaitTTL), chatID: chatID, msgID: msgID})
		show(b.cookieAsk(l, uid, ""))
		return
	case "c":
		b.cookieWaits.Delete(uid)
		answer(l.t("لغو شد"), false)
	case "t":
		answer(l.t("دارم تست می‌کنم…"), false)
		list, _ := b.st.UserCookies(ctx, uid)
		for _, c := range list {
			if c.ID == cookieID {
				show(withNotice(b.viewUserCookies(ctx, uid), "⏳ "+l.t("دارم تست می‌کنم…")))
				notice = b.cookieVerdict(l, c.Data, c.Domains)
				break
			}
		}
	case "d":
		_ = b.st.DeleteUserCookie(ctx, uid, cookieID)
		answer(l.t("حذف شد"), false)
	case "x":
		answer("", false)
		_ = b.api.DeleteMessage(ctx, chatID, msgID)
		return
	default:
		answer("", false)
	}
	show(withNotice(b.viewUserCookies(ctx, uid), notice))
}

func (b *Bot) cookieVerdict(l lang, data string, domains []string) string {
	site := cookieMainSite(data, domains)
	if site == "" {
		return l.t("⚠️ سایتی توی این کوکی پیدا نکردم.")
	}
	ctx, cancel := b.ctx(2 * time.Minute)
	defer cancel()
	res := b.app.Registry.TestCookie(ctx, data, site)
	switch {
	case res.OK && res.Live:
		return l.f("🩺 %s: ✅ کار می‌کنه (%s)", esc(site), esc(res.Detail))
	case res.OK:
		return l.f("🩺 %s: ✅ لاگین توشه", esc(site))
	}
	return l.f("🩺 %s: ❌ کار نمی‌کنه: %s", esc(site), esc(res.Detail))
}

func (b *Bot) cookieInput(ctx context.Context, m *telegram.Message, cmd string) bool {
	v, ok := b.cookieWaits.Load(m.From.ID)
	if !ok || !m.Chat.Private() {
		return false
	}
	w := v.(cookieWait)
	if time.Now().After(w.until) {
		b.cookieWaits.Delete(m.From.ID)
		return false
	}
	l := b.langOf(m.From)
	uid := m.From.ID
	show := func(v panelView) { w.msgID = b.showCookies(ctx, w.chatID, w.msgID, v) }
	ask := func(problem string) bool {
		show(b.cookieAsk(l, uid, problem))
		b.cookieWaits.Store(uid, w)
		return true
	}
	switch {
	case cmd == "/cancel":
		b.cookieWaits.Delete(uid)
		_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
		show(withNotice(b.viewUserCookies(ctx, uid), l.t("باشه، بی‌خیالش شدیم.")))
		return true
	case cmd != "":
		b.cookieWaits.Delete(uid)
		return false
	}
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
	data := m.Text
	if d := m.Document; d != nil {
		raw, err := b.fetch(ctx, d.FileID, maxCookieBytes)
		if err != nil {
			return ask("❌ " + l.t("نشد فایل رو بگیرم: ") + esc(err.Error()))
		}
		data = string(raw)
	}
	data = strings.TrimPrefix(data, "\uFEFF")
	domains := tools.CookieDomains(data)
	if !looksLikeCookies(data) || len(domains) == 0 {
		return ask("❌ " + l.t("این فرمت cookies.txt (Netscape) نیست؛ دوباره بفرست."))
	}
	logins := tools.CookieLogins(data, time.Now())
	live := 0
	for _, lg := range logins {
		if !lg.Expired {
			live++
		}
	}
	switch {
	case len(logins) > 0 && live == 0:
		return ask(l.t("❌ لاگین این کوکی‌ها تموم شده؛ دوباره توی سایت لاگین کن و از نو بگیرش."))
	case len(logins) == 0 && knownLoginSite(domains):
		return ask(l.t("❌ توی این کوکی‌ها لاگینی نیست؛ اول توی سایت لاگین کن، بعد بگیرش."))
	}

	sites := map[string]bool{}
	for _, s := range cookieSites(data, domains) {
		sites[s] = true
	}
	var replace []int64
	if old, err := b.st.UserCookies(ctx, uid); err == nil {
		for _, c := range old {
			for _, s := range cookieSites(c.Data, c.Domains) {
				if sites[s] {
					replace = append(replace, c.ID)
					break
				}
			}
		}
	}
	label := cookieLabel(cookieSites(data, domains), b.st.Now().In(b.loc).Format("01-02"))
	_, err := b.st.AddUserCookie(ctx, uid, label, domains, data, replace)
	switch {
	case errors.Is(err, store.ErrTooManyCookies):
		b.cookieWaits.Delete(uid)
		show(withNotice(b.viewUserCookies(ctx, uid), "⚠️ "+l.f("حداکثر %s تا کوکی می‌تونی داشته باشی؛ اول یکی رو از /cookies پاک کن.", l.num(store.MaxUserCookies))))
		return true
	case err != nil:
		return ask("❌ " + l.t(textError))
	}
	b.cookieWaits.Delete(uid)
	saved := l.f("✅ کوکی %s ذخیره شد و از این به بعد برای دانلودهای خودت استفاده می‌شه.", esc(label))
	show(withNotice(b.viewUserCookies(ctx, uid), saved+"\n⏳ "+l.t("دارم تست می‌کنم…")))
	show(withNotice(b.viewUserCookies(ctx, uid), saved+"\n"+b.cookieVerdict(l, data, domains)))
	return true
}

func knownLoginSite(domains []string) bool {
	for _, d := range domains {
		if tools.KnownLoginSite(d) {
			return true
		}
	}
	return false
}
