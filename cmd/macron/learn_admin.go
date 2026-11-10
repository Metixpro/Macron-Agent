package main

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const lessonsPage = 8

func (b *Bot) viewLearn(ctx context.Context, l lang) panelView {
	counts, _ := b.st.LessonCounts(ctx)
	var sb strings.Builder
	sb.WriteString(l.t("🧠 <b>یادگیری</b>\n\nدرس‌های اجرا در مهارت‌ها ذخیره می‌شوند. برگشت: ↩️ یا <code>/learn undo ID</code>."))
	fmt.Fprint(&sb, l.f("\n\n✅ در حال استفاده: <b>%s</b> · 📝 در انتظار: %s · ❌ ردشده یا پس‌گرفته: %s", l.num(counts[store.LessonApproved]),
		l.num(counts[store.LessonPending]), l.num(counts[store.LessonRejected])))
	fmt.Fprint(&sb, l.f("\n🔁 یادگیری: %s · ⚡ خودکار: %s", onOff(l, b.learningOn()), onOff(l, b.st.SettingBool(settingLearnAuto, true))))
	if logs, err := b.st.LessonLogs(ctx, store.LessonReviewed, 1); err == nil && len(logs) > 0 {
		fmt.Fprint(&sb, l.f("\n🔍 آخرین بازبینی: %s · %s", ago(l, &logs[0].CreatedAt), esc(firstLine(logs[0].Note))))
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(b.button(l.t("🧩 مهارت‌های ساخته‌شده"), "a:learn:skills", telegram.StylePrimary)),
		row(b.button(l.t("🕘 آخرین درس‌ها"), "a:learn:log:0", telegram.StylePrimary)),
		row(b.button(l.t("⚙️ تنظیمات یادگیری"), "a:learn:cfg", ""), b.button(l.t("✍️ یاد بده"), "a:learn:teach", telegram.StyleSuccess)),
		row(b.back(l, "a:home")),
	)}
}

func (b *Bot) viewLearnSkills(ctx context.Context, l lang) panelView {
	var rows [][]telegram.InlineKeyboardButton
	var sb strings.Builder
	sb.WriteString(l.t("🧩 <b>مهارت‌های ساخته‌شده</b>\n\nهر زمینه یه مهارت داره که درس‌های تأییدشده‌اش توشه."))
	fields := make([]string, 0, len(lessonCategories))
	for f := range lessonCategories {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		list, _ := b.st.Lessons(ctx, store.LessonApproved, f, 200)
		if len(list) == 0 {
			continue
		}
		uses := 0
		if sk := b.app.Agent.Skills; sk != nil {
			if s, ok := sk.Get(lessonSkill + f); ok {
				uses = s.Uses
			}
		}
		fmt.Fprint(&sb, l.f("\n• <code>%s</code>: %s درس · %s بار خونده شده", esc(lessonSkill+f), l.num(len(list)), l.num(uses)))
		rows = append(rows, row(b.button(lessonSkill+f, "a:learn:sk:"+f, "")))
	}
	if len(rows) == 0 {
		sb.WriteString(l.t("\n\nهنوز مهارتی ساخته نشده."))
	}
	rows = append(rows, row(b.back(l, "a:learn")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) viewLearnSkill(ctx context.Context, l lang, field string) panelView {
	list, _ := b.st.Lessons(ctx, store.LessonApproved, field, 200)
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("🧩 <b>%s</b>\n%s\n", esc(lessonSkill+field), esc(lessonCategories[field])))
	var rows [][]telegram.InlineKeyboardButton
	for _, x := range list {
		fmt.Fprintf(&sb, "\n• <b>%s</b>: %s", esc(x.Title), esc(textutil.TruncateRunes(x.Rule, 160, "…")))
		rows = append(rows, row(b.button("✏️ "+textutil.TruncateRunes(x.Title, 40, "…"), fmt.Sprintf("a:learn:ln:%d", x.ID), "")))
	}
	if len(list) > 0 {
		rows = append(rows, row(b.button(l.t("🗑 حذف کل مهارت"), "a:learn:skdel:"+field, telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:learn:skills")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) viewLearnLog(ctx context.Context, l lang, page int) panelView {
	logs, _ := b.st.LessonLogs(ctx, "", 200)
	var sb strings.Builder
	sb.WriteString(l.t("🕘 <b>آخرین درس‌ها</b>"))
	if len(logs) == 0 {
		sb.WriteString(l.t("\n\nهنوز چیزی یاد نگرفتم."))
		return panelView{html: sb.String(), kb: keyboard(row(b.back(l, "a:learn:cfg")))}
	}
	pages := (len(logs) + lessonLogPage - 1) / lessonLogPage
	page = min(max(page, 0), pages-1)
	var rows [][]telegram.InlineKeyboardButton
	for _, x := range logs[page*lessonLogPage : min(len(logs), (page+1)*lessonLogPage)] {
		mark := map[string]string{store.LessonAdopted: "🧠", store.LessonReverted: "↩️", store.LessonReviewed: "🔍"}[x.Action]
		fmt.Fprintf(&sb, "\n%s <code>#%d</code> %s · %s", mark, x.LessonID, esc(textutil.TruncateRunes(x.Title, 60, "…")), ago(l, &x.CreatedAt))
		if x.Action == store.LessonReviewed {
			fmt.Fprintf(&sb, "\n   <i>%s</i>", esc(textutil.TruncateRunes(strings.ReplaceAll(x.Note, "\n", " · "), 200, "…")))
		}
		if x.Action == store.LessonAdopted {
			if cur, err := b.st.Lesson(ctx, x.LessonID); err == nil && cur.Status == store.LessonApproved {
				rows = append(rows, row(b.button("↩️ "+textutil.TruncateRunes(x.Title, 40, "…"), fmt.Sprintf("a:learn:no:%d", x.LessonID), "")))
			}
		}
	}
	var nav []telegram.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, b.button("◀️", fmt.Sprintf("a:learn:log:%d", page-1), ""))
	}
	if page < pages-1 {
		nav = append(nav, b.button("▶️", fmt.Sprintf("a:learn:log:%d", page+1), ""))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, row(b.back(l, "a:learn")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

var learnReviewSteps = map[int]int{learnReviewDefault: 50, 50: 100, 100: 0, 0: learnReviewDefault}

func (b *Bot) viewLearnSettings(ctx context.Context, l lang) panelView {
	counts, _ := b.st.LessonCounts(ctx)
	auto := b.st.SettingBool(settingLearnAuto, true)
	var sb strings.Builder
	sb.WriteString(l.t("⚙️ <b>تنظیمات یادگیری</b>\n"))
	fmt.Fprint(&sb, l.f("\n🔁 یادگیری از کارها: %s", onOff(l, b.learningOn())))
	fmt.Fprint(&sb, l.f("\n⚡ اعمال خودکار (بدون تأیید): %s", onOff(l, auto)))
	fmt.Fprint(&sb, l.f("\n🚦 سقف: %s درس برای هر کاربر و %s درس در روز", l.num(lessonsPerUser), l.num(lessonsPerDay)))
	if n := b.learnReviewEvery(); n > 0 {
		fmt.Fprint(&sb, l.f("\n🔍 بازبینی پس‌زمینه: هر %s کار (تکراری‌ها ادغام، بی‌استفاده‌ها علامت‌گذاری)", l.num(n)))
	} else {
		sb.WriteString(l.t("\n🔍 بازبینی پس‌زمینه: خاموش"))
	}
	fmt.Fprint(&sb, l.f("\n🌙 امتحان شبانه: %s · 📨 گزارش هفتگی: %s", onOff(l, b.evalsOn()), onOff(l, b.st.SettingBool(settingWeeklyReport, true))))
	toggle := b.button(l.t("⏸ یادگیری: خاموش کن"), "a:learn:toggle", "")
	if !b.learningOn() {
		toggle = b.button(l.t("▶️ یادگیری: روشن کن"), "a:learn:toggle", telegram.StyleSuccess)
	}
	autoBtn := l.t("⚡ خودکار: روشن")
	if !auto {
		autoBtn = l.t("⚡ خودکار: خاموش (با تأیید)")
	}
	review := l.t("🔍 بازبینی: خاموش")
	if n := b.learnReviewEvery(); n > 0 {
		review = l.f("🔍 بازبینی: هر %s کار", l.num(n))
	}
	weekly := map[bool]string{true: l.t("📨 گزارش هفتگی: روشن"), false: l.t("📨 گزارش هفتگی: خاموش")}[b.st.SettingBool(settingWeeklyReport, true)]
	return panelView{html: sb.String(), kb: keyboard(
		row(toggle, b.button(autoBtn, "a:learn:auto", "")),
		row(b.button(review, "a:learn:rev", ""), b.button(l.t("🔍 الان بازبینی کن"), "a:learn:revnow", "")),
		row(b.button(l.f("📝 در انتظار (%s)", l.num(counts[store.LessonPending])), "a:learn:list:pending:0", ""),
			b.button(l.t("❌ ردشده‌ها"), "a:learn:list:rejected:0", "")),
		row(b.button(l.t("🧪 امتحان دوره‌ای"), "a:learn:exam", ""), b.button(weekly, "a:learn:weekly", "")),
		row(b.button(l.t("📈 گزارش ۷ روز اخیر"), "a:learn:report", "")),
		row(b.back(l, "a:learn")),
	)}
}

func (b *Bot) viewLessons(ctx context.Context, l lang, status string, page int) panelView {
	list, err := b.st.Lessons(ctx, status, "", 200)
	if err != nil {
		return panelView{html: l.t(textError), kb: keyboard(row(b.back(l, "a:learn:cfg")))}
	}
	title := map[string]string{store.LessonPending: l.t("📝 <b>درس‌های در انتظار</b>"), store.LessonApproved: l.t("✅ <b>درس‌های در حال استفاده</b>"),
		store.LessonRejected: l.t("❌ <b>درس‌های ردشده</b>")}[status]
	if len(list) == 0 {
		return panelView{html: title + "\n\n" + l.t("چیزی نیست."), kb: keyboard(row(b.back(l, "a:learn:cfg")))}
	}
	pages := (len(list) + lessonsPage - 1) / lessonsPage
	page = min(max(page, 0), pages-1)
	var rows [][]telegram.InlineKeyboardButton
	for _, x := range list[page*lessonsPage : min(len(list), (page+1)*lessonsPage)] {
		label := textutil.TruncateRunes(x.Title, 40, "…")
		if x.Seen > 1 {
			label += fmt.Sprintf(" ×%d", x.Seen)
		}
		rows = append(rows, row(b.button(label, fmt.Sprintf("a:learn:ln:%d", x.ID), "")))
	}
	var nav []telegram.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, b.button("◀️", fmt.Sprintf("a:learn:list:%s:%d", status, page-1), ""))
	}
	if page < pages-1 {
		nav = append(nav, b.button("▶️", fmt.Sprintf("a:learn:list:%s:%d", status, page+1), ""))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}
	rows = append(rows, row(b.back(l, "a:learn:cfg")))
	return panelView{html: title + l.f("\n\n%s درس، بیشترین تکرار اول. روی هر کدوم بزن تا ببینی و تصمیم بگیری.", l.num(len(list))), kb: keyboard(rows...)}
}

func (b *Bot) viewLesson(ctx context.Context, l lang, id int64) panelView {
	x, err := b.st.Lesson(ctx, id)
	if err != nil {
		return b.viewLearn(ctx, l)
	}
	status := map[string]string{store.LessonPending: l.t("📝 در انتظار"), store.LessonApproved: l.t("✅ در حال استفاده"),
		store.LessonRejected: l.t("❌ ردشده")}[x.Status]
	var sb strings.Builder
	fmt.Fprintf(&sb, "🧠 <b>%s</b>\n\n%s", esc(x.Title), esc(x.Rule))
	taught := strings.Contains(x.Key, taughtMark)
	switch {
	case taught:
		fmt.Fprint(&sb, l.f("\n\n✍️ یاد داده‌شده توسط ادمین · %s", status))
	default:
		fmt.Fprint(&sb, l.f("\n\n📂 زمینه: <code>%s</code> · 🔁 %s بار یاد گرفته شده · %s", esc(x.Category), l.num(x.Seen), status))
	}
	if x.Tools != "" {
		fmt.Fprint(&sb, l.f("\n🛠 ابزارها: <code>%s</code>", esc(x.Tools)))
	}
	switch {
	case x.Category == categoryAlways:
		sb.WriteString(l.t("\n🌐 کجا: <b>همیشه</b>، توی همه جواب‌ها"))
	case x.Status == store.LessonApproved:
		fmt.Fprint(&sb, l.f("\n🧩 کجا: وقتی سر کارهای این زمینه‌ست (اسکیل <code>%s</code>)", esc(lessonSkill+x.Category)))
	}
	act := func(a string) string { return fmt.Sprintf("a:learn:%s:%d", a, x.ID) }
	var rows [][]telegram.InlineKeyboardButton
	switch x.Status {
	case store.LessonPending:
		rows = append(rows, row(b.button(l.t("✅ تأیید"), act("ok"), telegram.StyleSuccess), b.button(l.t("❌ رد"), act("no"), telegram.StyleDanger)))
	case store.LessonApproved:
		rows = append(rows, row(b.button(l.t("↩️ برداشتن از اسکیل"), act("undo"), ""), b.button(l.t("❌ رد"), act("no"), telegram.StyleDanger)))
	default:
		rows = append(rows, row(b.button(l.t("↩️ برگردون به انتظار"), act("undo"), "")))
	}
	rows = append(rows, row(b.button(l.t("✏️ ویرایش متن"), act("edit"), ""), b.button(l.t("🗂 کجا به کار بیاد"), act("where"), "")),
		row(b.back(l, "a:learn:skills")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) handleLearnPanel(ctx context.Context, l lang, adminID int64, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	id, _ := strconv.ParseInt(arg(3), 10, 64)
	switch arg(2) {
	case "toggle":
		_ = b.st.SetSetting(ctx, settingLearn, strconv.FormatBool(!b.learningOn()))
		answer("", false)
		show(b.viewLearnSettings(ctx, l))
		return
	case "weekly":
		_ = b.st.SetSetting(ctx, settingWeeklyReport, strconv.FormatBool(!b.st.SettingBool(settingWeeklyReport, true)))
		answer("", false)
		show(b.viewLearnSettings(ctx, l))
		return
	case "auto":
		_ = b.st.SetSetting(ctx, settingLearnAuto, strconv.FormatBool(!b.st.SettingBool(settingLearnAuto, true)))
		answer("", false)
		show(b.viewLearnSettings(ctx, l))
		return
	case "rev":
		next, ok := learnReviewSteps[b.learnReviewEvery()]
		if !ok {
			next = learnReviewDefault
		}
		_ = b.st.SetSetting(ctx, settingLearnReview, strconv.Itoa(next))
		answer("", false)
		show(b.viewLearnSettings(ctx, l))
		return
	case "revnow":
		entry, err := b.reviewLessons(ctx)
		if err != nil {
			answer(l.t("بازبینی نشد: ")+err.Error(), true)
		} else {
			answer(firstLine(entry.Note), false)
		}
		show(b.viewLearnLog(ctx, l, 0))
		return
	case "cfg":
		answer("", false)
		show(b.viewLearnSettings(ctx, l))
		return
	case "skills":
		answer("", false)
		show(b.viewLearnSkills(ctx, l))
		return
	case "sk":
		answer("", false)
		show(b.viewLearnSkill(ctx, l, arg(3)))
		return
	case "skdel":
		field := arg(3)
		list, _ := b.st.Lessons(ctx, store.LessonApproved, field, 500)
		for _, x := range list {
			_ = b.st.DecideLesson(ctx, x.ID, store.LessonRejected)
		}
		_ = b.rebuildLessons(ctx, field)
		_, _ = b.st.AddLessonLog(ctx, store.LessonLog{Category: field, Action: store.LessonReverted, Title: lessonSkill + field, Note: "removed by an admin"})
		answer(l.t("🗑 حذف شد"), false)
		show(b.viewLearnSkills(ctx, l))
		return
	case "log":
		page, _ := strconv.Atoi(arg(3))
		answer("", false)
		show(b.viewLearnLog(ctx, l, page))
		return
	case "exam":
		answer("", false)
		show(b.viewExams(ctx, l))
		return
	case "examtoggle":
		_ = b.st.SetSetting(ctx, settingEvals, strconv.FormatBool(!b.evalsOn()))
		answer("", false)
		show(b.viewExams(ctx, l))
		return
	case "examrun":
		if !b.prov.Configured() || b.evalBusy.Load() {
			answer(l.t("الان نمی‌شه: یه امتحان در حال اجراست یا پرووایدری نیست"), true)
			return
		}
		answer(l.t("🧪 امتحان شروع شد؛ چند دقیقه طول می‌کشه و نتیجه رو برات می‌فرستم."), true)
		b.startExam(adminID)
		return
	case "report":
		answer("", false)
		show(panelView{html: b.weeklyReport(ctx, l, time.Now().Add(-reportDays*24*time.Hour)), kb: keyboard(row(b.back(l, "a:learn:cfg")))})
		return
	case "list":
		page, _ := strconv.Atoi(arg(4))
		answer("", false)
		show(b.viewLessons(ctx, l, arg(3), page))
		return
	case "ln":
		answer("", false)
		show(b.viewLesson(ctx, l, id))
		return
	case "ok", "no", "undo":
		x, err := b.st.Lesson(ctx, id)
		if err != nil {
			answer(l.t("این درس دیگه نیست"), true)
			show(b.viewLearn(ctx, l))
			return
		}
		var err2 error
		switch {
		case arg(2) == "ok":
			err2 = b.applyLesson(ctx, id, x)
		case x.Status == store.LessonApproved:
			_, err2 = b.undoLesson(ctx, id)
		default:
			status := map[string]string{"no": store.LessonRejected, "undo": store.LessonPending}[arg(2)]
			if err2 = b.st.DecideLesson(ctx, id, status); err2 == nil {
				err2 = b.rebuildLessons(ctx, x.Category)
			}
		}
		if err2 != nil {
			answer(l.t("ذخیره شد، ولی اسکیل ساخته نشد: ")+err2.Error(), true)
			show(b.viewLesson(ctx, l, id))
			return
		}
		answer(map[string]string{"ok": l.t("✅ تأیید شد و وارد اسکیل شد"), "no": l.t("❌ رد شد"), "undo": l.t("↩️ برگشت")}[arg(2)], false)
		show(b.viewLesson(ctx, l, id))
		return
	case "edit":
		answer("", false)
		ask(&adminInput{kind: "ln_edit", target: id}, l.t("✏️ متن جدید درس رو بفرست: خط اول عنوان کوتاه، بعدش خود قانون. اگه فقط یه خط بفرستی، فقط قانون عوض می‌شه."))
		return
	case "teach":
		answer("", false)
		ask(&adminInput{kind: "ln_teach"}, l.t(textTeachHelp))
		return
	case "where":
		answer("", false)
		show(b.viewLessonPlace(ctx, l, id))
		return
	case "mv":
		note, err := b.moveLesson(ctx, l, id, arg(4))
		switch {
		case err != nil:
			answer(l.t("جابه‌جا نشد: ")+err.Error(), true)
		case note != "":
			answer(note, true)
		default:
			answer(l.t("✅ جاش عوض شد"), false)
		}
		show(b.viewLesson(ctx, l, id))
		return
	default:
		answer("", false)
	}
	show(b.viewLearn(ctx, l))
}

func (b *Bot) learnInput(ctx context.Context, l lang, m *telegram.Message, in *adminInput, done func(panelView)) {
	x, err := b.st.Lesson(ctx, in.target)
	if err != nil {
		done(b.viewLearn(ctx, l))
		return
	}
	text := strings.TrimSpace(m.Text)
	title, rule := x.Title, text
	if first, rest, ok := strings.Cut(text, "\n"); ok && strings.TrimSpace(rest) != "" {
		title, rule = textutil.OneLine(first), strings.TrimSpace(rest)
	}
	if rule == "" || len([]rune(title)) > 120 || len([]rune(rule)) > 1500 {
		b.reply(m, l.t("متن خالی یا خیلی بلنده؛ دوباره بفرست (عنوان تا ۱۲۰ و قانون تا ۱۵۰۰ کاراکتر)."))
		return
	}
	_ = b.st.EditLesson(ctx, x.ID, title, rule)
	if x.Status == store.LessonApproved {
		_ = b.rebuildLessons(ctx, x.Category)
	}
	done(b.viewLesson(ctx, l, x.ID))
}

func (b *Bot) learnLabel(ctx context.Context, l lang) string {
	if counts, err := b.st.LessonCounts(ctx); err == nil && counts[store.LessonPending] > 0 {
		return l.f("🧠 یادگیری (%s درس جدید)", l.num(counts[store.LessonPending]))
	}
	return l.t("🧠 یادگیری")
}

func (b *Bot) viewExams(ctx context.Context, l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🧪 <b>امتحان دوره‌ای</b>\n\nیه سری درخواست ثابت و سخت (حساب، کد، فایل، نمودار، حفظ دستورها، صداقت، فارسی) که هر شب ساعت ۴، بعد از عوض شدن مدل یا پرووایدر، یا با دکمه زیر اجرا می‌شن و با کد نمره می‌گیرن. ") +
		l.t("اگه نمره ۱۰ نمره افت کنه یا زیر ۷۰٪ بیاد، به مالک‌ها خبر داده می‌شه. امتحان توی فضای جدا اجرا می‌شه و چیزی برای کسی فرستاده نمی‌شه."))
	fmt.Fprint(&sb, l.f("\n\n🌙 امتحان شبانه: %s", onOff(l, b.evalsOn())))
	runs, _ := b.st.EvalRuns(ctx, 8)
	if len(runs) == 0 {
		sb.WriteString(l.t("\n\nهنوز امتحانی اجرا نشده."))
	} else {
		sb.WriteString(l.t("\n\n📊 <b>آخرین امتحان‌ها</b>"))
		for _, r := range runs {
			fmt.Fprint(&sb, l.f("\n• <b>%s٪</b> · <code>%s</code> · %s · %s", l.num(r.Score()), esc(r.Model), evalReasonText(l, r.Reason), ago(l, &r.StartedAt)))
		}
		sb.WriteString(l.t("\n\n🔍 <b>آخرین بار</b>"))
		for _, c := range parseEvalResults(runs[0].Details) {
			mark := "✅"
			if !c.Pass {
				mark = "❌"
			}
			fmt.Fprintf(&sb, "\n%s %s", mark, esc(c.name(l)))
			if !c.Pass && c.Reason != "" {
				fmt.Fprintf(&sb, ": <i>%s</i>", esc(c.Reason))
			}
		}
	}
	toggle := l.t("🌙 شبانه: روشن")
	if !b.evalsOn() {
		toggle = l.t("🌙 شبانه: خاموش")
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(b.button(l.t("▶️ الان امتحان بده"), "a:learn:examrun", telegram.StyleSuccess), b.button(toggle, "a:learn:examtoggle", "")),
		row(b.back(l, "a:learn:cfg")),
	)}
}

func (b *Bot) startExam(adminID int64) {
	b.wg.Add(1)
	go func() {
		defer guard.Recover("learn_admin")
		defer b.wg.Done()
		ctx, cancel := b.ctx(time.Duration(len(evalCases)+2) * evalCaseTime)
		defer cancel()
		l := b.lang(adminID)
		run, err := b.runEvals(ctx, "manual")
		nctx, ncancel := b.ctx(15 * time.Second)
		defer ncancel()
		if err != nil {
			b.sendHTML(nctx, adminID, l.t("🧪 امتحان اجرا نشد: ")+esc(err.Error()), telegram.SendOptions{})
			return
		}
		if run != nil {
			b.showNew(nctx, adminID, b.viewExams(nctx, l))
		}
	}()
}
