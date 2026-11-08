package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	settingLearnReview     = "learn_review_interval"
	settingLearnJobs       = "learn_jobs_since_review"
	learnReviewDefault     = 25
	learnNudgeEvery        = 15
	lessonDuplicateAt      = 0.6
	lessonUnusedAfter      = 30 * 24 * time.Hour
	lessonLogPage          = 8
	learnReviewMaxFlagged  = 10
	learnReviewMaxMerged   = 20
	learnReviewStepTimeout = 30 * time.Second
)

type skillShot struct {
	Description string `json:"description"`
	Body        string `json:"body"`
	Category    string `json:"category"`
}

func (b *Bot) shotOf(name string) string {
	sk := b.app.Agent.Skills
	if sk == nil {
		return ""
	}
	s, ok := sk.Get(name)
	if !ok {
		return ""
	}
	raw, _ := json.Marshal(skillShot{Description: s.Description, Body: s.Body, Category: s.Category})
	return string(raw)
}

func (b *Bot) restoreShot(name, shot string) error {
	sk := b.app.Agent.Skills
	if sk == nil {
		return nil
	}
	if shot == "" {
		if _, ok := sk.Get(name); ok {
			return sk.Delete(name)
		}
		return nil
	}
	var s skillShot
	if err := json.Unmarshal([]byte(shot), &s); err != nil {
		return err
	}
	return sk.Save(name, s.Description, s.Body, s.Category)
}

func (b *Bot) lessonPrivate() []string {
	var out []string
	for id := range b.owner {
		out = append(out, strconv.FormatInt(id, 10))
	}
	out = append(out, config.Home())
	if h, err := os.UserHomeDir(); err == nil && h != "/" {
		out = append(out, h)
	}
	return out
}

func (b *Bot) lessonLeaks(x *store.Lesson) string {
	return agent.SecretIn(x.Title+"\n"+x.Rule+"\n"+x.Tools, b.lessonPrivate()...)
}

func (b *Bot) undoLesson(ctx context.Context, id int64) (*store.Lesson, error) {
	x, err := b.st.Lesson(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := b.st.DecideLesson(ctx, id, store.LessonRejected); err != nil {
		return nil, err
	}
	name := lessonSkill + x.Category
	before := b.shotOf(name)
	restored := false
	if last, err := b.st.LastAdoption(ctx, id); err == nil && last.After != "" && last.After == before && x.Category != categoryAlways {
		if err := b.restoreShot(name, last.Before); err != nil {
			return nil, err
		}
		restored = true
	}
	if !restored {
		if err := b.rebuildLessons(ctx, x.Category); err != nil {
			return nil, err
		}
	}
	note := "rebuilt without it"
	if restored {
		note = "the skill as it was before"
	}
	_, _ = b.st.AddLessonLog(ctx, store.LessonLog{LessonID: id, Category: x.Category, Action: store.LessonReverted, Title: x.Title,
		Before: before, After: b.shotOf(name), Note: note})
	log.Printf("🧠 a lesson was taken back: %s (%s; %s)", x.Title, x.Key, note)
	return x, nil
}

func (b *Bot) learnReviewEvery() int {
	return max(b.st.SettingInt(settingLearnReview, learnReviewDefault), 0)
}

func (b *Bot) noteJobForReview() {
	every := b.learnReviewEvery()
	if every == 0 || !b.learningOn() {
		return
	}
	ctx, cancel := b.ctx(5 * time.Second)
	defer cancel()
	n := b.st.SettingInt(settingLearnJobs, 0) + 1
	if n < every {
		_ = b.st.SetSetting(ctx, settingLearnJobs, strconv.Itoa(n))
		return
	}
	_ = b.st.SetSetting(ctx, settingLearnJobs, "0")
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		rctx, rcancel := b.ctx(time.Minute)
		defer rcancel()
		if _, err := b.reviewLessons(rctx); err != nil {
			log.Printf("learning review: %v", err)
		}
	}()
}

func (b *Bot) reviewLessons(ctx context.Context) (store.LessonLog, error) {
	list, err := b.st.Lessons(ctx, store.LessonApproved, "", 500)
	if err != nil {
		return store.LessonLog{}, err
	}
	byField := map[string][]store.Lesson{}
	for _, x := range list {
		byField[x.Category] = append(byField[x.Category], x)
	}
	var merged []string
	changed := map[string]bool{}
	for field, xs := range byField {
		gone := map[int64]bool{}
		for i := range xs {
			if gone[xs[i].ID] {
				continue
			}
			for j := i + 1; j < len(xs) && len(merged) < learnReviewMaxMerged; j++ {
				if gone[xs[j].ID] || lessonOverlap(xs[i], xs[j]) < lessonDuplicateAt {
					continue
				}
				if err := b.st.MergeLesson(ctx, xs[i].ID, xs[j].ID); err != nil {
					return store.LessonLog{}, err
				}
				gone[xs[j].ID], changed[field] = true, true
				merged = append(merged, fmt.Sprintf("«%s» → «%s»", xs[j].Title, xs[i].Title))
			}
		}
	}
	fields := make([]string, 0, len(changed))
	for f := range changed {
		fields = append(fields, f)
	}
	sort.Strings(fields)
	for _, f := range fields {
		if err := b.rebuildLessons(ctx, f); err != nil {
			return store.LessonLog{}, err
		}
	}
	var unused []string
	if sk := b.app.Agent.Skills; sk != nil {
		cut := b.st.Now().Add(-lessonUnusedAfter)
		for _, s := range sk.Snapshot() {
			if s.Enabled && s.Uses == 0 && !s.UpdatedAt.IsZero() && s.UpdatedAt.Before(cut) && len(unused) < learnReviewMaxFlagged {
				unused = append(unused, s.Name)
			}
		}
		sort.Strings(unused)
	}
	note := fmt.Sprintf("merged %d, unused %d", len(merged), len(unused))
	if len(merged) > 0 {
		note += "\nmerged: " + strings.Join(merged, "; ")
	}
	if len(unused) > 0 {
		note += "\nunused: " + strings.Join(unused, ", ")
	}
	entry := store.LessonLog{Action: store.LessonReviewed, Title: "review", Note: note}
	entry.ID, err = b.st.AddLessonLog(ctx, entry)
	log.Printf("🧠 learning review: %s", strings.ReplaceAll(note, "\n", " · "))
	return entry, err
}

func lessonOverlap(a, b store.Lesson) float64 {
	words := func(s string) map[string]bool {
		out := map[string]bool{}
		for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
		}) {
			if len([]rune(w)) > 2 {
				out[w] = true
			}
		}
		return out
	}
	x, y := words(a.Title+" "+a.Rule), words(b.Title+" "+b.Rule)
	if len(x) == 0 || len(y) == 0 {
		return 0
	}
	both := 0
	for w := range x {
		if y[w] {
			both++
		}
	}
	return float64(both) / float64(len(x)+len(y)-both)
}

func (b *Bot) learnCommand(ctx context.Context, m *telegram.Message, l lang, args string) {
	f := strings.Fields(args)
	if len(f) == 0 {
		b.showNew(ctx, m.Chat.ID, b.viewLearn(ctx, l))
		return
	}
	if f[0] != "undo" || len(f) != 2 {
		b.reply(m, l.t("<code>/learn</code> پنل یادگیری رو باز می‌کنه؛ <code>/learn undo ID</code> یه درس رو پس می‌گیره (ID رو توی 🕘 آخرین درس‌ها می‌بینی)."))
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(f[1], "#"), 10, 64)
	if err != nil {
		b.reply(m, l.t("ID باید عدد باشه."))
		return
	}
	x, err := b.st.Lesson(ctx, id)
	switch {
	case err != nil:
		b.reply(m, l.t("این درس دیگه نیست"))
		return
	case x.Status != store.LessonApproved:
		b.reply(m, l.t("این درس الان توی مهارت‌ها نیست."))
		return
	}
	if _, err := b.undoLesson(ctx, id); err != nil {
		b.reply(m, l.t(textError))
		return
	}
	b.reply(m, l.f("↩️ پس گرفته شد: <b>%s</b>\nمهارتش به نسخه قبل برگشت و این درس دیگه پیشنهاد نمی‌شه.", esc(x.Title)))
}
