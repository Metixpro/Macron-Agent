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
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	toolBackground = "background_task"
	maxBgPerUser   = 2
	maxBgPerAdmin  = 4
	bgNoticeFor    = 6 * time.Hour
	bgStepsFactor  = 3
	bgMaxSteps     = 400
	bgMinTimeout   = 45 * time.Minute
	bgMaxTimeout   = 3 * time.Hour
	bgListed       = 8
)

type bgRun struct {
	task   store.BgTask
	status string
	note   string
}

func (r *bgRun) end(status, note string) {
	if r != nil && r.status == "" {
		r.status, r.note = status, note
	}
}

func bgBudget(steps int, timeout time.Duration) (int, time.Duration) {
	return min(max(steps*bgStepsFactor, 120), bgMaxSteps), min(max(timeout*bgStepsFactor, bgMinTimeout), bgMaxTimeout)
}

func (b *Bot) backgroundTool(u *store.User, m *telegram.Message) agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolBackground,
			Description: "Run a long job (an app build, a long render, a big download or conversion: minutes, not seconds) on its own: " +
				"the user keeps chatting and its result comes as a separate message. Give a short title in the user's language and the " +
				"whole goal (workspace file names, what to make and send): it does not see this request's steps. Then say in one line " +
				"that it started; don't also do the job yourself.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"title": map[string]any{"type": "string"},
				"goal":  map[string]any{"type": "string"},
			}, "required": []string{"title", "goal"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			title, _ := args["title"].(string)
			goal, _ := args["goal"].(string)
			return agent.ToolOutput{Text: b.startBg(ctx, u, m, title, goal)}, nil
		},
	}
}

func (b *Bot) startBg(ctx context.Context, u *store.User, m *telegram.Message, title, goal string) string {
	title = textutil.TruncateRunes(textutil.OneLine(strings.TrimSpace(title)), 60, "…")
	goal = strings.TrimSpace(goal)
	if title == "" || len([]rune(goal)) < 10 {
		return "error: give a short title and the whole goal of the job"
	}
	limit := maxBgPerUser
	if b.isAdmin(u.ID) {
		limit = maxBgPerAdmin
	}
	if n, err := b.st.ActiveBgTasks(ctx, u.ID); err != nil {
		return "error: could not check the running tasks: " + err.Error()
	} else if n >= limit {
		return fmt.Sprintf("refused: %d background tasks are already running or waiting (the most at once); do this one yourself now, "+
			"or tell the user to wait for one to finish (/tasks lists them)", n)
	}
	task := store.BgTask{UserID: u.ID, ChatID: m.Chat.ID, ChatType: m.Chat.Type, MsgID: m.MessageID, Title: title, Goal: goal}
	if m.IsTopicMessage {
		task.ThreadID = m.MessageThreadID
	}
	id, err := b.st.AddBgTask(ctx, task)
	if err != nil {
		return "error: the task was not saved: " + err.Error()
	}
	task.ID = id
	b.launchBg(task)
	return fmt.Sprintf("Started background task #%d «%s». It runs on its own and its result comes as a separate message. Tell the "+
		"user in one short line that it started and they'll get the result when it's done (they can keep chatting meanwhile); "+
		"don't do this job yourself.", id, title)
}

func (b *Bot) launchBg(task store.BgTask) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.runBg(task)
	}()
}

func (b *Bot) runBg(task store.BgTask) {
	ctx, cancel := b.ctx(time.Minute)
	u, err := b.st.User(ctx, task.UserID)
	if err == nil {
		err = b.st.StartBgTask(ctx, task.ID)
	}
	cancel()
	if err != nil {
		b.finishBg(task.ID, store.BgFailed, "could not start: "+err.Error())
		return
	}
	l := b.lang(u.ID)
	m := &telegram.Message{MessageID: task.MsgID, From: &telegram.User{ID: u.ID, FirstName: u.FirstName, LastName: u.LastName,
		Username: u.Username}, Chat: telegram.Chat{ID: task.ChatID, Type: task.ChatType}, Date: time.Now().Unix(),
		IsTopicMessage: task.ThreadID != 0, MessageThreadID: task.ThreadID}
	run := &bgRun{task: task}
	about := fmt.Sprintf("This is background task #%d «%s», which you started for the user earlier. It runs on its own: the user "+
		"is not waiting on this chat and may be talking about other things meanwhile. Work through the whole job on your own (no "+
		"questions), check the result, send the final files the user wants, and end with a short report of what was done.", task.ID, task.Title)
	err = b.handlePrompt(u, m, task.Goal, promptOptions{bg: run, free: true, noFlood: true, noReact: true, about: about,
		prefix: l.f("✅ **کار پس‌زمینه تموم شد:** %s", strings.ReplaceAll(task.Title, "*", ""))})
	switch {
	case b.jobCtx.Err() != nil && run.status != store.BgDone:
		return
	case run.status != "":
	case err == nil:
		run.status = store.BgDone
	case errors.Is(err, errNotDone):
		run.status, run.note = store.BgFailed, "it could not start (no free slot, the quota or the model)"
	default:
		run.status, run.note = store.BgFailed, err.Error()
	}
	b.finishBg(task.ID, run.status, run.note)
}

func (b *Bot) finishBg(id int64, status, note string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := b.st.FinishBgTask(ctx, id, status, note); err != nil {
		log.Printf("background task %d: %v", id, err)
	}
}

func (b *Bot) stopBg(uid, id int64) bool {
	v, ok := b.bgRuns.Load(id)
	if !ok {
		return false
	}
	r := v.(*activeRun)
	if r.userID != uid {
		return false
	}
	r.stop()
	return true
}

func bgStatusIcon(status string) string {
	switch status {
	case store.BgQueued:
		return "⏳"
	case store.BgRunning:
		return "🔄"
	case store.BgDone:
		return "✅"
	case store.BgStopped:
		return "⏹"
	}
	return "⚠️"
}

func (b *Bot) sendTasks(ctx context.Context, m *telegram.Message, uid int64) {
	l := b.langOf(m.From)
	tasks, err := b.st.UserBgTasks(ctx, uid, bgListed)
	if err != nil {
		b.reply(m, l.t(textError))
		return
	}
	if len(tasks) == 0 {
		b.sendHTML(ctx, m.Chat.ID, l.t("هنوز کار پس‌زمینه‌ای نداری. کارهای طولانی (بیلد، رندر، دانلود بزرگ) خودم می‌فرستم پس‌زمینه و آخرش نتیجه رو می‌فرستم."),
			telegram.SendOptions{ReplyTo: m.MessageID})
		return
	}
	var sb strings.Builder
	sb.WriteString(l.t("🔄 <b>کارهای پس‌زمینه</b>") + "\n")
	var rows [][]telegram.InlineKeyboardButton
	for _, t := range tasks {
		fmt.Fprintf(&sb, "\n%s <b>#%d</b> %s", bgStatusIcon(t.Status), t.ID, esc(t.Title))
		since := t.CreatedAt
		if t.StartedAt != nil {
			since = *t.StartedAt
		}
		switch {
		case t.Active():
			sb.WriteString(" · " + l.f("%s دقیقه", l.num(int(time.Since(since).Minutes()))))
			rows = append(rows, row(b.button(l.f("⏹ توقف #%d", t.ID), fmt.Sprintf("bg:stop:%d:%d", uid, t.ID), telegram.StyleDanger)))
		case t.Status == store.BgFailed || t.Status == store.BgInterrupted || t.Status == store.BgStopped:
			rows = append(rows, row(b.button(l.f("🔁 دوباره #%d", t.ID), fmt.Sprintf("bg:retry:%d:%d", uid, t.ID), telegram.StylePrimary)))
		}
	}
	opts := telegram.SendOptions{ReplyTo: m.MessageID}
	if len(rows) > 0 {
		opts.Markup = keyboard(rows...)
	}
	b.sendHTML(ctx, m.Chat.ID, sb.String(), opts)
}

func (b *Bot) handleBgCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) != 4 {
		answer("", false)
		return
	}
	uid, err1 := strconv.ParseInt(parts[2], 10, 64)
	id, err2 := strconv.ParseInt(parts[3], 10, 64)
	if err1 != nil || err2 != nil {
		answer("", false)
		return
	}
	if q.From.ID != uid && (parts[1] != "stop" || !b.isAdmin(q.From.ID)) {
		answer(l.t("این دکمه مال کسیه که درخواست رو داده."), true)
		return
	}
	task, err := b.st.BgTaskByID(ctx, id)
	if err != nil || task == nil || task.UserID != uid {
		answer(l.t("این کار پیدا نشد."), true)
		return
	}
	switch parts[1] {
	case "stop":
		if !b.stopBg(uid, id) {
			answer(l.t("این کار دیگه تموم شده."), false)
			return
		}
		answer(l.t("⏹ متوقفش کردم"), false)
	case "retry":
		if task.Active() {
			answer(l.t("این کار هنوز در جریانه."), false)
			return
		}
		limit := maxBgPerUser
		if b.isAdmin(uid) {
			limit = maxBgPerAdmin
		}
		if n, _ := b.st.ActiveBgTasks(ctx, uid); n >= limit {
			answer(l.f("الان %s کار پس‌زمینه در جریانه؛ صبر کن یکیش تموم بشه.", l.num(n)), true)
			return
		}
		again := *task
		again.ID, again.Status, again.Note = 0, "", ""
		newID, err := b.st.AddBgTask(ctx, again)
		if err != nil {
			answer(l.t(textError), true)
			return
		}
		again.ID = newID
		_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
		answer(l.f("🔁 دوباره شروع شد (#%d)", newID), false)
		b.launchBg(again)
	default:
		answer("", false)
	}
}

func (b *Bot) orphanedBgTasks(ctx context.Context) []store.BgTask {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	tasks, err := b.st.OrphanedBgTasks(ctx)
	if err != nil {
		log.Printf("background tasks after a restart: %v", err)
	}
	return tasks
}

func (b *Bot) tellBgRestarted(ctx context.Context, tasks []store.BgTask) {
	for _, t := range tasks {
		started := t.CreatedAt
		if t.StartedAt != nil {
			started = *t.StartedAt
		}
		if time.Since(started) > bgNoticeFor || ctx.Err() != nil {
			continue
		}
		l := b.lang(t.UserID)
		kb := keyboard(row(b.button(l.t("🔁 دوباره شروعش کن"), fmt.Sprintf("bg:retry:%d:%d", t.UserID, t.ID), telegram.StylePrimary)))
		sctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		b.sendHTML(sctx, t.ChatID, l.f(textBgRestarted, esc(t.Title)), telegram.SendOptions{ReplyTo: t.MsgID, Markup: kb, ThreadID: t.ThreadID})
		cancel()
		time.Sleep(jobNoticeEvery)
	}
	if len(tasks) > 0 {
		log.Printf("🔄 %d background tasks were cut short by the restart", len(tasks))
	}
}

func (r *bgRun) taskID() int64 {
	if r == nil {
		return 0
	}
	return r.task.ID
}

func (r *bgRun) stopData() string  { return fmt.Sprintf("bg:stop:%d:%d", r.task.UserID, r.task.ID) }
func (r *bgRun) retryData() string { return fmt.Sprintf("bg:retry:%d:%d", r.task.UserID, r.task.ID) }

func (r *bgRun) memoryText() string {
	return fmt.Sprintf("[background task #%d «%s», which I started for the user and ran on my own; its goal:]\n%s", r.task.ID, r.task.Title, r.task.Goal)
}

func (r *bgRun) failed(stopped bool, err error) {
	switch {
	case r == nil:
	case stopped:
		r.end(store.BgStopped, "stopped by the user")
	case errors.Is(err, context.DeadlineExceeded):
		r.end(store.BgFailed, "it ran out of time")
	default:
		r.end(store.BgFailed, textutil.TruncateRunes(textutil.OneLine(err.Error()), 300, "…"))
	}
}

func (r *bgRun) ended(stopped bool, res *agent.Result) {
	switch {
	case r == nil:
	case stopped:
		r.end(store.BgStopped, "stopped by the user")
	case res.Partial:
		r.end(store.BgFailed, "it ended before finishing ("+res.StopReason+")")
	default:
		r.end(store.BgDone, "")
	}
}
