package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/router"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	settingRouterOn     = "router_on"
	settingRouterStrong = "router_strong:"
	settingRouterKinds  = "router_kinds"
	settingRouterSens   = "router_sensitivity"
	settingRouterEsc    = "router_escalate"
	routeSticky         = 30 * time.Minute
)

type routerConf struct {
	on       bool
	light    int64
	strong   int64
	kinds    map[router.Kind]bool
	sens     router.Sensitivity
	escalate bool
}

func (b *Bot) routerKinds() map[router.Kind]bool {
	if v, ok := b.st.Setting(settingRouterKinds); ok {
		var list []router.Kind
		if json.Unmarshal([]byte(v), &list) == nil {
			out := map[router.Kind]bool{}
			for _, k := range list {
				out[k] = true
			}
			return out
		}
	}
	return router.DefaultStrong
}

func (b *Bot) routerSens() router.Sensitivity {
	switch v, _ := b.st.Setting(settingRouterSens); router.Sensitivity(v) {
	case router.Cheap, router.Quality:
		return router.Sensitivity(v)
	}
	return router.Balanced
}

func (b *Bot) routerConf(t tierID) routerConf {
	rc := routerConf{kinds: b.routerKinds(), sens: b.routerSens(), escalate: b.st.SettingBool(settingRouterEsc, true)}
	if p, ok := b.prov.ActiveFor(providers.Prefer(context.Background(), b.tier(t).provider)); ok {
		rc.light = p.ID
	}
	rc.strong = int64(b.st.SettingInt(settingRouterStrong+string(t), 0))
	rc.on = b.st.SettingBool(settingRouterOn, true) && rc.strong > 0 && rc.strong != rc.light && b.providerOn(rc.strong)
	return rc
}

func (b *Bot) providerOn(id int64) bool {
	for _, s := range b.prov.Snapshot() {
		if s.ID == id {
			return s.Enabled
		}
	}
	return false
}

func routeInput(m *telegram.Message, text string) router.Input {
	in := router.Input{Text: text, Reply: m.Replied() != nil || m.Quote != nil || m.ReplyToStory != nil}
	look := func(m *telegram.Message) {
		if len(m.Photo) > 0 {
			in.Images++
		}
		if d := m.Document; d != nil {
			if strings.HasPrefix(d.MimeType, "image/") {
				in.Images++
			} else {
				name := d.FileName
				if name == "" && d.MimeType == "application/vnd.android.package-archive" {
					name = "attachment.apk"
				}
				in.Files = append(in.Files, name)
			}
		}
	}
	look(m)
	for _, item := range m.Album {
		look(item)
	}
	if r := m.Replied(); r != nil {
		in.Hint = r.Body()
		look(r)
	}
	return in
}

type modelRoute struct {
	conf    routerConf
	verdict router.Verdict
	strong  bool
	why     string
	uid     int64
	b       *Bot
}

type routeMemo struct {
	strong bool
	at     time.Time
}

func (b *Bot) chooseModel(uid int64, t tierID, in router.Input, continuation bool, triaged func() (*router.Verdict, bool)) *modelRoute {
	rc := b.routerConf(t)
	if !rc.on {
		return nil
	}
	r := &modelRoute{conf: rc, verdict: router.Classify(in), uid: uid, b: b, why: "kind"}
	followUp := r.verdict.Signals[0].Why == "none"
	r.strong = router.Decide(r.verdict, rc.kinds, rc.sens)
	if triaged != nil {
		if v, leans := triaged(); v != nil {
			if !r.strong && router.Decide(*v, rc.kinds, rc.sens) {
				r.verdict, r.strong = *v, true
			}
			followUp = followUp || leans
		}
	}
	last, _ := b.routes.Load(uid)
	memo, recent := last.(routeMemo)
	recent = recent && time.Since(memo.at) < routeSticky
	switch {
	case continuation && recent:
		r.strong, r.why = memo.strong, "continuation"
	case !r.strong && recent && memo.strong && followUp:
		r.strong, r.why = true, "follow-up"
	}
	r.remember(r.strong)
	return r
}

func (r *modelRoute) remember(strong bool) {
	r.b.routes.Store(r.uid, routeMemo{strong: strong, at: time.Now()})
}

func (r *modelRoute) prefer(ctx context.Context) context.Context {
	if r == nil || !r.strong {
		return ctx
	}
	return providers.Prefer(ctx, r.conf.strong)
}

func (r *modelRoute) upgrade(in router.Input) bool {
	if r == nil || r.strong {
		return false
	}
	v := router.Classify(in)
	if !router.Decide(v, r.conf.kinds, r.conf.sens) {
		return false
	}
	r.verdict, r.strong, r.why = v, true, "kind"
	r.remember(true)
	return true
}

func (r *modelRoute) escalation() func(context.Context) context.Context {
	if r == nil || r.strong || !r.conf.escalate {
		return nil
	}
	return func(ctx context.Context) context.Context {
		r.remember(true)
		return providers.Prefer(ctx, r.conf.strong)
	}
}

func (r *modelRoute) window(b *Bot, conf tierConf, window int) int {
	if r == nil || (!r.strong && !r.conf.escalate) {
		return window
	}
	return min(window, b.tierWindow(tierConf{provider: r.conf.strong, context: conf.context}))
}

func (r *modelRoute) record(escalated bool, acc *providers.Account) {
	if r == nil {
		return
	}
	way := "light"
	switch {
	case escalated:
		way = "escalated"
	case r.strong:
		way = "strong"
	}
	u := acc.Usage()
	r.b.recordStat(store.StatRoute, way+":"+string(r.verdict.Kind), false, u.PromptTokens, u.CompletionTokens)
	if escalated || r.strong {
		log.Printf("[user %d] %s request (%s) → strong model %d (%s, %s)", r.uid, r.verdict.Kind, way, r.conf.strong, r.why, signalsLine(r.verdict))
	}
}

func signalsLine(v router.Verdict) string {
	parts := make([]string, len(v.Signals))
	for i, s := range v.Signals {
		parts[i] = s.String()
	}
	return strings.Join(parts, ", ")
}

func kindName(l lang, k router.Kind) string {
	switch k {
	case router.Chat:
		return l.t("💬 گپ و گفت‌وگو")
	case router.Quick:
		return l.t("⚡️ سؤال کوتاه")
	case router.Media:
		return l.t("🎬 مدیا و دانلود")
	case router.Writing:
		return l.t("✍️ نوشتن و ترجمه")
	case router.Vision:
		return l.t("👁 دیدن عکس")
	case router.Code:
		return l.t("💻 کدنویسی")
	case router.Research:
		return l.t("🔬 تحقیق و مقایسه")
	case router.Reasoning:
		return l.t("🧮 ریاضی و استدلال")
	case router.Documents:
		return l.t("📑 اسناد و داده")
	case router.Project:
		return l.t("🏗 پروژه و سرور")
	}
	return string(k)
}

func kindExample(l lang, k router.Kind) string {
	switch k {
	case router.Chat:
		return l.t("سلام، نظرت چیه، درد دل")
	case router.Quick:
		return l.t("قیمت دلار، معنی یه کلمه، یادآور")
	case router.Media:
		return l.t("دانلود ویدیو و آهنگ، جستجوی عکس، تبدیل فایل")
	case router.Writing:
		return l.t("کپشن، ایمیل، مقاله، خلاصه، ترجمه")
	case router.Vision:
		return l.t("«تو این عکس چیه؟»")
	case router.Code:
		return l.t("نوشتن، دیباگ و توضیح کد")
	case router.Research:
		return l.t("تحقیق جامع، مقایسه، گزارش")
	case router.Reasoning:
		return l.t("معادله، اثبات، الگوریتم، برنامه‌ریزی")
	case router.Documents:
		return l.t("PDF، اکسل، قرارداد، دیتاست")
	case router.Project:
		return l.t("ساخت اپ و سایت، سرور، داکر، دیپلوی")
	}
	return ""
}

func sensName(l lang, s router.Sensitivity) string {
	switch s {
	case router.Cheap:
		return l.t("💸 صرفه‌جو")
	case router.Quality:
		return l.t("💎 کیفیت")
	}
	return l.t("⚖️ متعادل")
}

func sensAbout(l lang, s router.Sensitivity) string {
	switch s {
	case router.Cheap:
		return l.t("فقط نوع‌های تیک‌خورده می‌رن سراغ مدل قوی.")
	case router.Quality:
		return l.t("هر چی جز گپ، سؤال کوتاه و مدیا با مدل قوی.")
	}
	return l.t("نوع‌های تیک‌خورده، به‌علاوه کار سنگین از نوع‌های سبک (پیام خیلی بلند، فهرست چندتایی خواسته‌ها، «کامل و حرفه‌ای»).")
}

func signalText(l lang, s router.Signal) string {
	var out string
	switch s.Why {
	case "code":
		out = l.t("خود کد یا ارور توی پیامه")
	case "programming":
		out = l.t("حرف از برنامه‌نویسی")
	case "app":
		out = l.t("یه اپ یا سایت کامل")
	case "servers":
		out = l.t("سرور و زیرساخت")
	case "research":
		out = l.t("تحقیق")
	case "analysis":
		out = l.t("مقایسه یا تحلیل")
	case "math":
		out = l.t("ریاضی یا منطق")
	case "planning":
		out = l.t("برنامه‌ریزی یا تصمیم")
	case "documents":
		out = l.t("سند بلند یا داده")
	case "writing":
		out = l.t("نوشتن")
	case "media":
		out = l.t("مدیا")
	case "quick":
		out = l.t("سؤال کوتاه")
	case "chat":
		out = l.t("گپ")
	case "code_file":
		out = l.f("فایل کد (%s)", s.Arg)
	case "document_file":
		out = l.f("سند (%s)", s.Arg)
	case "archive_file":
		out = l.f("آرشیو (%s)", s.Arg)
	case "pictures":
		out = l.f("%s عکس برای دیدن", s.Arg)
	case "none":
		out = l.t("کلمه خاصی از یه کار نداره")
	case "deep":
		out = l.t("حالت عمیق")
	case "long":
		out = l.f("پیام بلند (%s کاراکتر)", s.Arg)
	case "list":
		out = l.t("فهرست چندتایی خواسته‌ها")
	case "thorough":
		out = l.t("یه کار کامل و دقیق می‌خواد")
	case "triage":
		out = l.f("🧭 تشخیص درخواست (اطمینان و حجم کار: %s)", s.Arg)
	default:
		out = s.String()
	}
	if s.Hint {
		out += l.t(" (توی پیامی که بهش جواب داده)")
	}
	return out
}

func (b *Bot) providerLabel(id int64) string {
	for _, s := range b.prov.Snapshot() {
		if s.ID == id {
			return fmt.Sprintf("%s · <code>%s</code>", esc(s.Name), esc(s.Model))
		}
	}
	return "—"
}

func (b *Bot) viewRouter(ctx context.Context, l lang) panelView {
	var sb strings.Builder
	on := b.st.SettingBool(settingRouterOn, true)
	sb.WriteString(l.t("🧭 <b>مسیریابی هوشمند مدل</b>\n\nکارهای سبک و ارزون با مدل معمولی، کارهای سنگین و پرچالش با مدل قوی. هر پیام قبل از رفتن به مدل دسته‌بندی می‌شه: بی‌هزینه و در کسری از میلی‌ثانیه، بدون اینکه از هیچ مدلی سؤال بشه.\n\n"))
	sb.WriteString(l.f("وضعیت: <b>%s</b>\n", onOff(l, on)))
	for _, t := range []tierID{tierOwner} {
		rc := b.routerConf(t)
		sb.WriteString(l.f("\n▫️ معمولی: %s\n", b.providerLabel(rc.light)))
		switch {
		case rc.strong <= 0:
			sb.WriteString(l.t("💪 قوی: انتخاب نشده (همه چیز با مدل معمولی)\n"))
		case !b.providerOn(rc.strong):
			sb.WriteString(l.f("💪 قوی: %s ⚠️ خاموشه؛ تا روشن نشه همه چیز با مدل معمولیه\n", b.providerLabel(rc.strong)))
		case rc.strong == rc.light:
			sb.WriteString(l.f("💪 قوی: %s ⚠️ همون مدل معمولیه\n", b.providerLabel(rc.strong)))
		default:
			sb.WriteString(l.f("💪 قوی: %s\n", b.providerLabel(rc.strong)))
		}
	}
	kinds := b.routerKinds()
	var strong []string
	for _, k := range router.Kinds {
		if kinds[k] {
			strong = append(strong, kindName(l, k))
		}
	}
	if len(strong) == 0 {
		strong = append(strong, l.t("هیچ نوعی"))
	}
	sens := b.routerSens()
	help := b.st.SettingBool(settingRouterEsc, true)
	sb.WriteString(l.f("\n💪 <b>با مدل قوی:</b> %s\n", strings.Join(strong, l.t("، "))))
	sb.WriteString(l.f("🎯 <b>حساسیت: %s</b> — %s\n", sensName(l, sens), sensAbout(l, sens)))
	sb.WriteString(l.f("🚀 <b>کمک وسط کار: %s</b> — اگه مدل معمولی وسط کار گیر کنه (دو قدم ناموفق، فراخوانی‌های اشتباه، جا زدن یا جواب خالی)، بقیه کار رو مدل قوی ادامه می‌ده.\n", onOff(l, help)))
	sb.WriteString(l.t("🔁 پیام کوتاه بعدی همون کار («ادامه بده»، «آره همینو») تا ۳۰ دقیقه پیش همون مدل قوی می‌مونه.\n"))
	sb.WriteString(b.routerWeek(ctx, l))
	sb.WriteString(l.t("\n\nℹ️ مدل معمولی همون پروایدر فعاله (⭐ توی 🤖 پروایدرها)."))

	toggle := b.button(l.t("⏸ خاموش کردن مسیریابی"), "a:rt:on", telegram.StyleDanger)
	if !on {
		toggle = b.button(l.t("▶️ روشن کردن مسیریابی"), "a:rt:on", telegram.StyleSuccess)
	}
	sensBtn := func(s router.Sensitivity) telegram.InlineKeyboardButton {
		label := sensName(l, s)
		if s == sens {
			label = "✅ " + label
		}
		return b.button(label, "a:rt:sens:"+string(s), "")
	}
	escLabel := l.t("🚀 کمک وسط کار: روشن")
	if !help {
		escLabel = l.t("🚀 کمک وسط کار: خاموش")
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(toggle),
		row(b.button(l.t("💪 مدل قوی"), "a:rt:pick:strong:owner", telegram.StylePrimary), b.button(l.t("▫️ مدل معمولی"), "a:rt:pick:light:owner", "")),
		row(b.button(l.t("🗂 کدوم کارها با مدل قوی"), "a:rt:kinds", "")),
		row(sensBtn(router.Cheap), sensBtn(router.Balanced), sensBtn(router.Quality)),
		row(b.button(escLabel, "a:rt:esc", ""), b.button(l.t("🧪 امتحان یه پیام"), "a:rt:test", "")),
		row(b.button(l.t("🧭 تشخیص درخواست")+": "+triageModeName(l, b.triageMode()), "a:tri", telegram.StylePrimary)),
		row(b.back(l, "a:pv")),
	)}
}

func (b *Bot) routerWeek(ctx context.Context, l lang) string {
	rows, err := b.st.TopStats(ctx, store.StatRoute, 7, 100)
	if err != nil || len(rows) == 0 {
		return l.t("\n📊 هنوز درخواستی مسیریابی نشده.")
	}
	ways := map[string]int64{}
	tokens := map[string]int64{}
	byKind := map[router.Kind]int64{}
	var total int64
	for _, r := range rows {
		way, kind, _ := strings.Cut(r.Name, ":")
		ways[way] += r.Count
		tokens[way] += r.Tokens
		byKind[router.Kind(kind)] += r.Count
		total += r.Count
	}
	share := func(n int64) string { return l.num(int(n * 100 / max(total, 1))) }
	out := l.f("\n📊 <b>۷ روز اخیر</b> (%s درخواست):\n   ▫️ معمولی: %s (%s٪) · 💪 قوی: %s (%s٪) · 🚀 کمک وسط کار: %s",
		l.num(int(total)), l.num(int(ways["light"])), share(ways["light"]), l.num(int(ways["strong"])), share(ways["strong"]), l.num(int(ways["escalated"])))
	if t := tokens["light"] + tokens["strong"] + tokens["escalated"]; t > 0 {
		out += l.f("\n   🔢 توکن: معمولی %s · قوی %s", agent.HumanTokens(int(tokens["light"])), agent.HumanTokens(int(tokens["strong"]+tokens["escalated"])))
	}
	kinds := make([]router.Kind, 0, len(byKind))
	for k := range byKind {
		kinds = append(kinds, k)
	}
	slices.SortFunc(kinds, func(a, c router.Kind) int { return int(byKind[c] - byKind[a]) })
	var top []string
	for _, k := range kinds[:min(len(kinds), 5)] {
		top = append(top, fmt.Sprintf("%s %s", kindName(l, k), l.num(int(byKind[k]))))
	}
	return out + "\n   " + strings.Join(top, " · ")
}

func (b *Bot) viewRouterKinds(l lang) panelView {
	kinds := b.routerKinds()
	var sb strings.Builder
	sb.WriteString(l.t("🗂 <b>کدوم کارها با مدل قوی؟</b>\n\nتیک‌خورده‌ها با مدل قوی، بقیه با مدل معمولی (با حساسیت ⚖️ متعادل، کار سنگین از نوع‌های سبک هم با قوی).\n\n"))
	var btns []telegram.InlineKeyboardButton
	for _, k := range router.Kinds {
		mark := "▫️"
		if kinds[k] {
			mark = "💪"
		}
		fmt.Fprintf(&sb, "%s %s — %s\n", mark, kindName(l, k), kindExample(l, k))
		label := kindName(l, k)
		if kinds[k] {
			label = "✅ " + label
		}
		btns = append(btns, b.button(label, "a:rt:kind:"+string(k), ""))
	}
	rows := chunkButtons(btns, 2)
	rows = append(rows, row(b.button(l.t("♻️ پیش‌فرض"), "a:rt:kreset", ""), b.back(l, "a:rt")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) viewRouterPick(l lang, which string, t tierID) panelView {
	rc := b.routerConf(t)
	current := rc.strong
	html := l.t("💪 <b>مدل قوی</b>\n\nکارهای سنگین اول به این پروایدر می‌رن (اگه خطا بده، بقیه جاش جواب می‌دن). «هیچ» یعنی مسیریابی خاموش و همه چیز با مدل معمولی.")
	none := l.t("هیچ")
	if which == "light" {
		current = b.tier(t).provider
		html = l.t("▫️ <b>مدل معمولی</b>\n\nهمه درخواست‌ها، جز کارهای سنگین، اول به این پروایدر می‌رن. «خودکار» یعنی ⭐ فعال پنل.")
		none = l.t("خودکار")
	}
	mark := func(on bool, label string) string {
		if on {
			return "✅ " + label
		}
		return label
	}
	btns := []telegram.InlineKeyboardButton{b.button(mark(current <= 0, none), fmt.Sprintf("a:rt:set:%s:%s:0", which, t), "")}
	for _, s := range b.prov.Snapshot() {
		if !s.Enabled {
			continue
		}
		btns = append(btns, b.button(mark(s.ID == current, shortText(s.Name+" · "+s.Model, 40)), fmt.Sprintf("a:rt:set:%s:%s:%d", which, t, s.ID), ""))
	}
	rows := chunkButtons(btns, 1)
	rows = append(rows, row(b.back(l, "a:rt")))
	return panelView{html: html + l.t("\n\nℹ️ فقط پروایدرهای روشن اینجان."), kb: keyboard(rows...)}
}

func (b *Bot) viewRouterTest(l lang, text string) panelView {
	v := router.Classify(router.Input{Text: text})
	kinds, sens := b.routerKinds(), b.routerSens()
	var sb strings.Builder
	sb.WriteString(l.f("🧪 <b>نتیجه</b>\n\n«%s»\n\n", esc(shortText(text, 300))))
	sb.WriteString(l.f("🗂 نوع: <b>%s</b>", kindName(l, v.Kind)))
	if v.Hard {
		sb.WriteString(l.t(" · 🏋️ سنگین‌تر از معمول"))
	}
	sb.WriteString(l.t("\n🔎 چرا:"))
	for _, s := range v.Signals {
		sb.WriteString("\n   • " + signalText(l, s))
	}
	if also := v.Also(); len(also) > 0 {
		var names []string
		for _, k := range also {
			names = append(names, kindName(l, k))
		}
		sb.WriteString(l.f("\n   • کمتر: %s", strings.Join(names, l.t("، "))))
	}
	strong := router.Decide(v, kinds, sens)
	sb.WriteString("\n\n")
	for _, t := range []tierID{tierOwner} {
		rc := b.routerConf(t)
		switch {
		case !rc.on:
			sb.WriteString(l.t("▫️ مدل معمولی (مسیریابی خاموشه)\n"))
		case strong:
			sb.WriteString(l.f("💪 مدل قوی — %s\n", b.providerLabel(rc.strong)))
		default:
			sb.WriteString(l.f("▫️ مدل معمولی — %s\n", b.providerLabel(rc.light)))
		}
	}
	return panelView{html: sb.String(), kb: keyboard(row(b.button(l.t("🧪 یه پیام دیگه"), "a:rt:test", telegram.StylePrimary), b.back(l, "a:rt")))}
}

func (b *Bot) handleRouterPanel(ctx context.Context, l lang, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	saved := func() { answer(l.t("ذخیره شد ✅"), false) }
	switch arg(2) {
	case "on":
		_ = b.st.SetSetting(ctx, settingRouterOn, strconv.FormatBool(!b.st.SettingBool(settingRouterOn, true)))
		saved()
	case "esc":
		_ = b.st.SetSetting(ctx, settingRouterEsc, strconv.FormatBool(!b.st.SettingBool(settingRouterEsc, true)))
		saved()
	case "sens":
		s := router.Sensitivity(arg(3))
		if s != router.Cheap && s != router.Balanced && s != router.Quality {
			answer("", false)
			break
		}
		_ = b.st.SetSetting(ctx, settingRouterSens, string(s))
		saved()
	case "pick":
		t, ok := parseTier(arg(4))
		if !ok || (arg(3) != "light" && arg(3) != "strong") {
			answer("", false)
			break
		}
		answer("", false)
		show(b.viewRouterPick(l, arg(3), t))
		return
	case "set":
		t, ok := parseTier(arg(4))
		id, err := strconv.ParseInt(arg(5), 10, 64)
		if !ok || err != nil || id < 0 || (id > 0 && !b.providerOn(id)) {
			answer(l.t("این پروایدر دیگه روشن نیست"), true)
			break
		}
		switch arg(3) {
		case "strong":
			_ = b.st.SetSetting(ctx, settingRouterStrong+string(t), strconv.FormatInt(id, 10))
		case "light":
			_ = b.st.SetSetting(ctx, settingOwnerProvider, strconv.FormatInt(id, 10))
		}
		saved()
	case "kinds":
		answer("", false)
		show(b.viewRouterKinds(l))
		return
	case "kind", "kreset":
		kinds := b.routerKinds()
		if arg(2) == "kreset" {
			kinds = router.DefaultStrong
		} else if k := router.Kind(arg(3)); slices.Contains(router.Kinds, k) {
			next := map[router.Kind]bool{}
			for kk, on := range kinds {
				next[kk] = on
			}
			next[k] = !next[k]
			kinds = next
		}
		var list []router.Kind
		for _, k := range router.Kinds {
			if kinds[k] {
				list = append(list, k)
			}
		}
		raw, _ := json.Marshal(list)
		_ = b.st.SetSetting(ctx, settingRouterKinds, string(raw))
		saved()
		show(b.viewRouterKinds(l))
		return
	case "test":
		answer("", false)
		ask(&adminInput{kind: "rt_test"}, l.t("🧪 یه پیام بفرست، همون‌طور که یه کاربر می‌فرسته؛ می‌گم چه نوع کاریه، چرا، و با کدوم مدل جواب داده می‌شه."))
		return
	default:
		answer("", false)
	}
	show(b.viewRouter(ctx, l))
}
