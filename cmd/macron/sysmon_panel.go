package main

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/sysmon"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The server panel (admin panel → 🖥 سرور): CPU, memory, disk and network
// of the machine, the bot's own service, the machine, the busiest
// processes, a day of history, and alerts when a limit is crossed.
//
//	a:sys:W          the panel over the last W (1h, 6h, 24h)
//	a:syschart:W     a chart image of that stretch
//	a:sysal          the alerts' settings · a:sysal:t on/off · a:sysal:cpu|ram|disk cycles the limit

const (
	defaultSysLimit = 90
	sysSparkWidth   = 24
)

var sysWindows = []struct {
	key  string
	d    time.Duration
	name string
}{{"1h", time.Hour, "۱ ساعت"}, {"6h", 6 * time.Hour, "۶ ساعت"}, {"24h", 24 * time.Hour, "۲۴ ساعت"}}

// sysLimitChoices are what tapping a limit's button goes through (0: off).
var sysLimitChoices = []int{0, 70, 80, 85, 90, 95}

func sysWindow(key string) (time.Duration, string, string) {
	for _, w := range sysWindows {
		if w.key == key {
			return w.d, w.name, w.key
		}
	}
	return sysWindows[0].d, sysWindows[0].name, sysWindows[0].key
}

// sampler is the server monitor (made on first use).
func (b *Bot) sampler() *sysmon.Sampler {
	b.sysOnce.Do(func() {
		disks := []string{"/"}
		if base := b.app.Machine.BaseDir; base != "" && !sameDevice("/", base) {
			disks = append(disks, base)
		}
		b.sys = sysmon.New(sysmon.Options{Disks: disks, Alerts: &sysmon.Watch{Limits: b.sysLimits, Notify: b.sysAlert}})
	})
	return b.sys
}

// startSysmon starts reading the server and watching its limits.
func (b *Bot) startSysmon(ctx context.Context) { go b.sampler().Run(ctx) }

// sameDevice reports whether two paths are on one filesystem.
func sameDevice(a, c string) bool {
	var x, y syscall.Stat_t
	return syscall.Stat(a, &x) == nil && syscall.Stat(c, &y) == nil && x.Dev == y.Dev
}

// sysLimits are the percentages that raise an alert (all 0 when they are off).
func (b *Bot) sysLimits() sysmon.Limits {
	if !b.st.SettingBool(store.SettingSysAlerts, true) {
		return sysmon.Limits{}
	}
	return sysmon.Limits{
		CPU:  b.st.SettingInt(store.SettingSysCPU, defaultSysLimit),
		RAM:  b.st.SettingInt(store.SettingSysRAM, defaultSysLimit),
		Disk: b.st.SettingInt(store.SettingSysDisk, defaultSysLimit),
	}
}

// adminIDs are the owners and the admins.
func (b *Bot) adminIDs(ctx context.Context) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for id := range b.owner {
		seen[id] = true
		out = append(out, id)
	}
	if list, err := b.st.Admins(ctx); err == nil {
		for _, id := range list {
			if !seen[id] {
				out = append(out, id)
			}
		}
	}
	return out
}

// sysAlert tells the admins that a limit was crossed or is over.
func (b *Bot) sysAlert(a sysmon.Alert) {
	ctx, cancel := b.ctx(time.Minute)
	defer cancel()
	for _, id := range b.adminIDs(ctx) {
		l := b.lang(id)
		b.sendHTML(ctx, id, sysAlertText(l, a), telegram.SendOptions{
			Markup: keyboard(row(b.button(l.t("🖥 دیدن سرور"), "a:sys:1h", telegram.StylePrimary)))})
	}
}

func sysAlertText(l lang, a sysmon.Alert) string {
	var sb strings.Builder
	what := map[string]string{
		sysmon.AlertCPU:  l.t("CPU"),
		sysmon.AlertRAM:  l.t("RAM"),
		sysmon.AlertDisk: l.t("دیسک"),
	}[a.Kind]
	if a.Recovered {
		fmt.Fprint(&sb, l.f("✅ <b>%s به حالت عادی برگشت</b>: الان %.0f٪ (حد %d٪)", what, a.Value, a.Limit))
		if a.Path != "" {
			fmt.Fprintf(&sb, " · <code>%s</code>", esc(a.Path))
		}
		return sb.String()
	}
	switch a.Kind {
	case sysmon.AlertCPU:
		fmt.Fprint(&sb, l.f("🚨 <b>CPU سرور بالاست</b>: میانگین ۵ دقیقه اخیر %.0f٪ (حد %d٪)", a.Value, a.Limit))
	case sysmon.AlertRAM:
		fmt.Fprint(&sb, l.f("🚨 <b>رم سرور داره پر می‌شه</b>: %.0f٪ در ۲ دقیقه اخیر (حد %d٪)", a.Value, a.Limit))
	default:
		fmt.Fprint(&sb, l.f("🚨 <b>دیسک سرور داره پر می‌شه</b>: <code>%s</code> الان %.0f٪ پره (حد %d٪)", esc(a.Path), a.Value, a.Limit))
	}
	for i, p := range a.Top {
		if i == 0 {
			sb.WriteString("\n\n")
		}
		if a.Kind == sysmon.AlertRAM {
			fmt.Fprintf(&sb, "• <code>%s</code> — %s\n", esc(p.Name), sizeText(int64(p.RSS)))
		} else {
			fmt.Fprintf(&sb, "• <code>%s</code> — %.0f٪\n", esc(p.Name), p.CPU)
		}
	}
	return strings.TrimRight(sb.String(), "\n")
}

// ---------------------------------------------------------------- the panel

func levelIcon(pct float64) string {
	switch {
	case pct >= 85:
		return "🔴"
	case pct >= 60:
		return "🟡"
	}
	return "🟢"
}

// bar is a percentage as eight cells.
func bar(pct float64) string {
	n := min(max(int(pct/12.5+0.5), 0), 8)
	return strings.Repeat("▰", n) + strings.Repeat("▱", 8-n)
}

// spark draws values (0..top) as one character each.
func spark(vals []float64, top float64) string {
	const blocks = "▁▂▃▄▅▆▇█"
	r := []rune(blocks)
	var sb strings.Builder
	for _, v := range vals {
		i := int(v/top*float64(len(r)-1) + 0.5)
		sb.WriteRune(r[min(max(i, 0), len(r)-1)])
	}
	return sb.String()
}

func rateText(bps float64) string { return sizeText(int64(bps)) + "/s" }

func (b *Bot) viewServer(l lang, key string) panelView {
	win, winName, key := sysWindow(key)
	sample := b.sampler().Latest()
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("🖥 <b>سرور</b> · روشن از %s\n", uptime(l, sample.Uptime)))
	if !sample.Ready {
		sb.WriteString(l.t("\n⏳ هنوز دو بار اندازه نگرفته؛ چند ثانیه بعد «🔄 بروزرسانی» رو بزن.\n"))
	}
	hist := b.sampler().History(win, sysSparkWidth)
	series := func(f func(sysmon.Bucket) float64) []float64 {
		out := make([]float64, len(hist))
		for i, h := range hist {
			out[i] = f(h)
		}
		return out
	}
	peak := func(v []float64) float64 {
		m := 0.0
		for _, x := range v {
			m = max(m, x)
		}
		return m
	}
	mean := func(v []float64) float64 {
		if len(v) == 0 {
			return 0
		}
		s := 0.0
		for _, x := range v {
			s += x
		}
		return s / float64(len(v))
	}

	cpuHist := series(func(h sysmon.Bucket) float64 { return h.CPU })
	fmt.Fprint(&sb, l.f("\n⚙️ <b>CPU</b> %s %s <b>%.0f٪</b> · %d هسته\n", levelIcon(sample.CPU), bar(sample.CPU), sample.CPU, sample.CoreCount))
	fmt.Fprint(&sb, l.f("load: <code>%.2f · %.2f · %.2f</code>\n", sample.Load[0], sample.Load[1], sample.Load[2]))
	if len(hist) > 1 {
		fmt.Fprint(&sb, l.f("<code>%s</code> %s: میانگین %.0f٪ · اوج %.0f٪\n", spark(cpuHist, 100), winName, mean(cpuHist), peak(series(func(h sysmon.Bucket) float64 { return h.CPUMax }))))
	}
	if n := len(sample.Cores); n > 1 && n <= 16 {
		cores := make([]float64, n)
		copy(cores, sample.Cores)
		fmt.Fprint(&sb, l.f("هسته‌ها: <code>%s</code>\n", spark(cores, 100)))
	}

	memHist := series(func(h sysmon.Bucket) float64 { return h.Mem })
	fmt.Fprint(&sb, l.f("\n🧠 <b>RAM</b> %s %s <b>%.0f٪</b> · %s از %s\n", levelIcon(sample.MemPct()), bar(sample.MemPct()), sample.MemPct(),
		sizeText(int64(sample.MemUsed)), sizeText(int64(sample.MemTotal))))
	if sample.SwapTotal > 0 {
		fmt.Fprint(&sb, l.f("swap: %s از %s\n", sizeText(int64(sample.SwapUsed)), sizeText(int64(sample.SwapTotal))))
	}
	if len(hist) > 1 {
		fmt.Fprint(&sb, l.f("<code>%s</code> %s: میانگین %.0f٪ · اوج %.0f٪\n", spark(memHist, 100), winName, mean(memHist), peak(series(func(h sysmon.Bucket) float64 { return h.MemMax }))))
	}

	for _, d := range sample.Disks {
		fmt.Fprint(&sb, l.f("\n💾 <b>دیسک</b> <code>%s</code> %s %s <b>%.0f٪</b> · %s آزاد از %s\n", esc(d.Path), levelIcon(d.UsedPct()), bar(d.UsedPct()), d.UsedPct(),
			sizeText(int64(d.Free)), sizeText(int64(d.Total))))
	}
	if sample.Ready {
		fmt.Fprint(&sb, l.f("\n🌐 <b>شبکه</b> ↓ %s · ↑ %s\n", rateText(sample.NetRx), rateText(sample.NetTx)))
	}

	sv := sample.Service
	fmt.Fprint(&sb, l.f("\n🤖 <b>خود ربات</b> (با برنامه‌هایی که راه انداخته)\nCPU: %.1f هسته · RAM: %s", sv.Cores, sizeText(int64(sv.Mem))))
	switch {
	case sv.MemMax > 0:
		fmt.Fprint(&sb, l.f(" (سقف %s)", sizeText(int64(sv.MemMax))))
	case sv.MemHigh > 0:
		fmt.Fprint(&sb, l.f(" (سقف نرم %s)", sizeText(int64(sv.MemHigh))))
	}
	fmt.Fprint(&sb, l.f("\nپروسه ربات: %s · %d ترد · %d فایل باز · %d گوروتین\n", sizeText(int64(sv.ProcessRSS)), sv.Threads, sv.FDs, runtime.NumGoroutine()))

	ld := b.app.Machine.Load()
	fmt.Fprint(&sb, l.f("\n🧪 <b>دستورها</b>: %d دستور در حال اجرا · اسلات %d از %d · سنگین %d از %d", ld.Running, ld.Slots, ld.SlotsMax, ld.Heavy, ld.HeavyMax))
	if ld.Shells > 0 {
		fmt.Fprint(&sb, l.f(" · %d شل ماندگار با %d کار پس‌زمینه", ld.Shells, ld.Jobs))
	}
	sb.WriteString("\n")

	if sample.Ready {
		var busy []string
		for i, p := range b.sampler().Top(5, "cpu") {
			if p.CPU < 0.5 {
				break
			}
			busy = append(busy, fmt.Sprintf("%d. <code>%s</code> — %.0f٪ · %s\n", i+1, esc(p.Name), p.CPU, sizeText(int64(p.RSS))))
		}
		if len(busy) > 0 {
			sb.WriteString(l.t("\n🔥 <b>پرمصرف‌ترین‌ها (CPU)</b>\n") + strings.Join(busy, ""))
		}
	}
	if top := b.sampler().Top(5, "mem"); len(top) > 0 {
		sb.WriteString(l.t("\n🐏 <b>پرمصرف‌ترین‌ها (RAM)</b>\n"))
		for i, p := range top {
			fmt.Fprintf(&sb, "%d. <code>%s</code> — %s\n", i+1, esc(p.Name), sizeText(int64(p.RSS)))
		}
	}

	var wins []telegram.InlineKeyboardButton
	for _, w := range sysWindows {
		label := l.t(w.name)
		if w.key == key {
			label = "✓ " + label
		}
		wins = append(wins, b.button(label, "a:sys:"+w.key, ""))
	}
	alerts := "🔔 " + onOff(l, b.st.SettingBool(store.SettingSysAlerts, true))
	return panelView{html: sb.String(), kb: keyboard(
		row(b.button(l.t("🔄 بروزرسانی"), "a:sys:"+key, telegram.StylePrimary), b.button(l.t("📈 نمودار"), "a:syschart:"+key, "")),
		wins,
		row(b.button(alerts+" "+l.t("هشدارها"), "a:sysal", "")),
		row(b.back(l, "a:stats")),
	)}
}

// viewSysAlerts shows the alerts' settings.
func (b *Bot) viewSysAlerts(l lang) panelView {
	on := b.st.SettingBool(store.SettingSysAlerts, true)
	lim := func(k string) int { return b.st.SettingInt(k, defaultSysLimit) }
	word := func(n int) string {
		if n <= 0 {
			return l.t("خاموش")
		}
		if l == en {
			return fmt.Sprintf("%d%%", n)
		}
		return fmt.Sprintf("%d٪", n)
	}
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("🔔 <b>هشدار سرور</b>: %s\n\n", onOff(l, on)))
	sb.WriteString(l.t("وقتی یکی از این‌ها از حدش رد بشه، به همه ادمین‌ها پیام میاد؛ فقط یه بار، همراه پرمصرف‌ترین پروسه‌ها، و بعد از برگشتن به حالت عادی یه پیام دیگه. اگه همچنان بالا بمونه هر ۳ ساعت یادآوری می‌شه.\n\n"))
	fmt.Fprint(&sb, l.f("⚙️ CPU: میانگین ۵ دقیقه ≥ <b>%s</b>\n🧠 RAM: میانگین ۲ دقیقه ≥ <b>%s</b>\n💾 دیسک: پُری ≥ <b>%s</b>\n\nروی هر کدوم بزنی حدش یه پله عوض می‌شه.",
		word(lim(store.SettingSysCPU)), word(lim(store.SettingSysRAM)), word(lim(store.SettingSysDisk))))
	toggle := b.button(l.t("🔕 خاموش کن"), "a:sysal:t", "")
	if !on {
		toggle = b.button(l.t("🔔 روشن کن"), "a:sysal:t", telegram.StyleSuccess)
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(toggle),
		row(b.button("⚙️ CPU "+word(lim(store.SettingSysCPU)), "a:sysal:cpu", ""), b.button("🧠 RAM "+word(lim(store.SettingSysRAM)), "a:sysal:ram", ""),
			b.button("💾 "+l.t("دیسک")+" "+word(lim(store.SettingSysDisk)), "a:sysal:disk", "")),
		row(b.back(l, "a:sys:1h")),
	)}
}

// cycleSysLimit moves a limit to the next choice.
func (b *Bot) cycleSysLimit(ctx context.Context, key string) {
	cur := b.st.SettingInt(key, defaultSysLimit)
	next := sysLimitChoices[0]
	for i, v := range sysLimitChoices {
		if v == cur {
			next = sysLimitChoices[(i+1)%len(sysLimitChoices)]
			break
		}
	}
	_ = b.st.SetSetting(ctx, key, strconv.Itoa(next))
}

// handleSysCallback serves the server panel's buttons.
func (b *Bot) handleSysCallback(ctx context.Context, l lang, section string, parts []string, chatID, admin int64, answer func(string, bool), show showFunc) {
	arg := ""
	if len(parts) > 2 {
		arg = parts[2]
	}
	switch section {
	case "sys":
		answer("", false)
		show(b.viewServer(l, arg))
	case "syschart":
		answer(l.t("دارم نمودار رو می‌کشم…"), false)
		b.sendSysChart(ctx, chatID, admin, arg)
	case "sysal":
		switch arg {
		case "t":
			_ = b.st.SetSetting(ctx, store.SettingSysAlerts, strconv.FormatBool(!b.st.SettingBool(store.SettingSysAlerts, true)))
		case "cpu":
			b.cycleSysLimit(ctx, store.SettingSysCPU)
		case "ram":
			b.cycleSysLimit(ctx, store.SettingSysRAM)
		case "disk":
			b.cycleSysLimit(ctx, store.SettingSysDisk)
		}
		answer("", false)
		show(b.viewSysAlerts(l))
	}
}

// sendSysChart draws CPU and memory over the stretch and sends it.
func (b *Bot) sendSysChart(ctx context.Context, chatID, admin int64, key string) {
	l := b.lang(admin)
	win, winName, _ := sysWindow(key)
	hist := b.sampler().History(win, 48)
	if len(hist) < 2 {
		b.sendHTML(ctx, chatID, l.t("هنوز داده کافی برای نمودار نیست؛ چند دقیقه بعد دوباره امتحان کن."), telegram.SendOptions{})
		return
	}
	labels := make([]string, len(hist))
	cpu, mem := make([]float64, len(hist)), make([]float64, len(hist))
	for i, h := range hist {
		labels[i] = h.At.In(b.loc).Format("15:04")
		cpu[i], mem[i] = h.CPU, h.Mem
	}
	session := fmt.Sprintf("admin-%d", admin)
	args, _ := json.Marshal(map[string]any{"chart_type": "line", "title": "Server: CPU and RAM, last " + strings.TrimSuffix(key, "h") + " h", "labels": labels,
		"series": []map[string]any{{"name": "CPU", "values": cpu}, {"name": "RAM", "values": mem}}, "unit": "%", "filename": "server.png"})
	out, err := b.app.Registry.Execute(ctx, session, "generate_chart", string(args))
	if err != nil || !strings.Contains(out, "Chart created") {
		b.sendHTML(ctx, chatID, l.t("نمودار ساخته نشد: ")+esc(strings.TrimSpace(out)), telegram.SendOptions{})
		return
	}
	f, _, err := b.app.Machine.Open(session, "server.png")
	if err != nil {
		return
	}
	defer func() { f.Close(); _ = b.app.Machine.Remove(session, "server.png") }()
	sample := b.sampler().Latest()
	caption := l.f("🖥 CPU و RAM · %s\nالان: CPU %.0f٪ · RAM %.0f٪", winName, sample.CPU, sample.MemPct())
	_ = b.api.SendPhoto(ctx, chatID, telegram.File{Name: "server.png", Reader: f}, caption)
}

// sysLine is the server in one line, for the stats panel.
func (b *Bot) sysLine(l lang) string {
	sample := b.sampler().Latest()
	if !sample.Ready {
		return l.t("سرور: هنوز اندازه‌گیری نشده")
	}
	line := l.f("سرور: CPU %.0f٪ (load %.2f) · RAM %.0f٪", sample.CPU, sample.Load[0], sample.MemPct())
	for _, d := range sample.Disks {
		line += l.f(" · دیسک %s %.0f٪", esc(d.Path), d.UsedPct())
	}
	return line
}
