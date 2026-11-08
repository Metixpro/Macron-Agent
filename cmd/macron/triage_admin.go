package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The owner's triage panel (a:tri), under smart routing: how requests are
// triaged (off, the words here, or the agent's own model), whether a short
// conversation may go without its tools, history trimming, and a test.

func triageModeName(l lang, mode string) string {
	switch mode {
	case triageLocal:
		return l.t("محلی (از روی کلمات)")
	case triageModel:
		return l.t("با مدل خود ایجنت")
	}
	return l.t("خاموش")
}

func (b *Bot) viewTriage(l lang) panelView {
	var sb strings.Builder
	mode := b.triageMode()
	sb.WriteString(l.t("🧭 <b>تشخیص درخواست</b>\n\nانتخاب ابزار، مدل و میزان تاریخچه برای هر پیام.\n\n"))
	sb.WriteString(l.f("وضعیت: <b>%s</b>\n", triageModeName(l, mode)))
	sb.WriteString(l.t("• محلی: بدون درخواست به مدل.\n• با مدل: درخواست کوتاه به پروایدر فعال؛ در خطا، بازگشت به محلی.\n"))
	if till := b.triage.pausedTill.Load(); till > 0 && time.Now().UnixNano() < till {
		sb.WriteString(l.f("⏸ بعد از چند خطای پشت‌سرهم تا %s فقط تشخیص محلی کار می‌کنه.\n", time.Unix(0, till).In(b.loc).Format("15:04")))
	}
	sb.WriteString(l.t("\n<b>تصمیم‌ها</b>\n"))
	sb.WriteString(l.t("🧭 کار سختی که از کلمات پیام معلوم نیست هم پیش مدل قوی می‌ره؛ هیچ‌وقت درخواستی پیش مدل ضعیف‌تر فرستاده نمی‌شه.\n"))
	sb.WriteString(l.f("💬 گپ بدون ابزار: %s — فقط سلام، تشکر و گپ کوتاه و مستقل، با پرامپت سبک. پیش‌فرض خاموشه: پیامی که گپ خونده بشه نمی‌تونه چیزی جستجو یا دانلود کنه\n", onOff(l, b.triageChatOn())))
	sb.WriteString(l.f("✂️ کوتاه کردن تاریخچه و ابزارها: <b>%s</b> — %s\n", triageTrimName(l, b.triageTrimMode()), triageTrimAbout(l, b.triageTrimMode())))
	sb.WriteString(l.t("🛡 وسط یه کار (پیام‌های قبلی ابزار زدن یا فایل ساختن) هیچ‌کدوم از این‌ها اعمال نمی‌شه، جز رفتن پیش مدل قوی.\n"))
	sb.WriteString(b.triageCacheLines(l))

	var modes []telegram.InlineKeyboardButton
	for _, m := range []string{triageOff, triageLocal, triageModel} {
		label := triageModeName(l, m)
		if m == mode {
			label = "✅ " + label
		}
		modes = append(modes, b.button(label, "a:tri:mode:"+m, ""))
	}
	chat := l.t("💬 بدون ابزار: خاموش")
	if b.triageChatOn() {
		chat = l.t("💬 بدون ابزار: روشن")
	}
	var trims []telegram.InlineKeyboardButton
	for _, m := range []string{triageTrimAuto, triageTrimAlways, triageTrimOff} {
		label := "✂️ " + triageTrimName(l, m)
		if m == b.triageTrimMode() {
			label = "✅ " + triageTrimName(l, m)
		}
		trims = append(trims, b.button(label, "a:tri:trim:"+m, ""))
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(modes...),
		row(b.button(chat, "a:tri:chat", "")),
		row(trims...),
		row(b.button(l.t("🧪 امتحان یه پیام"), "a:tri:test", telegram.StylePrimary), b.back(l, "a:rt")),
	)}
}

func triageTrimName(l lang, mode string) string {
	switch mode {
	case triageTrimAlways:
		return l.t("همیشه")
	case triageTrimOff:
		return l.t("خاموش")
	}
	return l.t("خودکار")
}

func triageTrimAbout(l lang, mode string) string {
	switch mode {
	case triageTrimAlways:
		return l.t("هر درخواستی که روی پای خودش وایساده، با چند پیام آخر و ابزارهای لازم می‌ره (recall_history و more_tools بقیه رو برمی‌گردونن)؛ با پروایدری که کش داره ممکنه گرون‌تر تموم بشه")
	case triageTrimAuto:
		return l.t("فقط برای پروایدری که پرامپت رو کش نمی‌کنه؛ اونجا هر توکنی که نره، صرفه‌جوییه. با کش، پرامپت ثابت ارزون‌تره و دست نمی‌خوره")
	}
	return l.t("تاریخچه و ابزارها همیشه کامل می‌رن (پیش‌فرض؛ مطمئن‌ترین حالت)")
}

// triageCacheLines show how much each provider caches, as measured.
func (b *Bot) triageCacheLines(l lang) string {
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
		case r < triageCacheLow:
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

func (b *Bot) handleTriagePanel(ctx context.Context, l lang, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	saved := func() { answer(l.t("ذخیره شد ✅"), false) }
	switch arg(2) {
	case "mode":
		m := arg(3)
		if m != triageOff && m != triageLocal && m != triageModel {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingTriage, m)
		b.triage.fails.Store(0)
		b.triage.pausedTill.Store(0)
		saved()
	case "chat":
		_ = b.st.SetSetting(ctx, settingTriageChat, strconv.FormatBool(!b.triageChatOn()))
		saved()
	case "trim":
		m := arg(3)
		if m != triageTrimAuto && m != triageTrimAlways && m != triageTrimOff {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingTriageTrim, m)
		saved()
	case "test":
		answer("", false)
		ask(&adminInput{kind: "tri_test"}, l.t("🧪 یه پیام بفرست، همون‌طور که یه کاربر می‌فرسته؛ می‌گم تشخیص چی فهمید و ربات باهاش چیکار می‌کنه (حتی وقتی خاموشه، فقط برای امتحان)."))
		return
	default:
		answer("", false)
	}
	show(b.viewTriage(l))
}

// viewTriageTest shows what the triage reads in a message and what the bot
// would do with it.
func (b *Bot) viewTriageTest(ctx context.Context, l lang, text string) panelView {
	back := keyboard(row(b.button(l.t("🧪 یه پیام دیگه"), "a:tri:test", telegram.StylePrimary), b.back(l, "a:tri")))
	tri := router.Local(router.TriageInput{Input: router.Input{Text: text}})
	var sb strings.Builder
	sb.WriteString(l.f("🧪 <b>تشخیص درخواست</b>\n\n«%s»\n\n", esc(shortText(text, 300))))
	if b.triageMode() == triageModel {
		mctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		t, err := b.modelTriage(mctx, router.TriageState(nil, "", nil, text))
		cancel()
		if err != nil {
			sb.WriteString(l.f("⚠️ مدل جواب درست نداد (%s)؛ این تشخیص محلیه.\n", esc(shortText(err.Error(), 120))))
		} else {
			tri = t
		}
	}
	sb.WriteString(l.f("⏱ %s میلی‌ثانیه · %s\n", l.num(int(tri.Took.Milliseconds())), triageModeName(l, tri.Source)))
	if tri.Kind != "" {
		sb.WriteString(l.f("🗂 نوع: <b>%s</b> (%s٪)\n", kindName(l, tri.Kind), l.num(int(tri.KindConf*100+0.5))))
	}
	sb.WriteString(l.f("🏋️ حجم کار: %s از ۳ — %s\n", triageScore(l, tri.Work), triageWorkName(l, tri.Work)))
	sb.WriteString(l.f("🧰 ابزار لازم داره: %s٪\n", l.num(int(tri.NeedsTools*100+0.5))))
	sb.WriteString(l.f("🔗 تکیه به گفتگو: %s (%s٪)\n", triageContextName(l, tri.Context), l.num(int(tri.ContextConf*100+0.5))))
	type gp struct {
		k string
		p float64
	}
	var groups []gp
	for k, p := range tri.Groups {
		if p >= triageGroupAt {
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
	chat := b.triageChatOn() && triageChat(tri, text)
	v := triageVerdict(tri)
	words := router.Classify(router.Input{Text: text})
	rc := b.routerConf(tierOwner)
	strong := rc.on && router.Decide(words, rc.kinds, rc.sens)
	var route string
	switch {
	case !rc.on:
		route = l.t("▫️ مدل معمولی (مسیریابی این سطح خاموشه)")
	case strong:
		route = l.f("💪 مدل قوی (از کلمات پیام) — %s", b.providerLabel(rc.strong))
	case v != nil && router.Decide(*v, rc.kinds, rc.sens):
		strong = true
		route = l.f("💪 مدل قوی (تشخیص، کار سخت دید) — %s", b.providerLabel(rc.strong))
	default:
		route = l.f("▫️ مدل معمولی — %s", b.providerLabel(rc.light))
	}
	fmt.Fprintf(&sb, "\n%s\n", route)
	var did []string
	if chat {
		did = append(did, l.t("💬 بدون ابزار و با پرامپت سبک"), l.f("🎚 سقف effort: %s", triageEffort(true)))
	} else {
		provider := rc.light
		if strong {
			provider = rc.strong
		}
		if b.triageTrims(provider) {
			if keep := triageKeep(tri); keep > 0 {
				did = append(did, l.f("✂️ فقط حدود %s توکن آخر تاریخچه", l.num(keep)))
			}
			if tri.Groups != nil {
				did = append(did, l.t("🧰 فقط ابزارهای لازم"))
			}
		}
	}
	if len(did) == 0 {
		did = append(did, l.t("بقیه مثل همیشه"))
	}
	sb.WriteString("   " + strings.Join(did, l.t("، ")) + "\n")
	if b.triageMode() == triageOff {
		sb.WriteString(l.t("\n⏸ تشخیص خاموشه؛ این فقط امتحان بود و روی درخواست‌ها اثری نداره."))
	}
	return panelView{html: sb.String(), kb: back}
}

func triageScore(l lang, f float64) string {
	s := strconv.FormatFloat(f, 'f', 1, 64)
	if l == en {
		return s
	}
	return faDecimal(s)
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

func triageWorkName(l lang, w float64) string {
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

func triageContextName(l lang, c string) string {
	switch c {
	case router.ContextStandalone:
		return l.t("مستقل؛ روی پای خودش")
	case router.ContextRecent:
		return l.t("ادامه چند پیام آخر")
	}
	return l.t("به گفتگوی قبل‌تر یا یه کار در جریان")
}
