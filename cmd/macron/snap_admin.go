package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/snap"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The admin's Snap panel (a:sn): the switch, the key, the model, how long a
// request waits for it, which decisions it makes, a test, and its week.

// snapTimeouts are the waits offered, in milliseconds.
var snapTimeouts = []int{800, 1500, 3000}

// snapDecisions are the decisions the admin can turn off one by one.
var snapDecisions = []struct{ key, setting string }{
	{"route", settingSnapRoute},
	{"effort", settingSnapEffort},
	{"chat", settingSnapChat},
}

func (b *Bot) viewSnap(ctx context.Context, l lang) panelView {
	var sb strings.Builder
	on := b.st.SettingBool(settingSnapOn, false)
	sb.WriteString(l.t("⚡ <b>Snap: تصمیم‌گیر سریع</b>\n\nقبل از اینکه پیام به مدل زبانی برسه، Snap (مدل تصمیم‌گیری SubQ) در کسری از ثانیه می‌خونتش و می‌گه چه کاریه، چقدر کار داره، ابزار لازم داره یا نه، به چقدر از گفتگو تکیه داره و کدوم ابزارها به کارش میان. تصمیم با Snap، انجام کار با مدل زبانی؛ هزینه Snap فقط ورودیه و حدود یک‌صدم مدل زبانی.\n\n"))
	sb.WriteString(l.f("وضعیت: <b>%s</b>\n", onOff(l, on)))
	switch {
	case b.snapPanelKey() != "":
		sb.WriteString(l.t("🔑 کلید: تنظیم شده (رمزنگاری‌شده)\n"))
	case b.app.Config.SnapKey != "":
		sb.WriteString(l.t("🔑 کلید: از متغیر محیطی WAZE_SNAP_KEY\n"))
	default:
		sb.WriteString(l.t("🔑 کلید: تنظیم نشده؛ تا تنظیم نشه Snap کار نمی‌کنه\n"))
	}
	sb.WriteString(l.f("🧠 مدل: <code>%s</code> · ⏱ حداکثر انتظار: %s\n", esc(b.snapModel()), snapSeconds(l, b.snapTimeout())))
	if till := b.snap.pausedTill.Load(); till > 0 && time.Now().UnixNano() < till {
		sb.WriteString(l.f("⏸ بعد از چند خطای پشت‌سرهم تا %s استراحت می‌کنه؛ تا اون موقع درخواست‌ها مثل قبل و بدون Snap می‌رن.\n", time.Unix(0, till).In(b.loc).Format("15:04")))
	}

	sb.WriteString(l.t("\n<b>تصمیم‌ها</b>\n"))
	sb.WriteString(l.f("🧭 مدل قوی برای کار سخت: %s — کار سختی که از کلمات پیام معلوم نیست هم پیش مدل قوی می‌ره؛ Snap هیچ‌وقت درخواستی رو پیش مدل ضعیف‌تر نمی‌فرسته\n", onOff(l, b.st.SettingBool(settingSnapRoute, true))))
	sb.WriteString(l.f("💬 گپ بدون ابزار: %s — فقط سلام، تشکر و گپ کوتاه و مستقل، با پرامپت سبک و بدون تعریف ابزارها. پیش‌فرض خاموشه: پیامی که گپ خونده بشه نمی‌تونه چیزی جستجو یا دانلود کنه\n", onOff(l, b.snapDecides(settingSnapChat))))
	sb.WriteString(l.f("🎚 فکر کم برای گپ: %s — فقط همون گپ‌ها با effort پایین؛ هر کاری effort خود پروایدر رو نگه می‌داره\n", onOff(l, b.st.SettingBool(settingSnapEffort, true))))
	sb.WriteString(l.f("✂️ کوتاه کردن تاریخچه و ابزارها: <b>%s</b> — %s\n", snapTrimName(l, b.snapTrimMode()), snapTrimAbout(l, b.snapTrimMode())))
	sb.WriteString(l.t("🛡 وسط یه کار (پیام‌های قبلی ابزار زدن یا فایل ساختن) هیچ‌کدوم از این‌ها اعمال نمی‌شه، جز رفتن پیش مدل قوی.\n"))

	sb.WriteString(b.snapCacheLines(l))
	sb.WriteString(b.snapWeek(ctx, l))
	sb.WriteString(l.t("\n\n🔒 <b>حریم خصوصی:</b> متن پیام و چند پیام آخر گفتگو برای SubQ فرستاده می‌شه (به گفته خودشون تا ۶۰ روز نگه می‌دارن؛ نگه‌نداشتن کامل یا ZDR رو هم می‌شه ازشون خواست). پروایدرهای خود کاربر (Custom)، کارهای پس‌زمینه و زمان‌بندی‌شده هیچ‌وقت فرستاده نمی‌شن. لاگ ربات فقط عددها رو نگه می‌داره، نه متن و نه کاربر.\nℹ️ هر خطا یا دیر رسیدن جواب Snap یعنی درخواست دقیقاً مثل وقتی که خاموشه انجام می‌شه."))

	toggle := b.button(l.t("⏸ خاموش کردن Snap"), "a:sn:on", telegram.StyleDanger)
	if !on {
		toggle = b.button(l.t("▶️ روشن کردن Snap"), "a:sn:on", telegram.StyleSuccess)
	}
	keys := []telegram.InlineKeyboardButton{b.button(l.t("🔑 کلید"), "a:sn:key", telegram.StylePrimary)}
	if b.snapKey() != "" {
		keys = append(keys, b.button(l.t("✅ تست کلید"), "a:sn:chk", ""))
	}
	if b.snapPanelKey() != "" {
		keys = append(keys, b.button(l.t("🗑 حذف کلید"), "a:sn:del", telegram.StyleDanger))
	}
	var models []telegram.InlineKeyboardButton
	for i, m := range snap.Models {
		label := strings.TrimPrefix(m, "subq-snap-")
		if m == b.snapModel() {
			label = "✅ " + label
		}
		models = append(models, b.button(label, "a:sn:model:"+strconv.Itoa(i), ""))
	}
	var waits []telegram.InlineKeyboardButton
	for _, ms := range snapTimeouts {
		label := "⏱ " + snapSeconds(l, time.Duration(ms)*time.Millisecond)
		if time.Duration(ms)*time.Millisecond == b.snapTimeout() {
			label = "✅ " + label
		}
		waits = append(waits, b.button(label, "a:sn:to:"+strconv.Itoa(ms), ""))
	}
	decision := func(key, setting, label string) telegram.InlineKeyboardButton {
		mark := "✅ "
		if !b.snapDecides(setting) {
			mark = "▫️ "
		}
		return b.button(mark+label, "a:sn:d:"+key, "")
	}
	var trims []telegram.InlineKeyboardButton
	for _, m := range []string{snapTrimAuto, snapTrimAlways, snapTrimOff} {
		label := "✂️ " + snapTrimName(l, m)
		if m == b.snapTrimMode() {
			label = "✅ " + snapTrimName(l, m)
		}
		trims = append(trims, b.button(label, "a:sn:trim:"+m, ""))
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(toggle),
		row(keys...),
		row(models...),
		row(waits...),
		row(decision("route", settingSnapRoute, l.t("🧭 مسیر مدل")), decision("chat", settingSnapChat, l.t("💬 بدون ابزار"))),
		row(decision("effort", settingSnapEffort, l.t("🎚 سقف فکر"))),
		row(trims...),
		row(b.button(l.t("🧪 امتحان یه پیام"), "a:sn:test", telegram.StylePrimary), b.back(l, "a:pv")),
	)}
}

// snapPanelKey is the key set in the panel ("" none, or unreadable).
func (b *Bot) snapPanelKey() string {
	k, err := b.st.SecretSetting(settingSnapKey)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(k)
}

func snapSeconds(l lang, d time.Duration) string {
	s := strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
	if l == en {
		return s + "s"
	}
	return faDecimal(s) + " ثانیه"
}

// faDecimal writes a decimal number with Persian digits.
func faDecimal(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '0' && r <= '9':
			return '۰' + (r - '0')
		case r == '.':
			return '٫'
		}
		return r
	}, s)
}

func snapTrimName(l lang, mode string) string {
	switch mode {
	case snapTrimAlways:
		return l.t("همیشه")
	case snapTrimOff:
		return l.t("خاموش")
	}
	return l.t("خودکار")
}

func snapTrimAbout(l lang, mode string) string {
	switch mode {
	case snapTrimAlways:
		return l.t("هر درخواستی که روی پای خودش وایساده، با چند پیام آخر و ابزارهای لازم می‌ره (recall_history و more_tools بقیه رو برمی‌گردونن)؛ با پروایدری که کش داره ممکنه گرون‌تر تموم بشه")
	case snapTrimAuto:
		return l.t("فقط برای پروایدری که پرامپت رو کش نمی‌کنه؛ اونجا هر توکنی که نره، صرفه‌جوییه. با کش، پرامپت ثابت ارزون‌تره و دست نمی‌خوره")
	}
	return l.t("تاریخچه و ابزارها همیشه کامل می‌رن (پیش‌فرض؛ مطمئن‌ترین حالت)")
}

// snapCacheLines show how much each provider caches, as measured.
func (b *Bot) snapCacheLines(l lang) string {
	var lines []string
	for _, s := range b.prov.Snapshot() {
		r, n, known := b.providerCaches(s.ID)
		if n == 0 {
			continue
		}
		line := l.f("• %s: %s٪ از %s درخواست", esc(s.Name), l.num(int(r*100+0.5)), l.num(n))
		switch {
		case !known:
			line += l.t(" (هنوز کمه)")
		case r < snapCacheLow:
			line += l.t(" → کش نداره؛ خودکار کوتاه می‌شه")
		default:
			line += l.t(" → کش داره؛ خودکار دست نمی‌خوره")
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return l.t("\n<b>کش پرامپت پروایدرها</b>\nهنوز درخواستی اندازه‌گیری نشده.\n")
	}
	return l.t("\n<b>کش پرامپت پروایدرها</b> (از وقتی ربات روشن شده)\n") + strings.Join(lines, "\n") + "\n"
}

// snapWeek is the last seven days of decisions.
func (b *Bot) snapWeek(ctx context.Context, l lang) string {
	st, err := b.st.SnapSince(ctx, b.st.Now().Add(-7*24*time.Hour))
	if err != nil || st.Calls == 0 {
		return l.t("\n<b>۷ روز گذشته</b>\nهنوز تصمیمی گرفته نشده.")
	}
	var sb strings.Builder
	sb.WriteString(l.t("\n<b>۷ روز گذشته</b>\n"))
	sb.WriteString(l.f("📊 %s تصمیم · ❌ %s خطا یا دیرکرد · ⏱ میانگین %s میلی‌ثانیه (بیشترین %s)\n",
		l.num(st.Calls), l.num(st.Failures), l.num(st.AvgLatencyMS), l.num(st.MaxLatency)))
	sb.WriteString(l.f("🪙 توکن Snap: %s (≈ $%s)\n", snapTokens(l, st.SnapTokens), strconv.FormatFloat(float64(st.SnapTokens)*snapPrice/1e6, 'f', 4, 64)))
	sb.WriteString(l.f("💰 توکن‌هایی که به مدل زبانی نرفت (تخمینی): %s\n", snapTokens(l, st.SavedTokens)))
	var did []string
	for _, a := range []struct{ key, name string }{
		{snapRouteStrong, l.t("💪 مدل قوی")}, {snapDidChat, l.t("💬 بدون ابزار")},
		{snapDidEffort + "low", l.t("🎚 فکر کم")}, {snapDidTrim, l.t("✂️ تاریخچه کوتاه")},
		{snapDidFocus, l.t("🧰 ابزار انتخابی")},
	} {
		if n := st.Applied[a.key]; n > 0 {
			did = append(did, a.name+" "+l.num(n))
		}
	}
	if len(did) > 0 {
		sb.WriteString(l.f("✅ اعمال شد: %s\n", strings.Join(did, " · ")))
	}
	type kc struct {
		k string
		n int
	}
	var kinds []kc
	for k, n := range st.Kinds {
		kinds = append(kinds, kc{k, n})
	}
	sort.Slice(kinds, func(i, j int) bool {
		return kinds[i].n > kinds[j].n || (kinds[i].n == kinds[j].n && kinds[i].k < kinds[j].k)
	})
	var names []string
	for _, k := range kinds {
		names = append(names, kindName(l, router.Kind(k.k))+" "+l.num(k.n))
	}
	if len(names) > 0 {
		sb.WriteString(l.f("🗂 نوع کارها: %s", strings.Join(names, " · ")))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// snapTokens writes a token count briefly (12.3K, 1.2M).
func snapTokens(l lang, n int64) string {
	switch {
	case n >= 1_000_000:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	case n >= 10_000:
		return strconv.FormatFloat(float64(n)/1e3, 'f', 1, 64) + "K"
	}
	return l.num(int(n))
}

// testSnapKey asks Snap one small question with a key and says what happened.
func (b *Bot) testSnapKey(ctx context.Context, l lang, key string) string {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	_, err := b.snapClient().Decide(ctx, key, b.snapModel(), "Newest message:\nhello", map[string]snap.Question{
		"greeting": snap.Noul("Is the newest message a greeting?", "", ""),
	})
	return snapVerdictText(l, err)
}

// snapVerdictText says what a call's error means for the admin.
func snapVerdictText(l lang, err error) string {
	var e *snap.Error
	switch {
	case err == nil:
		return l.t("✅ کلید معتبره؛ Snap جواب داد.")
	case errors.Is(err, snap.ErrNoKey):
		return l.t("❌ کلیدی تنظیم نشده.")
	case errors.As(err, &e) && e.Status == 401:
		return l.t("❌ SubQ کلید رو قبول نکرد (نامعتبره یا باطل شده).")
	case errors.As(err, &e) && e.Status == 402:
		return l.t("❌ اعتبار حساب SubQ تموم شده؛ شارژش کن.")
	case errors.As(err, &e) && e.Status == 403:
		return l.t("❌ این کلید به این مدل یا سرویس دسترسی نداره.")
	case errors.As(err, &e) && e.Status == 429:
		return l.t("⚠️ کلید درسته ولی الان محدودیت تعداد درخواست خورده؛ کمی بعد.")
	case errors.As(err, &e):
		return l.f("⚠️ SubQ خطا داد: %s", esc(e.Error()))
	case errors.Is(err, context.DeadlineExceeded):
		return l.t("⚠️ SubQ به موقع جواب نداد.")
	}
	return l.t("⚠️ به SubQ وصل نشد (شبکه سرور یا آدرس سرویس رو چک کن).")
}

func (b *Bot) handleSnapPanel(ctx context.Context, l lang, chatID int64, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	saved := func() { answer(l.t("ذخیره شد ✅"), false) }
	switch arg(2) {
	case "on":
		on := b.st.SettingBool(settingSnapOn, false)
		if !on && b.snapKey() == "" {
			answer(l.t("اول کلید SubQ رو تنظیم کن 🔑"), true)
			return
		}
		_ = b.st.SetSetting(ctx, settingSnapOn, strconv.FormatBool(!on))
		b.snapReset()
		saved()
	case "key":
		answer("", false)
		ask(&adminInput{kind: "sn_key"}, l.t("🔑 کلید API سرویس SubQ رو بفرست (با sk- شروع می‌شه). پیامت بعد از خوندن پاک می‌شه و کلید رمزنگاری‌شده ذخیره می‌شه."))
		return
	case "chk":
		answer(l.t("دارم تست می‌کنم…"), false)
		b.sendHTML(ctx, chatID, b.testSnapKey(ctx, l, b.snapKey()), telegram.SendOptions{})
	case "del":
		_ = b.st.SetSecretSetting(ctx, settingSnapKey, "")
		if b.snapKey() == "" { // no key left: off, so it does not wait for one
			_ = b.st.SetSetting(ctx, settingSnapOn, "false")
		}
		answer(l.t("حذف شد"), false)
	case "model":
		i, err := strconv.Atoi(arg(3))
		if err != nil || i < 0 || i >= len(snap.Models) {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingSnapModel, snap.Models[i])
		saved()
	case "to":
		ms, err := strconv.Atoi(arg(3))
		if err != nil || !containsInt(snapTimeouts, ms) {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingSnapTimeout, strconv.Itoa(ms))
		saved()
	case "d":
		found := false
		for _, d := range snapDecisions {
			if d.key == arg(3) {
				_ = b.st.SetSetting(ctx, d.setting, strconv.FormatBool(!b.snapDecides(d.setting)))
				found = true
			}
		}
		if !found {
			answer("", false)
			break
		}
		saved()
	case "trim":
		m := arg(3)
		if m != snapTrimAuto && m != snapTrimAlways && m != snapTrimOff {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingSnapTrim, m)
		saved()
	case "test":
		if b.snapKey() == "" {
			answer(l.t("اول کلید SubQ رو تنظیم کن 🔑"), true)
			break
		}
		answer("", false)
		ask(&adminInput{kind: "sn_test"}, l.t("🧪 یه پیام بفرست، همون‌طور که یه کاربر می‌فرسته؛ می‌گم Snap چی فهمید و ربات باهاش چیکار می‌کنه (حتی وقتی Snap خاموشه، فقط برای امتحان)."))
		return
	default:
		answer("", false)
	}
	show(b.viewSnap(ctx, l))
}

func containsInt(xs []int, x int) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}

// snapReset forgets the failures: a new key or a switch starts afresh.
func (b *Bot) snapReset() {
	b.snap.fails.Store(0)
	b.snap.pausedTill.Store(0)
}

// snapKeyInput saves a key the admin sent, once SubQ accepts it.
func (b *Bot) snapKeyInput(ctx context.Context, l lang, m *telegram.Message, done func(panelView)) {
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // a key is a password: never leave it in the chat
	key := strings.TrimSpace(m.Text)
	if key == "" || strings.ContainsAny(key, " \n\t") || len(key) > 300 {
		b.reply(m, l.t("این شبیه کلید نیست؛ دوباره بفرست."))
		return
	}
	verdict := b.testSnapKey(ctx, l, key)
	if strings.HasPrefix(verdict, "❌") {
		b.reply(m, verdict)
		return
	}
	if err := b.st.SetSecretSetting(ctx, settingSnapKey, key); err != nil {
		b.reply(m, l.t(textError))
		return
	}
	b.snapReset()
	b.reply(m, l.t("✅ کلید ذخیره شد. Snap رو از همین پنل روشن کن.")+"\n"+verdict)
	done(b.viewSnap(ctx, l))
}

// viewSnapTest shows what Snap reads in a message and what each tier would
// do with it.
func (b *Bot) viewSnapTest(ctx context.Context, l lang, text string) panelView {
	back := keyboard(row(b.button(l.t("🧪 یه پیام دیگه"), "a:sn:test", telegram.StylePrimary), b.back(l, "a:sn")))
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := b.snapClient().Decide(ctx, b.snapKey(), b.snapModel(), b.snapScrub(snap.TriageState(nil, "", nil, text)), b.snapQuestions())
	var sb strings.Builder
	sb.WriteString(l.f("🧪 <b>تصمیم Snap</b>\n\n«%s»\n\n", esc(shortText(text, 300))))
	if err != nil {
		sb.WriteString(snapVerdictText(l, err))
		return panelView{html: sb.String(), kb: back}
	}
	tri := snap.ReadTriage(res)
	sb.WriteString(l.f("⏱ %s میلی‌ثانیه · 🪙 %s توکن\n", l.num(int(tri.Took.Milliseconds())), l.num(tri.InputTokens)))
	if tri.Kind != "" {
		sb.WriteString(l.f("🗂 نوع: <b>%s</b> (%s٪)\n", kindName(l, router.Kind(tri.Kind)), l.num(int(tri.KindConf*100+0.5))))
	}
	sb.WriteString(l.f("🏋️ حجم کار: %s از ۳ — %s\n", snapScore(l, tri.Work), snapWorkName(l, tri.Work)))
	sb.WriteString(l.f("🧰 ابزار لازم داره: %s٪\n", l.num(int(tri.NeedsTools*100+0.5))))
	sb.WriteString(l.f("🔗 تکیه به گفتگو: %s (%s٪)\n", snapContextName(l, tri.Context), l.num(int(tri.ContextConf*100+0.5))))
	type gp struct {
		k string
		p float64
	}
	var groups []gp
	for k, p := range tri.Groups {
		if p >= snapGroupAt {
			groups = append(groups, gp{k, p})
		}
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].p > groups[j].p })
	var gs []string
	for _, g := range groups {
		gs = append(gs, fmt.Sprintf("%s %s٪", g.k, l.num(int(g.p*100+0.5))))
	}
	if len(gs) > 0 {
		sb.WriteString(l.f("🧩 گروه ابزارها: %s\n", strings.Join(gs, l.t("، "))))
	}

	sb.WriteString(l.t("\n<b>ربات با این تصمیم چیکار می‌کنه</b>\n"))
	chat := b.snapDecides(settingSnapChat) && snapChat(tri, text)
	v := snapVerdict(tri)
	words := router.Classify(router.Input{Text: text})
	for _, t := range []tierID{tierOwner} {
		rc := b.routerConf(t)
		strong := rc.on && router.Decide(words, rc.kinds, rc.sens)
		var route string
		switch {
		case !rc.on:
			route = l.t("▫️ مدل معمولی (مسیریابی این سطح خاموشه)")
		case strong:
			route = l.f("💪 مدل قوی (از کلمات پیام) — %s", b.providerLabel(rc.strong))
		case b.st.SettingBool(settingSnapRoute, true) && v != nil && router.Decide(*v, rc.kinds, rc.sens):
			strong = true
			route = l.f("💪 مدل قوی (Snap کار سخت تشخیص داد) — %s", b.providerLabel(rc.strong))
		default:
			route = l.f("▫️ مدل معمولی — %s", b.providerLabel(rc.light))
		}
		fmt.Fprintf(&sb, "\n%s\n", route)
		var did []string
		if chat {
			did = append(did, l.t("💬 بدون ابزار و با پرامپت سبک"))
			if b.st.SettingBool(settingSnapEffort, true) {
				did = append(did, l.f("🎚 سقف effort: %s", snapEffort(true)))
			}
		}
		if !chat {
			provider := rc.light
			if strong {
				provider = rc.strong
			}
			if b.snapTrims(provider) {
				if keep := snapKeep(tri); keep > 0 {
					did = append(did, l.f("✂️ فقط حدود %s توکن آخر تاریخچه", l.num(keep)))
				}
				did = append(did, l.t("🧰 فقط ابزارهای لازم"))
			}
		}
		if len(did) == 0 {
			did = append(did, l.t("بقیه مثل همیشه"))
		}
		sb.WriteString("   " + strings.Join(did, l.t("، ")) + "\n")
	}
	if !b.st.SettingBool(settingSnapOn, false) {
		sb.WriteString(l.t("\n⏸ Snap خاموشه؛ این فقط امتحان بود و روی کاربرها اثری نداره."))
	}
	return panelView{html: sb.String(), kb: back}
}

func snapScore(l lang, f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if l == en {
		return s
	}
	return faDecimal(s)
}

func snapWorkName(l lang, w float64) string {
	switch min(max(int(w+0.5), 0), 3) {
	case 0:
		return l.t("یه جواب کوتاه")
	case 1:
		return l.t("یکی دو قدم سریع")
	case 2:
		return l.t("چند قدم")
	}
	return l.t("یه پروژه بلند چندمرحله‌ای")
}

func snapContextName(l lang, c string) string {
	switch c {
	case snap.ContextStandalone:
		return l.t("مستقل؛ روی پای خودش")
	case snap.ContextRecent:
		return l.t("ادامه چند پیام آخر")
	}
	return l.t("به گفتگوی قبل‌تر یا یه کار در جریان")
}
