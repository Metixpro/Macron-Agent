package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// statsQueryTime bounds each database part of the stats panel.
const statsQueryTime = 15 * time.Second

// adminInput is a pending multi-step admin dialog ("send me the API key…").
type adminInput struct {
	kind     string
	target   int64
	mode     string
	draft    store.Provider
	panelMsg int64
	expires  time.Time
}

const inputTTL = 10 * time.Minute

func (b *Bot) expect(adminID int64, in *adminInput) {
	in.expires = time.Now().Add(inputTTL)
	b.inputs.Store(adminID, in)
}

func (b *Bot) pending(adminID int64) *adminInput {
	v, ok := b.inputs.Load(adminID)
	if !ok {
		return nil
	}
	in := v.(*adminInput)
	if time.Now().After(in.expires) {
		b.inputs.Delete(adminID)
		return nil
	}
	return in
}

// panelView is one screen of the panel.
type panelView struct {
	html string
	kb   *telegram.InlineKeyboardMarkup
}

func (b *Bot) back(l lang, to string) telegram.InlineKeyboardButton {
	return b.button(l.t("⬅️ برگشت"), to, "")
}

// openPanel sends a fresh panel message (from /settings).
func (b *Bot) openPanel(ctx context.Context, m *telegram.Message) {
	b.inputs.Delete(m.From.ID)
	v := b.viewHome(ctx, m.From.ID)
	b.sendHTML(ctx, m.Chat.ID, v.html, telegram.SendOptions{Markup: v.kb})
}

// showNew sends a panel screen as a new message (after the admin typed something).
func (b *Bot) showNew(ctx context.Context, chatID int64, v panelView) {
	b.sendHTML(ctx, chatID, v.html, telegram.SendOptions{Markup: v.kb})
}

func (b *Bot) handlePanel(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	chatID, msgID, admin := q.Message.Chat.ID, q.Message.MessageID, q.From.ID
	parts := strings.Split(q.Data, ":")
	show := func(v panelView) {
		if err := b.editHTML(ctx, chatID, msgID, v.html, v.kb); err != nil && !telegram.IsNotModified(err) {
			b.showNew(ctx, chatID, v)
		}
	}
	ask := func(in *adminInput, prompt string) {
		in.panelMsg = msgID
		b.expect(admin, in)
		show(panelView{html: prompt + l.t("\n\n<i>برای انصراف /cancel رو بفرست.</i>"), kb: keyboard(row(b.button(l.t("❌ انصراف"), "a:cancelinput", telegram.StyleDanger)))})
	}

	section := ""
	if len(parts) > 1 {
		section = parts[1]
	}
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	argInt := func(i int) int64 {
		n, _ := strconv.ParseInt(arg(i), 10, 64)
		return n
	}

	switch section {
	case "home":
		answer("", false)
		show(b.viewHome(ctx, admin))
	case "close":
		answer("", false)
		_ = b.api.DeleteMessage(ctx, chatID, msgID)
	case "cancelinput":
		b.inputs.Delete(admin)
		answer(l.t("لغو شد"), false)
		show(b.viewHome(ctx, admin))

	// ----- stats
	case "stats":
		answer("", false)
		show(b.viewStats(ctx, l))
	case "chart":
		answer(l.t("دارم نمودار رو می‌کشم…"), false)
		b.sendChart(ctx, chatID, admin)
	case "top":
		answer("", false)
		show(b.viewTop(ctx, l))

	// ----- the server
	case "sys", "syschart", "sysal":
		b.handleSysCallback(ctx, l, section, parts, chatID, admin, answer, show)
	// ----- the downloader
	case "dl":
		switch arg(2) {
		case "update":
			answer(l.t("دارم آپدیت می‌کنم… (تا ۳ دقیقه)"), false)
			msg := b.updateYtDlp(ctx, l)
			b.sendHTML(ctx, chatID, "⬆️ "+esc(msg), telegram.SendOptions{})
		case "cookies":
			answer("", false)
			ask(&adminInput{kind: "dl_cookies"}, l.t("🍪 فایل <code>cookies.txt</code> (فرمت Netscape) یه اکانت رو بفرست؛ با افزونه‌هایی مثل «Get cookies.txt LOCALLY» از مرورگرت بگیرش. ")+
				l.t("می‌تونی چندتا اکانت (هر بار یه فایل) اضافه کنی تا به نوبت استفاده بشن. توی کپشن فایل می‌تونی یه اسم براش بنویسی. ")+
				l.t("پیامت بعد از خوندن پاک می‌شه و کوکی‌ها رمزنگاری‌شده ذخیره می‌شن."))
			return
		case "links":
			answer("", false)
			ask(&adminInput{kind: "dl_links"}, l.t("🔗 لینک فایل‌های <code>cookies.txt</code> رو بفرست، هر خط یه لینک (مثلاً raw گیت‌هاب یا هر آدرس مستقیم)؛ ")+
				l.t("هر لینک یه کوکی جدا توی استخر می‌شه و به نوبت استفاده می‌شن. هر وقت یکی بلاک بشه یا لاگینش تموم بشه، دوباره از لینکش گرفته می‌شه."))
			return
		case "test":
			answer(l.t("دارم تست می‌کنم… جوابش توی یه پیام جدا می‌رسه."), false)
			tctx, tcancel := b.ctx(5 * time.Minute)
			defer tcancel()
			b.sendHTML(tctx, chatID, b.testCookies(tctx, l), telegram.SendOptions{})
			b.showNew(tctx, chatID, b.viewDownloader(l)) // the test takes longer than the button's context
			return
		case "delck":
			_ = b.st.DeleteCookie(ctx, argInt(3))
			answer(l.t("حذف شد"), false)
		default:
			answer("", false)
		}
		show(b.viewDownloader(l))

	// ----- web search (treg.to)
	case "sr":
		switch arg(2) {
		case "token":
			answer("", false)
			ask(&adminInput{kind: "sr_token"}, l.t("🔑 توکن treg.to رو بفرست (از داشبورد treg بعد از ورود). پیامت بعد از خوندن پاک می‌شه و توکن رمزنگاری‌شده ذخیره می‌شه."))
			return
		case "test":
			answer(l.t("دارم تست می‌کنم…"), false)
			b.sendHTML(ctx, chatID, b.testSearch(ctx, l, b.app.Search.Treg.Token()), telegram.SendOptions{})
		case "del":
			_ = b.st.SetSecretSetting(ctx, settingTregToken, "")
			answer(l.t("حذف شد"), false)
		default:
			answer("", false)
		}
		show(b.viewSearch(l))

	// ----- the soul (SOUL.md)
	case "soul":
		switch arg(2) {
		case "get":
			answer("", false)
			b.sendSoulFile(ctx, l, chatID)
			return
		case "set":
			answer("", false)
			ask(&adminInput{kind: "soul"}, l.t("🧬 فایل <b>SOUL.md</b> جدید رو بفرست (یا متنش رو مستقیم). جاهایی که <code>{{name}}</code> (اسم لاتین)، <code>{{name_fa}}</code> (اسم فارسی)، <code>{{creator}}</code> و <code>{{creator_handle}}</code> بیاد، با اسم ربات و سازنده پر می‌شن.\n\nقانون‌های پایه (لو ندادن مدل، رد کارهای خطرناک، محرمانه بودن تنظیمات) همیشه سر جاشونن، هر چی توی این فایل باشه."))
			return
		case "reset":
			if err := b.brain.ResetSoul(); err != nil {
				answer(l.t(textError), true)
				return
			}
			answer(l.t("روح پیش‌فرض برگشت 🧬"), false)
		default:
			answer("", false)
		}
		show(b.viewSoul(l))

	// ----- settings
	case "st":
		switch arg(2) {
		case "remoji":
			i, err := strconv.Atoi(arg(3))
			if err != nil || i < 0 || i >= len(reactionEmojis) {
				answer("", false)
				break
			}
			_ = b.st.SetSetting(ctx, store.SettingReactionEmoji, reactionEmojis[i])
			answer(reactionEmojis[i], false)
		case "react", "premium", "failover", "trace", "gapprove":
			key := map[string]string{"react": store.SettingReactions, "premium": store.SettingPremiumEmoji, "failover": store.SettingFailover,
				"trace": store.SettingLiveTrace, "gapprove": store.SettingGroupApproval}[arg(2)]
			def := map[string]bool{store.SettingReactions: true, store.SettingFailover: true,
				store.SettingLiveTrace: true, store.SettingGroupApproval: true}[key]
			_ = b.st.SetSetting(ctx, key, strconv.FormatBool(!b.st.SettingBool(key, def)))
			answer(l.t("انجام شد"), false)
		case "steps":
			answer("", false)
			ask(&adminInput{kind: "st_steps"}, l.f("🤖 قدم‌ها در هر دور: %d تا %d\n<code>0</code> = پیش‌فرض سرور", minAgentSteps, config.MaxAgentSteps))
			return
		case "minutes":
			answer("", false)
			ask(&adminInput{kind: "st_minutes"}, l.f("⏱ حداکثر زمان هر درخواست رو به دقیقه بفرست (۱ تا %d). <code>0</code> = پیش‌فرض سرور:", config.MaxRunMinutes))
			return
		case "ctx":
			answer("", false)
			ask(&adminInput{kind: "st_ctx"}, l.f("🧠 حافظهٔ گفتگو (توکن)\n<code>0</code> = خودکار (%s)\nبازهٔ دستی: %s تا %s",
				humanInt(agent.FillHistoryTokens(b.contextWindowNow())), humanInt(memory.MinBudget), humanInt(memory.MaxBudget)))
			return
		case "window":
			answer("", false)
			ask(&adminInput{kind: "st_window"}, l.t("🪟 پنجره context (توکن)\nمثال: <code>256k</code> یا <code>1m</code>\n<code>0</code> = خودکار"))
			return
		case "auto":
			next, ok := autoSteps[b.autoContinue()]
			if !ok {
				next = autoForever
			}
			_ = b.st.SetSetting(ctx, store.SettingAutoContinue, strconv.Itoa(next))
			answer(autoName(l, next), false)
		default:
			answer("", false)
		}
		switch {
		case arg(3) == "em":
			show(b.viewEmoji(ctx, l))
		case arg(3) == "pv":
			show(b.viewProviders(ctx, l))
		case arg(3) == "gr":
			show(b.viewGroups(ctx, l))
		case arg(3) == "ws":
			show(b.viewShowWork(l))
		case arg(2) == "auto":
			show(b.viewAgent(l))
		default:
			show(b.viewSettings(l))
		}

	case "ws":
		answer("", false)
		show(b.viewShowWork(l))
	case "gate":
		b.handleGatePanel(ctx, l, parts, answer, show)
	case "moa":
		b.handleMoaPanel(ctx, l, parts, answer, show, ask)
	case "upd":
		answer("", false)
		show(b.updateView(ctx, l, "a:home"))

	// ----- the agent and its tools
	case "ai":
		answer("", false)
		show(b.viewAgent(l))

	case "learn":
		b.handleLearnPanel(ctx, l, admin, parts, answer, show, ask)

	// ----- other sections
	case "pv":
		b.handleProviderPanel(ctx, q, parts, answer, show, ask)
	case "rt":
		b.handleRouterPanel(ctx, l, parts, answer, show, ask)
	case "tri":
		b.handleTriagePanel(ctx, l, parts, answer, show, ask)
	case "em":
		b.handleEmojiPanel(ctx, q, parts, answer, show, ask)
	case "gr":
		b.handleGroupPanel(ctx, q, parts, answer, show, ask)
	case "ie":
		b.handleImageEditPanel(ctx, q, parts, answer, show, ask)
	case "ig":
		b.handleImageGenPanel(ctx, q, parts, answer, show, ask)
	case "mu":
		b.handleMusicPanel(ctx, q, parts, answer, show, ask)
	case "stt":
		if b.sttCallback(ctx, l, arg(2), chatID, answer, ask) {
			return
		}
		show(b.viewSTT(l))
	default:
		answer("", false)
	}
}

// handleAdminInput consumes a message that answers a pending dialog.
func (b *Bot) handleAdminInput(ctx context.Context, m *telegram.Message) bool {
	l := b.langOf(m.From)
	in := b.pending(m.From.ID)
	if in == nil {
		return false
	}
	text := strings.TrimSpace(m.Text)
	if text == "/cancel" {
		b.inputs.Delete(m.From.ID)
		b.reply(m, l.t("باشه، بی‌خیالش شدیم."))
		b.showNew(ctx, m.Chat.ID, b.viewHome(ctx, m.From.ID))
		return true
	}
	if strings.HasPrefix(text, "/") && in.kind != "bc_msg" && in.kind != "user_msg" && in.kind != "plg_mcp" && in.kind != "plg_env" {
		b.inputs.Delete(m.From.ID) // another command: abandon the dialog
		return false
	}
	done := func(v panelView) {
		b.inputs.Delete(m.From.ID)
		b.showNew(ctx, m.Chat.ID, v)
	}

	switch in.kind {
	case "stt_set":
		if b.sttInput(ctx, l, m) {
			done(b.viewSTT(l))
		}
	case "sr_token":
		_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // a token is a password: never leave it in the chat
		token := strings.TrimSpace(m.Text)
		if token == "" || strings.ContainsAny(token, " \n\t") || len(token) > 300 {
			b.reply(m, l.t("این شبیه توکن نیست؛ دوباره بفرست."))
			return true
		}
		verdict := b.testSearch(ctx, l, token)
		if strings.HasPrefix(verdict, "❌") {
			b.reply(m, verdict)
			return true
		}
		if err := b.st.SetSecretSetting(ctx, settingTregToken, token); err != nil {
			b.reply(m, l.t(textError))
			return true
		}
		b.reply(m, l.t("✅ توکن treg ذخیره شد؛ از حالا جستجوها اول با treg انجام می‌شن.")+"\n"+verdict)
		done(b.viewSearch(l))
	case "dl_cookies":
		cookies := m.Text
		if d := m.Document; d != nil {
			data, err := b.fetch(ctx, d.FileID, 1<<20)
			if err != nil {
				b.reply(m, l.t("نشد فایل رو بگیرم: ")+esc(err.Error()))
				return true
			}
			cookies = string(data)
		}
		_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // cookies are a login: never leave them in the chat
		domains := tools.CookieDomains(cookies)
		if !looksLikeCookies(cookies) || len(domains) == 0 {
			b.reply(m, l.t("این فرمت cookies.txt (Netscape) نیست؛ دوباره بفرست."))
			return true
		}
		label := strings.TrimSpace(textutil.OneLine(m.Caption))
		if label == "" {
			label = cookieLabel(domains, b.st.Now().In(b.loc).Format("01-02"))
		}
		id, err := b.st.AddCookie(ctx, label, domains, cookies)
		if err != nil {
			b.reply(m, l.t(textError))
			return true
		}
		b.reply(m, l.f("✅ کوکی #%d (%s) به استخر اضافه شد. سایت‌ها: %s", id, esc(label), esc(textutil.TruncateRunes(strings.Join(domains, "، "), 200, "…")))+
			"\n"+b.cookieVerdict(l, cookies, domains))
		done(b.viewDownloader(l))
	case "dl_links":
		report, added := b.addCookieLinks(ctx, l, m.Text)
		b.reply(m, report)
		if added {
			done(b.viewDownloader(l))
		}
	case "soul":
		soul := m.Text
		if d := m.Document; d != nil {
			ext := strings.ToLower(filepath.Ext(d.FileName))
			if ext != ".md" && ext != ".txt" && ext != "" {
				b.reply(m, l.t("یه فایل <code>.md</code> یا <code>.txt</code> بفرست."))
				return true
			}
			data, err := b.fetch(ctx, d.FileID, 256<<10)
			if err != nil {
				b.reply(m, l.t("نشد فایل رو بگیرم: ")+esc(err.Error()))
				return true
			}
			soul = string(data)
		}
		soul = strings.TrimSpace(strings.TrimPrefix(soul, "\uFEFF"))
		switch n := len([]rune(soul)); {
		case n == 0 || !utf8.ValidString(soul):
			b.reply(m, l.t("متن خالی یا خراب بود؛ دوباره بفرست."))
			return true
		case n > maxSoulRunes:
			b.reply(m, l.f("خیلی بلنده (%d کاراکتر)؛ حداکثر %d. هر چی کوتاه‌تر، هر پیام ارزون‌تر و سریع‌تر.", n, maxSoulRunes))
			return true
		}
		if err := b.brain.SetSoul(soul); err != nil {
			b.reply(m, "❌ "+esc(err.Error()))
			return true
		}
		b.reply(m, l.t("✅ روح جدید از پیام بعدی فعاله."))
		done(b.viewSoul(l))
	case "rt_test":
		if text == "" {
			b.reply(m, l.t("یه پیام متنی بفرست."))
			return true
		}
		done(b.viewRouterTest(l, text))
	case "moa_name":
		b.moaNameInput(ctx, l, m, done)
	case "tri_test":
		if text == "" {
			b.reply(m, l.t("یه پیام متنی بفرست."))
			return true
		}
		done(b.viewTriageTest(ctx, l, text))
	case "st_window":
		n, ok := parseTokens(text)
		if !ok {
			b.reply(m, l.t("یه عدد بین ۸۰۰۰ و ۱۰ میلیون بفرست (مثلاً <code>256k</code>)، یا <code>0</code> برای خودکار."))
			return true
		}
		_ = b.st.SetSetting(ctx, settingContextWindow, strconv.Itoa(n))
		done(b.viewAgent(l))
	case "st_steps", "st_minutes":
		lo, hi, key := minAgentSteps, config.MaxAgentSteps, store.SettingAgentSteps
		if in.kind == "st_minutes" {
			lo, hi, key = 1, config.MaxRunMinutes, store.SettingAgentMinutes
		}
		n, err := strconv.Atoi(strings.TrimSpace(textutil.Fold(text)))
		if err != nil || (n != 0 && (n < lo || n > hi)) {
			b.reply(m, l.f("یه عدد بین %d و %d بفرست (یا 0).", lo, hi))
			return true
		}
		_ = b.st.SetSetting(ctx, key, strconv.Itoa(n))
		done(b.viewAgent(l))
	case "st_ctx":
		n, err := strconv.Atoi(strings.ReplaceAll(strings.TrimSpace(textutil.Fold(text)), ",", ""))
		if err != nil || (n != 0 && (n < memory.MinBudget || n > memory.MaxBudget)) {
			b.reply(m, l.f("یه عدد بین %s و %s بفرست، یا <code>0</code> برای خودکار.", humanInt(memory.MinBudget), humanInt(memory.MaxBudget)))
			return true
		}
		_ = b.st.SetSetting(ctx, store.SettingHistoryTokens, strconv.Itoa(n))
		done(b.viewAgent(l))
	case "gr_log":
		v, err := b.setLogChat(ctx, l, m)
		if err != nil {
			b.reply(m, esc(err.Error())+l.t("\n\n<i>دوباره بفرست، یا /cancel</i>"))
			return true
		}
		b.reply(m, l.t("✅ کانال لاگ تنظیم شد."))
		done(v)
	case "ln_edit":
		b.learnInput(ctx, l, m, in, done)
	case "ln_teach":
		b.teachInput(ctx, l, m)
	default:
		return b.handleSectionInput(ctx, m, in, done)
	}
	return true
}

func onOff(l lang, v bool) string {
	if v {
		return l.t("روشن ✅")
	}
	return l.t("خاموش ⚪️")
}

func ago(l lang, t *time.Time) string {
	if t == nil {
		return l.t("هیچ‌وقت")
	}
	d := time.Since(*t)
	switch {
	case d < time.Minute:
		return l.t("همین الان")
	case d < time.Hour:
		return l.f("%d دقیقه پیش", int(d.Minutes()))
	case d < 24*time.Hour:
		return l.f("%d ساعت پیش", int(d.Hours()))
	}
	return l.f("%d روز پیش", int(d.Hours()/24))
}

func uptime(l lang, d time.Duration) string {
	d = d.Round(time.Minute)
	days := int(d.Hours()) / 24
	if days > 0 {
		return l.f("%d روز و %d ساعت", days, int(d.Hours())%24)
	}
	return l.f("%d ساعت و %d دقیقه", int(d.Hours()), int(d.Minutes())%60)
}

// ---------------------------------------------------------------- home

// ---------------------------------------------------------------- stats

func (b *Bot) viewStats(ctx context.Context, l lang) panelView {
	// Each part has its own time: a slow table or disk never leaves the
	// button without an answer.
	var (
		o      *store.Overview
		err    error
		dbSize int64
		wg     sync.WaitGroup
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		qctx, cancel := context.WithTimeout(ctx, statsQueryTime)
		defer cancel()
		o, err = b.st.Overview(qctx)
	}()
	go func() {
		defer wg.Done()
		qctx, cancel := context.WithTimeout(ctx, statsQueryTime)
		defer cancel()
		dbSize, _ = b.st.DatabaseSize(qctx)
	}()
	wg.Wait()
	if err != nil {
		return panelView{html: l.t("خواندن آمار خطا داد: ") + esc(err.Error()), kb: keyboard(row(b.button(l.t("🔄 دوباره"), "a:stats", ""), b.back(l, "a:home")))}
	}
	ws := b.usage.get(b.app.Machine.BaseDir, usageFresh, dirUsage)
	wsFiles, wsSize := ws.Files, ws.Size
	wsNote := ""
	switch {
	case ws.At.IsZero():
		wsNote = l.t(" · دارم اندازه می‌گیرم، چند لحظه بعد «🔄 بروزرسانی» رو بزن")
	case ws.Walked:
		wsNote = l.f(" · اندازه‌گیری %s؛ دوباره در حال اندازه‌گیری", ago(l, &ws.At))
	}
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	html := l.f(`📊 <b>آمار کامل</b>

<b>پیام‌ها</b>
• امروز: <b>%d</b> · ۷ روز: %d · کل: %d

<b>مدل زبانی (امروز)</b>
• توکن ورودی: %s · خروجی: %s
• پروایدرها: %d (فعال: %d)

<b>حافظه و فایل</b>
• پیام‌های توی پنجره گفتگو: %d
• فایل‌های من: %d (%s)%s
• دیتابیس: %s

<b>سیستم</b>
• %s
• رم ربات: %s · گوروتین: %d`,
		o.RequestsToday, o.Requests7d, o.RequestsTotal,
		humanInt(int(o.TokensTodayPrompt)), humanInt(int(o.TokensTodayCompletion)), o.Providers, o.ProvidersEnabled,
		o.MemoryMessages, wsFiles, sizeText(wsSize), wsNote, sizeText(dbSize),
		b.sysLine(l), sizeText(int64(ms.Sys)), runtime.NumGoroutine())
	return panelView{html: html, kb: keyboard(
		row(b.button(l.t("📈 نمودار ۱۴ روز"), "a:chart", telegram.StylePrimary), b.button(l.t("🏆 برترین‌ها"), "a:top", "")),
		row(b.button(l.t("🖥 سرور (CPU و RAM)"), "a:sys:1h", telegram.StylePrimary)),
		row(b.button(l.t("🔄 بروزرسانی"), "a:stats", ""), b.back(l, "a:home")),
	)}
}

func (b *Bot) viewTop(ctx context.Context, l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🏆 <b>برترین‌ها</b>\n\n<b>فعال‌ترین کاربرهای امروز</b>\n"))
	users, _ := b.st.TopUsersToday(ctx, 10)
	if len(users) == 0 {
		sb.WriteString(l.t("هنوز کسی پیام نداده.\n"))
	}
	for i, u := range users {
		fmt.Fprint(&sb, l.f("%d. %s — %d پیام (<code>%d</code>)\n", i+1, esc(u.Name), u.Count, u.ID))
	}
	sb.WriteString(l.t("\n<b>پرکاربردترین قابلیت‌ها (۷ روز)</b>\n"))
	tools, _ := b.st.TopStats(ctx, store.StatTool, 7, 10)
	for i, t := range tools {
		fmt.Fprint(&sb, l.f("%d. <code>%s</code> — %d بار", i+1, esc(t.Name), t.Count))
		if t.Failures > 0 {
			fmt.Fprint(&sb, l.f(" (%d خطا)", t.Failures))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(l.t("\n<b>پروایدرها (۷ روز)</b>\n"))
	provs, _ := b.st.TopStats(ctx, store.StatProvider, 7, 10)
	for _, p := range provs {
		fmt.Fprint(&sb, l.f("• %s — %d درخواست، %s توکن، %d خطا\n", esc(p.Name), p.Count, humanInt(int(p.Tokens)), p.Failures))
	}
	return panelView{html: sb.String(), kb: keyboard(row(b.back(l, "a:stats")))}
}

// sendChart renders the 14-day activity chart here and sends it.
func (b *Bot) sendChart(ctx context.Context, chatID, admin int64) {
	l := b.lang(admin)
	series, err := b.st.DailySeries(ctx, 14)
	if err != nil {
		b.sendHTML(ctx, chatID, l.t("خطا: ")+esc(err.Error()), telegram.SendOptions{})
		return
	}
	labels := make([]string, len(series))
	values := make([]float64, len(series))
	var newUsers, active int
	for i, p := range series {
		labels[i] = p.Day[5:]
		values[i] = float64(p.Requests)
		newUsers += p.NewUsers
		active = max(active, p.Users)
	}
	session := fmt.Sprintf("admin-%d", admin)
	args, _ := json.Marshal(map[string]any{"chart_type": "bar", "title": "Messages per day (last 14 days)", "labels": labels, "values": values, "filename": "stats.png"})
	out, err := b.app.Registry.Execute(ctx, session, "generate_chart", string(args))
	if err != nil || !strings.Contains(out, "Chart created") {
		b.sendHTML(ctx, chatID, l.t("نمودار ساخته نشد: ")+esc(strings.TrimSpace(out)), telegram.SendOptions{})
		return
	}
	f, _, err := b.app.Machine.Open(session, "stats.png")
	if err != nil {
		return
	}
	defer func() { f.Close(); _ = b.app.Machine.Remove(session, "stats.png") }()
	caption := l.f("📈 پیام‌های ۱۴ روز اخیر\n👥 کاربر جدید: %d · بیشترین فعال در یک روز: %d", newUsers, active)
	_ = b.api.SendPhoto(ctx, chatID, telegram.File{Name: "stats.png", Reader: f}, caption)
}

// ---------------------------------------------------------------- users

const usersPerPage = 8

// ---------------------------------------------------------------- settings and admins

// reactionEmojis are the reactions offered for a message that was read.
var reactionEmojis = []string{"👀", "👍", "✍", "🫡", "🤔", "⚡"}

// seenReaction is the reaction a message gets when it is read.
func (b *Bot) seenReaction() string {
	if v, ok := b.st.Setting(store.SettingReactionEmoji); ok {
		for _, e := range reactionEmojis {
			if e == v {
				return v
			}
		}
	}
	return reactionEmojis[0]
}

// viewSettings is the settings page: the reactions, nothing else (how the
// work is shown lives in 🦾 the agent and its tools → 🖥 showing the work).
func (b *Bot) viewSettings(l lang) panelView {
	on := b.st.SettingBool(store.SettingReactions, true)
	html := l.f("⚙️ <b>تنظیمات</b>\n\n😍 ری‌اکشن روی پیام‌ها: %s\nایموجی پیش‌فرض (وقتی پیام خونده شد): %s", onOff(l, on), b.seenReaction())
	toggle := l.t("😍 ری‌اکشن: روشن")
	if !on {
		toggle = l.t("😍 ری‌اکشن: خاموش")
	}
	var emojis []telegram.InlineKeyboardButton
	for i, e := range reactionEmojis {
		label := e
		if e == b.seenReaction() {
			label = "✅ " + e
		}
		emojis = append(emojis, b.button(label, "a:st:remoji:"+strconv.Itoa(i), ""))
	}
	return panelView{html: html, kb: keyboard(
		row(b.button(toggle, "a:st:react", "")),
		row(emojis...),
		row(b.back(l, "a:home")),
	)}
}

// viewShowWork controls progress visibility.
func (b *Bot) viewShowWork(l lang) panelView {
	rows := [][]telegram.InlineKeyboardButton{row(b.button(l.t("🖥 ترمینال زنده"), "a:st:trace:ws", ""))}
	if b.st.SettingBool(store.SettingPremiumEmoji, false) {
		rows = append(rows, row(b.button(l.t("✨ ایموجی پریمیوم"), "a:em", "")))
	}
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: l.f("🖥 <b>نمایش کار</b>\n\nنمایش مراحل: %s", onOff(l, b.st.SettingBool(store.SettingLiveTrace, true))), kb: keyboard(rows...)}
}

// autoName says how far a job goes on by itself.
func autoName(l lang, n int) string {
	switch n {
	case autoForever:
		return l.t("⏩ تا تمام شدن کار (بی‌نهایت)")
	case autoOff:
		return l.t("⏩ ادامه خودکار: خاموش")
	}
	return l.f("⏩ تا %d تمدید", n)
}

// viewAgent is everything about how the agent works: its budget, how much
// of the conversation it gets, its soul, skills, plugins and tools.
func (b *Bot) viewAgent(l lang) panelView {
	ctxTokens := b.historyTokens()
	ctxMode := l.t("خودکار")
	if b.st.SettingInt(store.SettingHistoryTokens, 0) > 0 {
		ctxMode = l.t("دستی")
	}
	steps, timeout := b.agentBudget()
	html := l.f("🦾 <b>ایجنت و ابزارها</b>\n\n🤖 ادامهٔ کار: %s\nقدم‌ها: %d · زمان: %d دقیقه · توقف بی‌کاری: %d دقیقه",
		autoName(l, b.autoContinue()), steps, int(timeout.Minutes()), int(b.idleTimeout().Minutes()))
	if w := b.app.Agent.ContextWindow; w != nil {
		html += l.f("\n🪟 پنجره context مدل: <b>%s</b> توکن", agent.HumanTokens(w()))
	}
	html += l.f("\n🧠 گفتگوی همراه هر درخواست: <b>%s</b> توکن (%s)", humanInt(ctxTokens), ctxMode)
	html += l.f("\n🧬 روح ربات (SOUL.md): %s", b.soulSource(l))
	if se := b.app.Search; se != nil {
		html += l.f("\n🔎 جستجوی وب: treg.to %s", onOff(l, se.Treg.Token() != ""))
	}
	html += l.f("\n🎨 ساخت عکس با استایل: %s", onOff(l, b.app.Registry.StudioReady()))
	html += l.f("\n🎤 ساخت آهنگ با خواننده: %s", onOff(l, b.app.Registry.SongReady()))
	if b.app.Registry.Has(tools.ToolEditImage) {
		html += l.f("\n🖼 ویرایش عکس با هوش مصنوعی: %s", onOff(l, b.app.Registry.ImageEditKey() != ""))
	}
	html += l.f("\n🎙 صدا به متن (ویس‌ها): %s", onOff(l, b.app.Registry.CanTranscribe()))
	return panelView{html: html, kb: keyboard(
		row(b.button(l.t("🤖 قدم‌های ایجنت"), "a:st:steps", ""), b.button(l.t("⏱ زمان هر درخواست"), "a:st:minutes", "")),
		row(b.button(l.t("⏩ ادامه خودکار"), "a:st:auto", ""), b.button(l.t("🪟 پنجره context"), "a:st:window", "")),
		row(b.button(l.t("🧠 اندازه حافظه"), "a:st:ctx", ""), b.button(l.t("🧬 روح ربات"), "a:soul", "")),
		row(b.button(l.t("🔎 جستجوی وب"), "a:sr", ""), b.button(l.t("📥 دانلودر"), "a:dl", "")),
		row(b.button(l.t("🎙 صدا به متن"), "a:stt", ""), b.button(l.t("🎨 ساخت عکس"), "a:ig", "")),
		row(b.button(l.t("🖼 ویرایش عکس"), "a:ie", ""), b.button(l.t("🎤 ساخت آهنگ"), "a:mu", "")),
		row(b.button(l.t("🖥 نمایش کار"), "a:ws", ""), b.button(l.t("🛡 گیت دستورات"), "a:gate", "")),
		row(b.button(l.t("⚖️ رأی‌گیری چندمدلی"), "a:moa", "")),
		row(b.back(l, "a:home")),
	)}
}

// menuLangs are the command menus: English by default, Persian for Persian apps.
var menuLangs = []struct {
	l    lang
	code string
}{{en, ""}, {fa, "fa"}}

// registerCommands sets the command menus of users, groups and admins.
func (b *Bot) registerCommands(ctx context.Context) error {
	var first error
	for _, m := range menuLangs {
		if err := b.api.SetMyCommandsFor(ctx, b.userCommands(m.l), nil, m.code); err != nil && first == nil {
			first = err
		}
		_ = b.api.SetMyCommandsFor(ctx, groupCommands(m.l), &telegram.BotCommandScope{Type: "all_group_chats"}, m.code)
	}
	b.registerAdminCommands(ctx)
	return first
}

// registerAdminCommands shows /settings in the command menu of every admin
// (/admin still works, unlisted).
func (b *Bot) registerAdminCommands(ctx context.Context) {
	ids, _ := b.st.Admins(ctx)
	for id := range b.owner {
		ids = append(ids, id)
	}
	for _, m := range menuLangs {
		cmds := append(b.userCommands(m.l), telegram.BotCommand{Command: "settings", Description: m.l.t("پنل مدیریت")},
			telegram.BotCommand{Command: "teach", Description: m.l.t("یاد دادن یه درس به ربات")})
		for _, id := range ids {
			_ = b.api.SetMyCommandsFor(ctx, cmds, &telegram.BotCommandScope{Type: "chat", ChatID: id}, m.code)
		}
	}
}

// groupCommands is the command menu inside groups.
func groupCommands(l lang) []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "recap", Description: l.t("چی از دستم رفت؟ (خلاصه گفتگو)")},
		{Command: "nolog", Description: l.t("پیام‌هام برای خلاصه نگه داشته نشه")},
		{Command: "groupsettings", Description: l.t("تنظیمات گروه (برای ادمین‌ها)")},
		{Command: "mode", Description: l.t("حالت جواب: ⚡ سریع / 🧠 عمیق")},
		{Command: "help", Description: l.t("راهنما")},
	}
}

func (b *Bot) userCommands(l lang) []telegram.BotCommand { return personalCommands(l) }

// ---------------------------------------------------------------- the soul (SOUL.md)

const maxSoulRunes = 30000

// The soul is the file SOUL.md in the agent's home folder (pkg/brain): the
// owner may edit it with any editor; this panel is the same thing from Telegram.
func (b *Bot) soulSource(l lang) string {
	if b.brain.IsCustomSoul() {
		return l.f("سفارشی (%s کاراکتر)", humanInt(len([]rune(b.brain.Soul()))))
	}
	return l.t("پیش‌فرض")
}

func (b *Bot) currentSoul() string { return b.brain.SoulText() }

func (b *Bot) viewSoul(l lang) panelView {
	html := l.t("🧬 <b>روح ربات (SOUL.md)</b>\n\nشخصیت، لحن، توانایی‌ها، ارزش‌ها و خط قرمزهای ربات توی این فایله و سر هر پیام به مدل داده می‌شه.\n\n") +
		l.t("📄 الان: <b>") + b.soulSource(l) + l.t("</b>\n\nبرای تغییر: فایل فعلی رو بگیر، ویرایشش کن و دوباره بفرست. قانون‌های پایه (لو ندادن مدل زبانی، رد کارهای خطرناک، محرمانه بودن تنظیمات) همیشه برقرارن.")
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("📥 دریافت SOUL.md"), "a:soul:get", telegram.StylePrimary), b.button(l.t("📤 جایگزینی"), "a:soul:set", "")),
	}
	if b.brain.IsCustomSoul() {
		rows = append(rows, row(b.button(l.t("♻️ برگشت به پیش‌فرض"), "a:soul:reset", telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: html, kb: keyboard(rows...)}
}

func (b *Bot) sendSoulFile(ctx context.Context, l lang, chatID int64) {
	f := telegram.File{Name: "SOUL.md", Reader: strings.NewReader(b.currentSoul())}
	if err := b.api.SendDocumentOpts(ctx, chatID, f, l.t("🧬 روح فعلی ربات · ")+b.soulSource(l), telegram.SendOptions{}); err != nil && !telegram.IsUnreachable(err) {
		b.sendHTML(ctx, chatID, l.t("نشد فایل رو بفرستم: ")+esc(err.Error()), telegram.SendOptions{})
	}
}
