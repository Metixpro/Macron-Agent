package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/sched"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Reminders and scheduled tasks: "مکرون فردا ساعت ۱۱ صبح یادم بنداز به مهدی
// زنگ بزنم", "مکرون هر روز ساعت ۱۰ قیمت تتر و طلا رو بفرست تو این گروه".
// The model turns the request into a schedule (schedule tool); a ticker
// fires due schedules: a reminder is a message, a task is a run of the
// assistant whose answer is posted in the chat.

const (
	toolSchedule       = "schedule"
	toolListSchedules  = "list_schedules"
	toolCancelSchedule = "cancel_schedule"

	schedulerTick        = 15 * time.Second
	maxSchedulesPerUser  = 20
	maxSchedulesPerChat  = 30
	minTaskInterval      = time.Hour
	maxScheduleAhead     = 366 * 24 * time.Hour
	scheduleMaxFailures  = 3
	lateReminderNoticeAt = 10 * time.Minute
	// onceRetries: a one-time schedule whose run failed (the model was down,
	// Telegram did not answer) is tried again this many times.
	onceRetries = 3
)

// onceRetryAfter is the wait before retry n (0-based) of a one-time schedule:
// 2, 6, 18 minutes (a variable for the tests).
var onceRetryAfter = func(n int) time.Duration {
	d := 2 * time.Minute
	for range n {
		d *= 3
	}
	return d
}

func (b *Bot) scheduleTools(u *store.User, m *telegram.Message) []agent.LocalTool {
	// "required" only when something is: strict providers (Gemini, some
	// OpenAI proxies) refuse "required": null and native tool calls with it.
	obj := func(props map[string]any, required ...string) map[string]any {
		m := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			m["required"] = required
		}
		return m
	}
	str := func(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }
	return []agent.LocalTool{
		{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolSchedule,
				Description: "A reminder (a message then) or a task (you do it then and post the result), once or repeating.",
				Parameters: obj(map[string]any{
					"kind":          map[string]any{"type": "string", "enum": []string{store.ScheduleReminder, store.ScheduleTask}},
					"text":          str("reminder: the user's words; task: a full instruction for you then"),
					"at":            str("Local 'YYYY-MM-DD HH:MM' (" + b.loc.String() + "); repeats: the time of day"),
					"in_minutes":    map[string]any{"type": "integer", "description": "Or minutes from now"},
					"repeat":        map[string]any{"type": "string", "enum": []string{"once", "daily", "weekly", "monthly", "interval"}},
					"days":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "weekly: e.g. [\"sat\",\"mon\"]"},
					"day_of_month":  map[string]any{"type": "integer", "description": "monthly"},
					"every_minutes": map[string]any{"type": "integer", "description": "interval (reminders ≥30, tasks ≥60)"},
					"title":         str("Short label"),
				}, "kind", "text"),
			}},
			Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
				return agent.ToolOutput{Text: b.createSchedule(ctx, u, m, args, time.Now())}, nil
			},
		},
		{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolListSchedules,
				Description: "The user's active reminders and tasks.",
				Parameters:  obj(map[string]any{}),
			}},
			Handler: func(ctx context.Context, _ map[string]any) (agent.ToolOutput, error) {
				return agent.ToolOutput{Text: b.describeSchedules(ctx, u.ID, m.Chat.ID)}, nil
			},
		},
		{
			Def: llm.Tool{Type: "function", Function: llm.ToolDef{
				Name:        toolCancelSchedule,
				Description: "Cancel a reminder or task by its number.",
				Parameters:  obj(map[string]any{"id": map[string]any{"type": "integer"}}, "id"),
			}},
			Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
				id := int64(numArg(args["id"]))
				owner := u.ID
				if b.isAdmin(u.ID) {
					owner = 0
				}
				if ok, err := b.st.CancelSchedule(ctx, id, owner); err != nil || !ok {
					return agent.ToolOutput{Text: fmt.Sprintf("error: there is no active schedule #%d of this user", id)}, nil
				}
				return agent.ToolOutput{Text: fmt.Sprintf("Schedule #%d cancelled.", id)}, nil
			},
		},
	}
}

func numArg(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int:
		return float64(x)
	case int64:
		return float64(x)
	case string:
		f, _ := strconv.ParseFloat(strings.TrimSpace(x), 64)
		return f
	}
	return 0
}

// createSchedule validates a schedule request and stores it. The returned
// text goes back to the model (errors say what to fix).
func (b *Bot) createSchedule(ctx context.Context, u *store.User, m *telegram.Message, args map[string]any, now time.Time) string {
	kind, _ := args["kind"].(string)
	text, _ := args["text"].(string)
	text = strings.TrimSpace(text)
	if kind != store.ScheduleReminder && kind != store.ScheduleTask {
		return "error: kind must be reminder or task"
	}
	if text == "" {
		return "error: text is required"
	}
	repeat, _ := args["repeat"].(string)
	repeat = strings.ToLower(strings.TrimSpace(repeat))

	var first time.Time
	if in := numArg(args["in_minutes"]); in > 0 {
		first = now.Add(time.Duration(in * float64(time.Minute))).Truncate(time.Minute)
	} else if at, _ := args["at"].(string); strings.TrimSpace(at) != "" {
		t, err := sched.ParseLocal(at, b.loc)
		if err != nil {
			return "error: " + err.Error()
		}
		first = t
	}

	var rule sched.Rule
	switch repeat {
	case "", "once":
	case "interval":
		every := time.Duration(numArg(args["every_minutes"])) * time.Minute
		min := sched.MinInterval
		if kind == store.ScheduleTask {
			min = minTaskInterval
		}
		if every < min {
			return fmt.Sprintf("error: every_minutes must be at least %d for a %s", int(min/time.Minute), kind)
		}
		rule = sched.Rule{Kind: "every", Every: every}
	case "daily", "weekly", "monthly":
		if first.IsZero() {
			return "error: give 'at' (YYYY-MM-DD HH:MM) so the time of day is known"
		}
		lt := first.In(b.loc)
		rule = sched.Rule{Kind: repeat, Hour: lt.Hour(), Minute: lt.Minute()}
		if repeat == "weekly" {
			for _, d := range stringList(args["days"]) {
				wd, ok := sched.ParseDay(d)
				if !ok {
					return fmt.Sprintf("error: unknown weekday %q (use sat, sun, mon, tue, wed, thu, fri)", d)
				}
				rule.Days = append(rule.Days, wd)
			}
			if len(rule.Days) == 0 {
				rule.Days = []time.Weekday{lt.Weekday()}
			}
		}
		if repeat == "monthly" {
			rule.Day = int(numArg(args["day_of_month"]))
			if rule.Day == 0 {
				rule.Day = lt.Day()
			}
			if rule.Day < 1 || rule.Day > 31 {
				return "error: day_of_month must be 1–31"
			}
		}
	default:
		return "error: repeat must be once, daily, weekly, monthly or interval"
	}

	switch {
	case rule.Once():
		if first.IsZero() {
			return "error: give 'at' (YYYY-MM-DD HH:MM) or in_minutes"
		}
		if first.Before(now.Add(-time.Minute)) {
			return fmt.Sprintf("error: %s has already passed (it is now %s); pick a future time",
				first.In(b.loc).Format("2006-01-02 15:04"), now.In(b.loc).Format("Monday 2006-01-02 15:04"))
		}
		if first.Before(now) {
			first = now
		}
	case rule.Kind == "every":
		if first.IsZero() || !first.After(now) {
			first = now.Add(rule.Every)
		}
	default:
		// The given moment counts if it is in the future and fits the rule.
		if !first.After(now) || (rule.Kind == "weekly" && !containsWeekday(rule.Days, first.In(b.loc).Weekday())) ||
			(rule.Kind == "monthly" && first.In(b.loc).Day() != rule.Day) {
			first = rule.Next(now, b.loc)
		}
	}
	if first.After(now.Add(maxScheduleAhead)) {
		return "error: schedules can be at most a year ahead"
	}

	if !b.isAdmin(u.ID) {
		byUser, byChat, err := b.st.CountActiveSchedules(ctx, u.ID, m.Chat.ID)
		if err != nil {
			return "error: " + err.Error()
		}
		if byUser >= maxSchedulesPerUser || byChat >= maxSchedulesPerChat {
			return fmt.Sprintf("error: the limit of %d active schedules is reached; cancel one first (list_schedules, cancel_schedule)", maxSchedulesPerUser)
		}
	}
	title, _ := args["title"].(string)
	title = strings.TrimSpace(title)
	if title == "" {
		title = textutil.TruncateRunes(textutil.OneLine(text), 60, "…")
	}
	id, err := b.st.AddSchedule(ctx, store.Schedule{UserID: u.ID, ChatID: m.Chat.ID, ChatType: m.Chat.Type, Kind: kind,
		Title: title, Body: text, Rule: rule.String(), NextRun: first, ThreadID: telegram.SendOptions{}.InTopic(m).ThreadID})
	if err != nil {
		return "error: " + err.Error()
	}
	return fmt.Sprintf("Scheduled #%d (%s, %q): first run %s (%s); repeats: %s. It will be posted in this chat. "+
		"Confirm to the user in one short sentence with the day and time; they can see and cancel it with /reminders.",
		id, kind, title, first.In(b.loc).Format("Monday 2006-01-02 15:04"), b.loc, rule.Describe())
}

func containsWeekday(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func (b *Bot) describeSchedules(ctx context.Context, userID, chatID int64) string {
	list, err := b.st.UserSchedules(ctx, userID)
	if err != nil {
		return "error: " + err.Error()
	}
	if len(list) == 0 {
		return "The user has no active reminders or scheduled tasks."
	}
	var sb strings.Builder
	for _, x := range list {
		where := "another chat"
		if x.ChatID == chatID {
			where = "this chat"
		}
		rule, _ := sched.Parse(x.Rule)
		if x.Status == store.SchedulePaused {
			fmt.Fprintf(&sb, "#%d %s %q — PAUSED after %d failures (last: %s); the user can turn it on again in /reminders, %s\n",
				x.ID, x.Kind, x.Title, x.Failures, textutil.TruncateRunes(x.LastError, 120, "…"), where)
			continue
		}
		fmt.Fprintf(&sb, "#%d %s %q — next %s, %s, %s\n", x.ID, x.Kind, x.Title, x.NextRun.In(b.loc).Format("Mon 2006-01-02 15:04"), rule.Describe(), where)
	}
	return sb.String()
}

// ---------------------------------------------------------------- running

// runScheduler fires due schedules until ctx ends.
func (b *Bot) runScheduler(ctx context.Context) {
	t := time.NewTicker(schedulerTick)
	defer t.Stop()
	for {
		b.runDueSchedules(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// runDueSchedules claims each due schedule (so it runs exactly once, even
// with several bot instances) and fires it in the background.
func (b *Bot) runDueSchedules(ctx context.Context, now time.Time) {
	qctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	due, err := b.st.DueSchedules(qctx, now, 20)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("scheduler: %v", err)
		}
		return
	}
	for _, x := range due {
		rule, err := sched.Parse(x.Rule)
		if err != nil {
			_, _ = b.st.CancelSchedule(qctx, x.ID, 0)
			continue
		}
		var next time.Time
		if !rule.Once() {
			next = rule.Next(now, b.loc) // missed occurrences are not replayed
		}
		ok, err := b.st.ClaimSchedule(qctx, x, next)
		if err != nil || !ok {
			continue
		}
		b.wg.Add(1)
		go func(x store.Schedule, rule sched.Rule) {
			defer b.wg.Done()
			err := b.fireSchedule(x, rule, now)
			msg := ""
			if err != nil {
				msg = err.Error()
				log.Printf("schedule #%d: %v", x.ID, err)
			}
			rctx, cancel := b.ctx(10 * time.Second)
			defer cancel()
			paused, _ := b.st.RecordScheduleResult(rctx, x.ID, msg, scheduleMaxFailures)
			switch {
			case err != nil && rule.Once() && x.Failures < onceRetries:
				// A one-time run is not lost to a passing failure (a cancelled
				// or ended schedule stays as it is).
				_, _ = b.st.RetrySchedule(rctx, x.ID, time.Now().Add(onceRetryAfter(x.Failures)))
			case err != nil && rule.Once():
				b.schedulePausedNotice(rctx, x, msg, false)
			case paused:
				b.schedulePausedNotice(rctx, x, msg, true)
			}
		}(x, rule)
	}
}

func (b *Bot) fireSchedule(x store.Schedule, rule sched.Rule, now time.Time) error {
	if b.personal && (x.ChatType != "private" || x.ChatID < 0) {
		ctx, cancel := b.ctx(10 * time.Second)
		defer cancel()
		_, _ = b.st.CancelSchedule(ctx, x.ID, 0)
		return errors.New("group schedules are disabled")
	}
	ctx, cancel := b.ctx(time.Minute)
	defer cancel()
	u, err := b.st.User(ctx, x.UserID)
	if err != nil {
		return err
	}
	// Nobody to post to any more: the user blocked the bot, or the bot left the group.
	if x.ChatType == "private" && u.BlockedBot {
		_, _ = b.st.CancelSchedule(ctx, x.ID, 0)
		return errors.New("the user blocked the bot")
	}
	if x.ChatType != "private" {
		if c, err := b.st.Chat(ctx, x.ChatID); err == nil && !c.Active() {
			_, _ = b.st.CancelSchedule(ctx, x.ID, 0)
			return errors.New("the bot is no longer in the chat")
		}
	}
	if x.Kind == store.ScheduleReminder {
		// A reminder uses no model: it costs no tokens.
		return b.sendReminder(ctx, x, u, rule, now.Sub(x.NextRun) > lateReminderNoticeAt)
	}
	m := &telegram.Message{From: &telegram.User{ID: u.ID, FirstName: u.FirstName, LastName: u.LastName, Username: u.Username},
		Chat: telegram.Chat{ID: x.ChatID, Type: x.ChatType}, Date: now.Unix(),
		IsTopicMessage: x.ThreadID != 0, MessageThreadID: x.ThreadID} // posted in the topic it was made in
	about := fmt.Sprintf("This is scheduled task #%d (%s) that the user set up earlier. Do it now and post the result for them. "+
		"They are not watching live, so don't ask questions or for confirmation; be concise and current.", x.ID, rule.Describe())
	return b.handlePrompt(u, m, x.Body, promptOptions{schedule: &x, noMemory: true, about: about,
		prefix: "⏰ **" + strings.ReplaceAll(x.Title, "*", "") + "**"})
}

func (b *Bot) sendReminder(ctx context.Context, x store.Schedule, u *store.User, rule sched.Rule, late bool) error {
	l := b.lang(u.ID)
	var sb strings.Builder
	if x.ChatType == "private" {
		sb.WriteString(l.t("⏰ <b>یادآوری</b>"))
	} else {
		fmt.Fprint(&sb, l.f(`⏰ <a href="tg://user?id=%d">%s</a>، یادآوری`, u.ID, esc(u.DisplayName())))
	}
	if late {
		sb.WriteString(l.t(" <i>(با کمی تأخیر)</i>"))
	}
	sb.WriteString("\n\n" + esc(x.Body))
	if !rule.Once() {
		sb.WriteString("\n\n<i>🔁 " + esc(describeRule(l, rule)) + l.t(" · لغو با /reminders</i>"))
	}
	if _, err := b.api.SendHTML(ctx, x.ChatID, b.bidi(x.ChatID, sb.String()), telegram.SendOptions{ThreadID: x.ThreadID}); err != nil {
		if telegram.IsUnreachable(err) {
			_, _ = b.st.CancelSchedule(ctx, x.ID, 0) // blocked, or removed from the group
		}
		return err
	}
	return nil
}

// schedulePausedNotice tells the owner that a schedule stopped after failing:
// paused (a recurring one, resumed from /reminders) or given up (a one-time one).
func (b *Bot) schedulePausedNotice(ctx context.Context, x store.Schedule, why string, paused bool) {
	l := b.lang(x.UserID)
	format := l.t("⚠️ کار زمان‌بندی‌شده <b>#%d</b> («%s») چند بار پشت هم انجام نشد و متوقفش کردم.\nآخرین خطا: <i>%s</i>\n\nبا /reminders می‌تونی دوباره روشنش کنی.")
	if !paused {
		format = l.t("⚠️ کار زمان‌بندی‌شده <b>#%d</b> («%s») با چند بار تلاش هم انجام نشد.\nآخرین خطا: <i>%s</i>")
	}
	text := fmt.Sprintf(format, x.ID, esc(x.Title), esc(textutil.TruncateRunes(why, 200, "…")))
	_, _ = b.api.SendHTML(ctx, x.UserID, b.bidi(x.UserID, text), telegram.SendOptions{}) // the owner's own chat: a group need not hear it
}

// ---------------------------------------------------------------- /reminders

func (b *Bot) sendSchedules(ctx context.Context, m *telegram.Message, userID int64) {
	v := b.viewSchedules(ctx, userID)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func (b *Bot) viewSchedules(ctx context.Context, userID int64) panelView {
	l := b.lang(userID)
	list, err := b.st.UserSchedules(ctx, userID)
	if err != nil {
		return panelView{html: l.t(textError)}
	}
	if len(list) == 0 {
		return panelView{html: l.t(textNoSchedules)}
	}
	var (
		sb   strings.Builder
		rows [][]telegram.InlineKeyboardButton
	)
	sb.WriteString(l.t("⏰ <b>یادآورها و کارهای زمان‌بندی‌شده</b>\n"))
	for _, x := range list {
		rule, _ := sched.Parse(x.Rule)
		icon := "🔔"
		if x.Kind == store.ScheduleTask {
			icon = "🤖"
		}
		when := x.NextRun.In(b.loc).Format("2006-01-02 15:04")
		if x.Status == store.SchedulePaused {
			fmt.Fprintf(&sb, "\n⏸ <b>#%d</b> %s\n   %s", x.ID, esc(x.Title), l.f("متوقف شده بعد از %s خطا: <i>%s</i>", l.num(x.Failures),
				esc(textutil.TruncateRunes(x.LastError, 120, "…"))))
			rows = append(rows, row(b.button(l.f("▶️ روشن کن #%d", x.ID), fmt.Sprintf("sc:r:%d", x.ID), telegram.StyleSuccess),
				b.button(l.f("❌ لغو #%d", x.ID), fmt.Sprintf("sc:x:%d", x.ID), telegram.StyleDanger)))
			continue
		}
		fmt.Fprintf(&sb, "\n%s <b>#%d</b> %s\n   ⏱ %s · %s", icon, x.ID, esc(x.Title), when, esc(describeRule(l, rule)))
		rows = append(rows, row(b.button(l.f("❌ لغو #%d", x.ID), fmt.Sprintf("sc:x:%d", x.ID), telegram.StyleDanger)))
	}
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) handleScheduleCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 || (parts[1] != "x" && parts[1] != "r") {
		answer("", false)
		return
	}
	id, _ := strconv.ParseInt(parts[2], 10, 64)
	owner := q.From.ID
	if b.isAdmin(q.From.ID) {
		owner = 0
	}
	if parts[1] == "r" {
		if ok, _ := b.st.ResumeSchedule(ctx, id, owner, time.Now().Add(time.Minute)); !ok {
			answer(l.t("این مورد دیگه متوقف نیست یا مال تو نیست"), true)
		} else {
			answer(l.f("#%d دوباره روشن شد", id), false)
		}
	} else if ok, _ := b.st.CancelSchedule(ctx, id, owner); !ok {
		answer(l.t("این مورد دیگه فعال نیست یا مال تو نیست"), true)
	} else {
		answer(l.f("#%d لغو شد", id), false)
	}
	v := b.viewSchedules(ctx, q.From.ID)
	_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, v.html, v.kb)
}

// stringList reads a JSON argument that is a list of strings (or one string).
func stringList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = append(out, x)
	}
	return out
}
