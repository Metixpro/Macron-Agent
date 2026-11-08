package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// The 🧠 Learning section of the panel: the lessons waiting for an admin,
// the ones in use, and learning on or off.

const lessonsPage = 8

func (b *Bot) viewLearn(ctx context.Context, l lang) panelView {
	counts, _ := b.st.LessonCounts(ctx)
	var sb strings.Builder
	sb.WriteString(l.t("🧠 <b>یادگیری از تجربه</b>\n\nوقتی یه قدم از کاری خطا بده و مکرون با یه راه دیگه انجامش بده، یه درس کلی ازش می‌نویسه (بدون هیچ اطلاعاتی از کاربر). ") +
		l.t("درس‌ها اینجا منتظر تو می‌مونن؛ فقط درسی که تأیید کنی وارد اسکیل‌ها می‌شه و از اون به بعد توی همه کارها به کار میاد. درسی که رد کنی دیگه پیشنهاد نمی‌شه.") +
		l.t("\n\n✍️ <b>یاد دادن دستی:</b> هر چیزی رو خودت هم می‌تونی یادش بدی (دکمه زیر یا <code>/teach</code>؛ حتی با ریپلای روی یه جواب اشتباه). درس‌هایی که «همیشه» باشن توی همه جواب‌ها رعایت می‌شن."))
	fmt.Fprint(&sb, l.f("\n\n📝 در انتظار: <b>%s</b> · ✅ در حال استفاده: <b>%s</b> · ❌ ردشده: %s", l.num(counts[store.LessonPending]),
		l.num(counts[store.LessonApproved]), l.num(counts[store.LessonRejected])))
	if always, err := b.st.Lessons(ctx, store.LessonApproved, categoryAlways, maxAlwaysRules); err == nil && len(always) > 0 {
		fmt.Fprint(&sb, l.f("\n🌐 قانون‌های همیشگی: <b>%s</b>", l.num(len(always))))
	}
	fmt.Fprint(&sb, l.f("\n🔁 یادگیری از کارها: %s", onOff(l, b.learningOn())))
	if runs, err := b.st.EvalRuns(ctx, 1); err == nil && len(runs) > 0 {
		fmt.Fprint(&sb, l.f("\n🧪 آخرین امتحان: <b>%s٪</b> (%s از %s) با <code>%s</code>، %s", l.num(runs[0].Score()), l.num(runs[0].Passed),
			l.num(runs[0].Total), esc(runs[0].Model), ago(l, &runs[0].StartedAt)))
	}
	weekly := b.st.SettingBool(settingWeeklyReport, true)
	fmt.Fprint(&sb, l.f("\n📨 گزارش هفتگی (شنبه‌ها صبح، برای مالک‌ها): %s", onOff(l, weekly)))
	toggle := b.button(l.t("⏸ خاموش کن"), "a:learn:toggle", "")
	if !b.learningOn() {
		toggle = b.button(l.t("▶️ روشن کن"), "a:learn:toggle", telegram.StyleSuccess)
	}
	return panelView{html: sb.String(), kb: keyboard(
		row(b.button(l.t("✍️ یاد بده"), "a:learn:teach", telegram.StyleSuccess)),
		row(b.button(l.f("📝 در انتظار (%s)", l.num(counts[store.LessonPending])), "a:learn:list:pending:0", telegram.StylePrimary),
			b.button(l.t("✅ در حال استفاده"), "a:learn:list:approved:0", "")),
		row(toggle, b.button(l.t("❌ ردشده‌ها"), "a:learn:list:rejected:0", "")),
		row(b.button(l.t("🧪 امتحان دوره‌ای"), "a:learn:exam", telegram.StylePrimary)),
		row(b.button(l.t("📈 گزارش ۷ روز اخیر"), "a:learn:report", telegram.StylePrimary),
			b.button(map[bool]string{true: l.t("📨 گزارش هفتگی: روشن"), false: l.t("📨 گزارش هفتگی: خاموش")}[weekly], "a:learn:weekly", "")),
		row(b.back(l, "a:home")),
	)}
}

func (b *Bot) viewLessons(ctx context.Context, l lang, status string, page int) panelView {
	list, err := b.st.Lessons(ctx, status, "", 200)
	if err != nil {
		return panelView{html: l.t(textError), kb: keyboard(row(b.back(l, "a:learn")))}
	}
	title := map[string]string{store.LessonPending: l.t("📝 <b>درس‌های در انتظار</b>"), store.LessonApproved: l.t("✅ <b>درس‌های در حال استفاده</b>"),
		store.LessonRejected: l.t("❌ <b>درس‌های ردشده</b>")}[status]
	if len(list) == 0 {
		return panelView{html: title + "\n\n" + l.t("چیزی نیست."), kb: keyboard(row(b.back(l, "a:learn")))}
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
	rows = append(rows, row(b.back(l, "a:learn")))
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
		row(b.back(l, "a:learn:list:"+x.Status+":0")))
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
	case "weekly":
		_ = b.st.SetSetting(ctx, settingWeeklyReport, strconv.FormatBool(!b.st.SettingBool(settingWeeklyReport, true)))
		answer("", false)
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
		show(panelView{html: b.weeklyReport(ctx, l, time.Now().Add(-reportDays*24*time.Hour)), kb: keyboard(row(b.back(l, "a:learn")))})
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
		status := map[string]string{"ok": store.LessonApproved, "no": store.LessonRejected, "undo": store.LessonPending}[arg(2)]
		if err := b.st.DecideLesson(ctx, id, status); err == nil {
			err = b.rebuildLessons(ctx, x.Category) // the skill follows what is approved
			if err != nil {
				answer(l.t("ذخیره شد، ولی اسکیل ساخته نشد: ")+err.Error(), true)
				show(b.viewLesson(ctx, l, id))
				return
			}
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

// learnInput takes an admin's edit of a lesson.
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

// learnLabel is the panel's button, with the lessons waiting.
func (b *Bot) learnLabel(ctx context.Context, l lang) string {
	if counts, err := b.st.LessonCounts(ctx); err == nil && counts[store.LessonPending] > 0 {
		return l.f("🧠 یادگیری (%s درس جدید)", l.num(counts[store.LessonPending]))
	}
	return l.t("🧠 یادگیری")
}

// viewExams shows the exam's latest runs and the cases of the last one.
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
		row(b.back(l, "a:learn")),
	)}
}

// startExam runs the exam in the background and sends the result to the admin who asked.
func (b *Bot) startExam(adminID int64) {
	b.wg.Add(1)
	go func() {
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
