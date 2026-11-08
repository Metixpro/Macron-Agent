package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Teaching by hand. An admin tells the bot a lesson in their own words: in
// the panel (🧠 Learning → ✍️ Teach), with /teach, or with /teach as a
// reply to an answer of the bot's that was wrong. A short model call writes
// it as one clear rule, faithful to what was said, and decides where it
// holds: always (in every answer: tone, facts about the bot or the
// service, names, formats, what to say or never say), or for a kind of job
// (then it joins that kind's lessons skill). The lesson is in use at once;
// the admin sees how it was understood and can move, edit or remove it.
// The rules that hold always go into every prompt, under the ground rules,
// which they can never change.

const (
	categoryAlways = "always"
	taughtMark     = "/taught-" // in the key of a lesson an admin taught
	maxAlwaysRules = 40
	maxAlwaysRunes = 4000
	maxTaughtRunes = 2000
)

// teachCache holds the always-rules for the prompt, read again after a change.
type teachCache struct {
	mu   sync.Mutex
	text string
	ok   bool
}

// teachings is what goes into every prompt: the always-rules, one per line.
func (b *Bot) teachings() string {
	t := &b.taught
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.ok {
		ctx, cancel := b.ctx(5 * time.Second)
		defer cancel()
		list, err := b.st.Lessons(ctx, store.LessonApproved, categoryAlways, maxAlwaysRules)
		if err != nil {
			return "" // asked again on the next prompt
		}
		t.text, t.ok = alwaysText(list), true
	}
	return t.text
}

func (b *Bot) forgetTeachings() {
	b.taught.mu.Lock()
	b.taught.ok = false
	b.taught.mu.Unlock()
}

func alwaysText(list []store.Lesson) string {
	var sb strings.Builder
	for _, x := range list {
		line := "- " + textutil.OneLine(x.Title) + ": " + textutil.OneLine(x.Rule) + "\n"
		if utf8.RuneCountInString(sb.String())+utf8.RuneCountInString(line) > maxAlwaysRunes {
			break
		}
		sb.WriteString(line)
	}
	return sb.String()
}

// alwaysRoom: another always-rule fits in the prompt.
func (b *Bot) alwaysRoom(ctx context.Context, extra string) bool {
	list, err := b.st.Lessons(ctx, store.LessonApproved, categoryAlways, maxAlwaysRules+1)
	if err != nil {
		return true
	}
	size := utf8.RuneCountInString(alwaysText(list)) + utf8.RuneCountInString(extra) + 10
	return len(list) < maxAlwaysRules && size <= maxAlwaysRunes
}

const teachPrompt = `An admin of the assistant is teaching it a lesson in their own words. Write it as ONE clear rule for the
assistant, faithful to what the admin meant: add nothing, drop nothing, do not soften it, and keep the admin's language
(Persian stays Persian).
Decide where it holds:
- "always": how to behave or answer in every conversation (tone, length, language, facts about itself, its owner or its
  service, names, formats, things to always or never say);
- or the kind of job it is about: media (downloads, audio, video, pictures), web (search, sites), dev (code),
  docs (documents, PDFs, spreadsheets), files (workspace files), telegram (chats, groups), general (any other job).
%s
Reply with JSON only: {"category": "...", "title": "at most 10 words", "rule": "one to three sentences"}

The admin's lesson:
<lesson>
%s
</lesson>`

// taughtLesson is the lesson an admin's words make: the model's reading of
// them, or the words themselves when there is no model.
func (b *Bot) taughtLesson(ctx context.Context, text, about string) (category, title, rule string) {
	if about != "" {
		about = "\nThe admin wrote it as a correction of this answer of yours:\n<answer>\n" + textutil.HeadTail(about, 2000) + "\n</answer>\n"
	}
	if b.prov.Configured() {
		mctx, cancel := context.WithTimeout(ctx, time.Minute)
		resp, err := b.prov.Complete(mctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: fmt.Sprintf(teachPrompt, about, text)}}, MaxTokens: 700})
		cancel()
		if err == nil {
			if c, t, r, ok := parseTaught(resp.Message.Content); ok {
				return c, t, r
			}
		} else {
			log.Printf("teach: %v", err)
		}
	}
	// The admin's own words, as they are.
	first, _, _ := strings.Cut(strings.TrimSpace(text), "\n")
	category = categoryAlways
	if utf8.RuneCountInString(text) > 400 {
		category = "general"
	}
	return category, textutil.TruncateRunes(textutil.OneLine(first), 60, "…"), strings.TrimSpace(text)
}

func parseTaught(reply string) (category, title, rule string, ok bool) {
	raw := strings.TrimSpace(reply)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		raw = raw[i : j+1]
	}
	var v struct {
		Category string `json:"category"`
		Title    string `json:"title"`
		Rule     string `json:"rule"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return "", "", "", false
	}
	title, rule = textutil.OneLine(v.Title), strings.TrimSpace(v.Rule)
	if _, known := lessonCategories[v.Category]; !known && v.Category != categoryAlways {
		v.Category = categoryAlways
	}
	if title == "" || rule == "" || utf8.RuneCountInString(title) > 120 || utf8.RuneCountInString(rule) > 1500 {
		return "", "", "", false
	}
	return v.Category, title, rule, true
}

// teach saves an admin's lesson, in use at once. note tells the admin
// something about where it went.
func (b *Bot) teach(ctx context.Context, l lang, text, about string) (id int64, note string, err error) {
	category, title, rule := b.taughtLesson(ctx, text, about)
	if category == categoryAlways && !b.alwaysRoom(ctx, title+rule) {
		category = "general"
		note = l.f("⚠️ جای قانون‌های همیشگی پره (حداکثر %s قانون یا %s کاراکتر)، پس این درس توی اسکیل «کارهای کلی» رفت. می‌تونی چندتا از قانون‌های همیشگی رو بیرون ببری.",
			l.num(maxAlwaysRules), l.num(maxAlwaysRunes))
	}
	key := category + taughtMark + strconv.FormatInt(time.Now().UnixNano(), 36)
	id, _, _, err = b.st.AddLesson(ctx, store.Lesson{Key: key, Category: category, Title: title, Rule: rule})
	if err != nil {
		return 0, "", err
	}
	if err = b.st.DecideLesson(ctx, id, store.LessonApproved); err != nil {
		return 0, "", err
	}
	return id, note, b.rebuildLessons(ctx, category)
}

// startTeaching runs teach in the background and shows the admin the lesson.
func (b *Bot) startTeaching(adminID int64, l lang, text, about string) {
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := b.ctx(2 * time.Minute)
		defer cancel()
		id, note, err := b.teach(ctx, l, text, about)
		if err != nil {
			b.sendHTML(ctx, adminID, l.t("❌ درس ذخیره نشد: ")+esc(err.Error()), telegram.SendOptions{})
			return
		}
		v := b.viewLesson(ctx, l, id)
		v.html = l.t("✅ <b>یاد گرفتم.</b> این‌طوری فهمیدمش و از همین الان به کار میاد:\n\n") + v.html
		if note != "" {
			v.html += "\n\n" + note
		}
		b.showNew(ctx, adminID, v)
	}()
}

// textTeachHelp is one literal, so the English catalog finds it.
const textTeachHelp = "✍️ <b>یاد دادن به ربات</b>\n\nدرس رو به زبون خودت بنویس، مثلاً:\n• «وقتی کسی قیمت طلا خواست، قیمت مثقال رو هم بگو»\n• «اسم سازنده‌ات رو همیشه حاج متی بگو»\n• «برای دانلود از اینستاگرام اول کوکی رو امتحان کن»\n\nخودم تشخیص می‌دم همیشه برقرار باشه یا فقط برای یه نوع کار، و بعدش می‌تونی جاش رو عوض کنی، ویرایشش کنی یا برداریش. اگه روی یه جواب اشتباه ربات ریپلای کنی و بنویسی <code>/teach درستش اینه…</code>، اون جواب هم کنار درس دیده می‌شه."

// teachCommand is /teach: the lesson after it, maybe as a reply to an answer of the bot's.
func (b *Bot) teachCommand(ctx context.Context, m *telegram.Message, l lang, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		b.reply(m, l.t(textTeachHelp))
		return
	}
	if utf8.RuneCountInString(text) > maxTaughtRunes {
		b.reply(m, l.f("درس خیلی بلنده؛ حداکثر %s کاراکتر.", l.num(maxTaughtRunes)))
		return
	}
	about := ""
	if r := m.RepliedInChat(); r != nil && r.From != nil && r.From.ID == b.me.ID {
		about = strings.TrimSpace(telegram.UnmarkRTL(r.Body()))
	}
	if m.Chat.Private() {
		b.reply(m, l.t("✍️ دارم یاد می‌گیرم…"))
	} else {
		_ = b.api.SetReaction(ctx, m.Chat.ID, m.MessageID, "✍", false)
	}
	b.startTeaching(m.From.ID, l, text, about) // the lesson is shown in private, never in a group
}

// teachInput takes the lesson typed after ✍️ Teach in the panel.
func (b *Bot) teachInput(ctx context.Context, l lang, m *telegram.Message) {
	text := strings.TrimSpace(m.Text)
	if text == "" || utf8.RuneCountInString(text) > maxTaughtRunes {
		b.reply(m, l.f("متن درس رو بفرست (تا %s کاراکتر).", l.num(maxTaughtRunes)))
		return
	}
	b.reply(m, l.t("✍️ دارم یاد می‌گیرم…"))
	b.startTeaching(m.From.ID, l, text, "")
}

// viewLessonPlace offers the places a lesson can hold in.
func (b *Bot) viewLessonPlace(ctx context.Context, l lang, id int64) panelView {
	x, err := b.st.Lesson(ctx, id)
	if err != nil {
		return b.viewLearn(ctx, l)
	}
	act := func(c string) string { return fmt.Sprintf("a:learn:mv:%d:%s", id, c) }
	label := func(c, text string) string {
		if c == x.Category {
			return "✅ " + text
		}
		return text
	}
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(label(categoryAlways, l.t("🌐 همیشه (توی همه جواب‌ها)")), act(categoryAlways), telegram.StylePrimary)),
		row(b.button(label("media", l.t("🎬 دانلود و رسانه")), act("media"), ""), b.button(label("web", l.t("🔎 وب و جستجو")), act("web"), "")),
		row(b.button(label("dev", l.t("💻 کد و برنامه")), act("dev"), ""), b.button(label("docs", l.t("📄 سند و PDF")), act("docs"), "")),
		row(b.button(label("files", l.t("📁 فایل‌ها")), act("files"), ""), b.button(label("telegram", l.t("💬 تلگرام و گروه")), act("telegram"), "")),
		row(b.button(label("general", l.t("🧩 کارهای کلی")), act("general"), "")),
		row(b.back(l, fmt.Sprintf("a:learn:ln:%d", id))),
	}
	return panelView{html: l.f("🗂 <b>%s</b>\n\nاین درس کجا به کار بیاد؟ «همیشه» یعنی توی همه جواب‌ها رعایت می‌شه (برای لحن، اطلاعات ثابت، اسم‌ها و قالب‌ها)؛ بقیه یعنی فقط وقتی ربات سر اون نوع کاره سراغش می‌ره.",
		esc(x.Title)), kb: keyboard(rows...)}
}

// moveLesson puts a lesson in another place and rebuilds both.
func (b *Bot) moveLesson(ctx context.Context, l lang, id int64, category string) (string, error) {
	x, err := b.st.Lesson(ctx, id)
	if err != nil {
		return "", err
	}
	if _, ok := lessonCategories[category]; !ok && category != categoryAlways {
		return "", fmt.Errorf("unknown place %q", category)
	}
	if category == x.Category {
		return "", nil
	}
	if category == categoryAlways && x.Status == store.LessonApproved && !b.alwaysRoom(ctx, x.Title+x.Rule) {
		return l.f("⚠️ جای قانون‌های همیشگی پره (حداکثر %s قانون یا %s کاراکتر).", l.num(maxAlwaysRules), l.num(maxAlwaysRunes)), nil
	}
	if err := b.st.MoveLesson(ctx, id, category); err != nil {
		return "", err
	}
	if x.Status == store.LessonApproved {
		_ = b.rebuildLessons(ctx, x.Category)
		if err := b.rebuildLessons(ctx, category); err != nil {
			return "", err
		}
	}
	return "", nil
}
