package main

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The weekly report: every Saturday morning the bot's owners get how the
// week went (jobs, reactions, complaints, the tools that failed most with
// their last error), what to do about it, and the lessons waiting for them.
// The panel shows the same report any time (🧠 Learning → 📈).

const (
	settingWeeklyReport = "weekly_report"      // "false" turns it off
	settingWeeklySent   = "weekly_report_sent" // when the last one went (RFC3339)
	reportDays          = 7
	dailyKeep           = 90 * 24 * time.Hour
)

type toolRow struct {
	name         string
	calls, fails int
	last         string
}

type reportData struct {
	jobs, partial, up, down, complaints, repeats int
	tools                                        []toolRow      // by failures
	unhappy                                      map[string]int // tool → 👎, complaints and repeats about its answers
	pending                                      int
}

func (b *Bot) gatherReport(ctx context.Context, since time.Time) (reportData, error) {
	sums, err := b.st.DailySums(ctx, since)
	if err != nil {
		return reportData{}, err
	}
	d := reportData{unhappy: map[string]int{}}
	byTool := map[string]*toolRow{}
	row := func(name string) *toolRow {
		if byTool[name] == nil {
			byTool[name] = &toolRow{name: name}
		}
		return byTool[name]
	}
	for _, s := range sums {
		switch {
		case s.Tool == "" && s.Kind == store.DailyJob:
			d.jobs = s.N
		case s.Tool == "" && s.Kind == store.DailyPartial:
			d.partial = s.N
		case s.Tool == "" && s.Kind == store.DailyUp:
			d.up = s.N
		case s.Tool == "" && s.Kind == store.DailyDown:
			d.down = s.N
		case s.Tool == "" && s.Kind == store.DailyComplaint:
			d.complaints = s.N
		case s.Tool == "" && s.Kind == store.DailyRepeat:
			d.repeats = s.N
		case s.Kind == store.DailyCall:
			row(s.Tool).calls = s.N
		case s.Kind == store.DailyFail:
			r := row(s.Tool)
			r.fails, r.last = s.N, s.Note
		case s.Tool != "" && (s.Kind == store.DailyDown || s.Kind == store.DailyComplaint || s.Kind == store.DailyRepeat):
			d.unhappy[s.Tool] += s.N
		}
	}
	for _, r := range byTool {
		if r.fails > 0 {
			d.tools = append(d.tools, *r)
		}
	}
	sort.Slice(d.tools, func(i, j int) bool {
		if d.tools[i].fails != d.tools[j].fails {
			return d.tools[i].fails > d.tools[j].fails
		}
		return d.tools[i].name < d.tools[j].name
	})
	if counts, err := b.st.LessonCounts(ctx); err == nil {
		d.pending = counts[store.LessonPending]
	}
	return d, nil
}

func pct(n, of int) int {
	if of <= 0 {
		return 0
	}
	return n * 100 / of
}

// toolHint is what to look at when a tool fails a lot.
func toolHint(l lang, tool string) string {
	switch tool {
	case "download_media_ytdlp":
		return l.t("کوکی‌ها رو تست کن (⚙️ تنظیمات ← 📥 دانلودر ← 🩺 تست کوکی‌ها) و yt-dlp رو آپدیت کن.")
	case "web_fetch", "web_search", "deep_research", "browser", "browser_agent":
		return l.t("اگه سایت‌ها بلاک می‌کنن، توکن treg (⚙️ ← 🔎 جستجوی وب) و پراکسی سرور رو چک کن.")
	case "generate_image", "edit_image", "search_image":
		return l.t("سرویس یا کلید ساخت و ویرایش عکس رو چک کن.")
	case "run_code", "bash_execute", "install_package":
		return l.t("بیشترشون خطای خود برنامه‌ان و ایجنت درستشون می‌کنه؛ اگه خیلی زیاده، پکیج‌ها و منابع سرور رو ببین.")
	case "text_to_speech", "transcribe_audio":
		return l.t("سرویس صدا رو چک کن (⚙️ ← 🎙 صدا به متن).")
	case "download_file":
		return l.t("لینک‌ها خراب یا بزرگ‌تر از سقف بودن؛ سقف دانلود و فضای کاربرها رو ببین.")
	}
	return l.t("آخرین خطاش رو ببین؛ اگه همیشه همینه، برای رفعش بفرست.")
}

// weeklyReport is the report of the days since since, in the admin's language.
func (b *Bot) weeklyReport(ctx context.Context, l lang, since time.Time) string {
	d, err := b.gatherReport(ctx, since)
	if err != nil {
		return l.t(textError)
	}
	var sb strings.Builder
	sb.WriteString(l.f("📈 <b>گزارش هفتگی مکرون</b> (%s روز گذشته)\n\n", l.num(int(time.Since(since).Hours()/24+0.5))))
	if d.jobs == 0 {
		sb.WriteString(l.t("هنوز کاری ثبت نشده."))
		return sb.String()
	}
	fmt.Fprint(&sb, l.f("🧾 کارها: <b>%s</b> · ⏸ نیمه‌کاره: %s (%s٪)\n", l.num(d.jobs), l.num(d.partial), l.num(pct(d.partial, d.jobs))))
	fmt.Fprint(&sb, l.f("👍 %s · 👎 %s · 😤 اعتراض: %s · 🔁 همون درخواست دوباره: %s", l.num(d.up), l.num(d.down), l.num(d.complaints), l.num(d.repeats)))
	if d.up+d.down > 0 {
		fmt.Fprint(&sb, l.f(" · رضایت: <b>%s٪</b>", l.num(pct(d.up, d.up+d.down))))
	}
	var hints []string
	if len(d.tools) > 0 {
		sb.WriteString(l.t("\n\n🛠 <b>ابزارهایی که بیشتر خطا دادن</b>"))
		for i, r := range d.tools {
			if i == 5 {
				break
			}
			fmt.Fprint(&sb, l.f("\n%s. <code>%s</code>: %s خطا از %s (%s٪)", l.num(i+1), esc(r.name), l.num(r.fails), l.num(max(r.calls, r.fails)),
				l.num(pct(r.fails, max(r.calls, r.fails)))))
			if r.last != "" {
				fmt.Fprint(&sb, l.f("\n   آخرین خطا: <i>%s</i>", esc(r.last)))
			}
			if r.fails >= 3 && pct(r.fails, max(r.calls, r.fails)) >= 15 {
				hints = append(hints, fmt.Sprintf("<code>%s</code>: %s", esc(r.name), toolHint(l, r.name)))
			}
		}
	}
	if len(d.unhappy) > 0 {
		type kv struct {
			k string
			v int
		}
		var list []kv
		for k, v := range d.unhappy {
			list = append(list, kv{k, v})
		}
		sort.Slice(list, func(i, j int) bool { return list[i].v > list[j].v || list[i].v == list[j].v && list[i].k < list[j].k })
		var parts []string
		for i, x := range list {
			if i == 4 {
				break
			}
			parts = append(parts, fmt.Sprintf("<code>%s</code> (%s)", esc(x.k), l.num(x.v)))
		}
		sb.WriteString(l.t("\n\n😕 <b>جواب‌هایی که راضی نکردن بیشتر با این‌ها بودن</b>: ") + strings.Join(parts, "، "))
	}
	if d.jobs >= 10 && pct(d.partial, d.jobs) >= 20 {
		hints = append(hints, l.t("خیلی از کارها نیمه‌کاره موندن: قدم‌ها یا زمان هر درخواست رو بیشتر کن (⚙️ ← 🤖 قدرت ایجنت) یا ادامه خودکار رو روشن کن."))
	}
	if d.down >= 3 && pct(d.down, d.up+d.down) >= 30 {
		hints = append(hints, l.t("رضایت پایینه: جواب‌های 👎 رو با ابزارهای بالا مقایسه کن؛ شاید مدل یا پرووایدر فعال مناسب نیست."))
	}
	if len(hints) > 0 {
		sb.WriteString(l.t("\n\n💡 <b>پیشنهادها</b>"))
		for _, h := range hints {
			sb.WriteString("\n• " + h)
		}
	}
	if d.pending > 0 {
		fmt.Fprint(&sb, l.f("\n\n🧠 درس‌های منتظر تأیید تو: <b>%s</b> (پنل ← 🧠 یادگیری)", l.num(d.pending)))
	}
	return sb.String()
}

// runWeeklyReports sends the report on Saturday mornings.
func (b *Bot) runWeeklyReports(ctx context.Context) {
	t := time.NewTicker(20 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			b.maybeWeeklyReport(ctx, now)
		}
	}
}

// maybeWeeklyReport sends the report if it is due (Saturday from 9:00, once a week).
func (b *Bot) maybeWeeklyReport(ctx context.Context, now time.Time) bool {
	if !b.st.SettingBool(settingWeeklyReport, true) {
		return false
	}
	local := now.In(b.loc)
	if local.Weekday() != time.Saturday || local.Hour() < 9 {
		return false
	}
	if last, ok := b.st.Setting(settingWeeklySent); ok {
		if t, err := time.Parse(time.RFC3339, last); err == nil && now.Sub(t) < 6*24*time.Hour {
			return false
		}
	}
	sctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := b.st.SetSetting(sctx, settingWeeklySent, now.UTC().Format(time.RFC3339)); err != nil {
		return false // first marked as sent: it never goes twice
	}
	since := now.Add(-reportDays * 24 * time.Hour)
	for id := range b.owner {
		l := b.lang(id)
		kb := keyboard(row(b.button(l.t("🧠 یادگیری"), "a:learn", "")))
		if _, err := b.api.SendHTML(sctx, id, b.bidi(id, b.weeklyReport(sctx, l, since)), telegram.SendOptions{Markup: kb, Silent: true}); err != nil {
			log.Printf("weekly report to %d: %v", id, err)
		}
	}
	_ = b.st.PruneDaily(sctx, dailyKeep)
	return true
}
