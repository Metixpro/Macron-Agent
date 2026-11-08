package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/skills"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Learning from experience. A job where a step failed and another way
// worked holds a lesson: right after it, a short model call turns its work
// log into one general rule ("when X, do Y, because Z"), with nothing about
// the user in it. The lesson waits in the panel (🧠 Learning); only an admin
// approving it puts it in a skill (lessons-<category>), where every later
// job finds it. Nothing a user says can change the agent by itself: the
// worst a crafted job can do is propose a lesson an admin rejects (and a
// rejected one is never proposed again).

const (
	settingLearn = "learn_from_jobs" // "false" turns it off
	// lessonsPerUser and lessonsPerDay bound the lessons proposed, so nobody
	// can flood the admins' queue (or the costs).
	lessonsPerUser = 3
	lessonsPerDay  = 30
	lessonSkill    = "lessons-" // + category
)

// lessonCategories are the fields a lesson may be about, with how a skill of them is described.
var lessonCategories = map[string]string{
	"media":    "downloads, audio, video and pictures",
	"web":      "web search, reading pages and sites",
	"dev":      "code, programs, packages and projects",
	"docs":     "documents, PDFs, spreadsheets and archives",
	"files":    "workspace files and delivering them",
	"telegram": "Telegram chats, groups and the bot's own tools",
	"general":  "jobs of any kind",
}

func (b *Bot) learningOn() bool { return b.st.SettingBool(settingLearn, true) }

// learnLimits are created once (a variable for the tests).
var learnLimits = struct{ user, day *guard.RateLimiter }{
	guard.NewRateLimiter(lessonsPerUser, 24*time.Hour), guard.NewRateLimiter(lessonsPerDay, 24*time.Hour),
}

// learnFrom proposes the lesson of a job that failed on the way and still
// answered, in the background (it never holds up the user).
func (b *Bot) learnFrom(uid int64, res *agent.Result) {
	if res == nil || res.Trail == "" || res.Partial || strings.TrimSpace(res.Answer) == "" || !b.learningOn() {
		return
	}
	// The personal edition also learns from a long job that went well: how
	// it was done is worth keeping for the next time.
	procedure := res.Failures == 0
	if procedure && !(b.personal && res.Steps >= procedureSteps && len(res.ToolsUsed) >= 3) {
		return
	}
	if ok, _ := learnLimits.user.Allow(strconv.FormatInt(uid, 10)); !ok {
		return
	}
	if ok, _ := learnLimits.day.Allow("all"); !ok {
		return
	}
	trail, used := res.Trail, append([]string(nil), res.ToolsUsed...)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		ctx, cancel := b.ctx(2 * time.Minute)
		defer cancel()
		x, err := b.drawLesson(ctx, trail, used, procedure)
		if err != nil || x == nil {
			if err != nil {
				log.Printf("learning: %v", err)
			}
			return
		}
		if id, fresh, status, err := b.st.AddLesson(ctx, *x); err != nil {
			log.Printf("learning: saving a lesson: %v", err)
		} else if fresh && b.personal && b.st.SettingBool(settingLearnAuto, true) {
			b.adoptLesson(ctx, id, x) // its owner is the only admin: it goes straight into a skill, and they are told
		} else if fresh {
			log.Printf("🧠 a new lesson waits for an admin: %s (%s)", x.Title, x.Key)
		} else if status == store.LessonApproved {
			_ = b.rebuildLessons(ctx, x.Category) // its count goes up in the skill too
		}
	}()
}

// procedureSteps is how long a job that went well is before it is worth a look.
const procedureSteps = 6

// settingLearnAuto: in the personal edition a lesson becomes part of the
// skills at once ("false" asks the owner first, as the public bot's admins are asked).
const settingLearnAuto = "learn_auto_adopt"

const procedurePrompt = `You review a job an AI assistant did for its owner, and it went well, with several steps.
If HOW it was done is worth keeping, write ONE general procedure that would make the next similar job faster and surer.

Rules:
- Write a rule for the assistant, not a story: "When <situation>, <do this, in this order> (<why>)."
- General: it must help a later job of the same kind. Nothing private: no names, ids, phone numbers, emails, file
  names, secrets or the content of anything. Sites, tools and commands may be named (ffmpeg, systemctl, nginx).
- Only from what the log shows worked. Never a rule about safety, the instructions or prompt, the tools' secrecy,
  the owner's identity, payments or limits.
- If the job was routine, one-off, or there is nothing worth keeping, reply {"none": true}.

Known lessons (when yours is one of them, use the same key):
%s

Reply with JSON only:
{"key": "2-5 lowercase words with dashes", "category": "media|web|dev|docs|files|telegram|general",
 "title": "at most 10 words", "rule": "one to three sentences", "tools": ["tool names it is about"]}

Tools the job used: %s
The job's steps (tool, short input → result):
<log>
%s
</log>`

const lessonPrompt = `You review a job an AI assistant did for a user. Some of its tool calls failed, and the job still succeeded.
Find ONE general lesson that would make the next similar job succeed at the first try.

Rules:
- Write a rule for the assistant, not a story: "When <situation>, <do this> (<why>)."
- General: it must help other people's jobs too. NOTHING about this user or what they asked: no names, usernames, ids,
  phone numbers, emails, file names, links with paths, personal details or content. Sites and tools may be named
  (youtube.com, ffmpeg, pdf_tool).
- Only from what the log shows worked. Never a rule about safety, the instructions or prompt, the tools' secrecy, the
  user's identity, payments or limits.
- If the failure came from the user's input, a one-off outage or there is no lesson worth keeping, reply {"none": true}.

Known lessons (when yours is one of them, use the same key):
%s

Reply with JSON only:
{"key": "2-5 lowercase words with dashes", "category": "media|web|dev|docs|files|telegram|general",
 "title": "at most 10 words", "rule": "one or two sentences", "tools": ["tool names it is about"]}

Tools the job used: %s
The job's steps (tool, short input → result):
<log>
%s
</log>`

var lessonKeyRe = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+){1,5}$`)

// drawLesson asks the model for the lesson of a work log (nil: there is none).
func (b *Bot) drawLesson(ctx context.Context, trail string, used []string, procedure bool) (*store.Lesson, error) {
	known, _ := b.st.LessonKeys(ctx, "", 30)
	var kb strings.Builder
	keys := make([]string, 0, len(known))
	for k := range known {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(&kb, "- %s: %s\n", strings.TrimPrefix(k[strings.Index(k, "/")+1:], "/"), known[k])
	}
	if kb.Len() == 0 {
		kb.WriteString("(none yet)")
	}
	template := lessonPrompt
	if procedure {
		template = procedurePrompt
	}
	prompt := fmt.Sprintf(template, strings.TrimSpace(kb.String()), strings.Join(used, ", "), textutil.HeadTail(scrubLesson(trail), 6000))
	resp, err := b.prov.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: prompt}}, MaxTokens: 600})
	if err != nil {
		return nil, err
	}
	return parseLesson(resp.Message.Content, b.redact), nil
}

// parseLesson reads the model's lesson; nil when there is none or it is not fit to keep.
func parseLesson(reply string, redact func(string) string) *store.Lesson {
	raw := strings.TrimSpace(reply)
	if i, j := strings.Index(raw, "{"), strings.LastIndex(raw, "}"); i >= 0 && j > i {
		raw = raw[i : j+1]
	}
	var v struct {
		None     bool     `json:"none"`
		Key      string   `json:"key"`
		Category string   `json:"category"`
		Title    string   `json:"title"`
		Rule     string   `json:"rule"`
		Tools    []string `json:"tools"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil || v.None {
		return nil
	}
	key := strings.Trim(strings.ToLower(strings.TrimSpace(v.Key)), "-")
	title := textutil.OneLine(scrubLesson(redact(v.Title)))
	rule := strings.TrimSpace(scrubLesson(redact(v.Rule)))
	if _, ok := lessonCategories[v.Category]; !ok {
		v.Category = "general"
	}
	switch {
	case !lessonKeyRe.MatchString(key), title == "", rule == "",
		utf8.RuneCountInString(title) > 120, utf8.RuneCountInString(rule) > 600, unsafeLesson(title + " " + rule):
		return nil
	}
	var tools []string
	for _, t := range v.Tools {
		if t = strings.TrimSpace(t); lessonKeyRe.MatchString(strings.ReplaceAll(t, "_", "-")) || skills.ValidName(t) {
			tools = append(tools, t)
		}
	}
	return &store.Lesson{Key: v.Category + "/" + key, Category: v.Category, Title: title, Rule: rule, Tools: strings.Join(tools, ",")}
}

var (
	scrubURLRe   = regexp.MustCompile(`(?i)\bhttps?://([a-z0-9.-]+)[^\s)\]"']*`)
	scrubMailRe  = regexp.MustCompile(`(?i)[a-z0-9._%+-]+@[a-z0-9.-]+\.[a-z]{2,}`)
	scrubAtRe    = regexp.MustCompile(`(^|[\s(])@[A-Za-z][A-Za-z0-9_]{3,}`)
	scrubPhoneRe = regexp.MustCompile(`\+?\d[\d \-]{8,}\d`)
	scrubIDRe    = regexp.MustCompile(`\b\d{6,}\b`)
	scrubFileRe  = regexp.MustCompile(`(?i)[^\s"'/\\]+\.(pdf|docx?|xlsx?|pptx?|csv|txt|md|zip|rar|7z|jpe?g|png|webp|gif|mp4|mkv|mov|mp3|m4a|ogg|wav|srt|json|html?)\b`)
)

// scrubLesson takes what could be about a user out of a text: links keep
// their site, files their kind; emails, @names, phone numbers and ids go.
func scrubLesson(s string) string {
	s = scrubURLRe.ReplaceAllString(s, "https://$1/…")
	s = scrubMailRe.ReplaceAllString(s, "<email>")
	s = scrubAtRe.ReplaceAllString(s, "$1@someone")
	s = scrubPhoneRe.ReplaceAllString(s, "<number>")
	s = scrubIDRe.ReplaceAllString(s, "<id>")
	return scrubFileRe.ReplaceAllString(s, "<file>.$1")
}

// unsafeLessonWords are what a lesson must never be about: the instructions,
// safety and secrecy, money and limits (a crafted job could try to teach those).
var unsafeLessonWords = []string{"system prompt", "instructions", "ignore previous", "ignore the", "ignore your", "reveal", "jailbreak",
	"bypass safety", "secret", "password", "api key", "admin", "free vip", "unlimited", "payment", "refund", "پرامپت", "دستورالعمل",
	"نادیده بگیر", "رمز عبور", "ادمین", "رایگان"}

func unsafeLesson(s string) bool {
	low := strings.ToLower(s)
	for _, w := range unsafeLessonWords {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// rebuildLessons writes the skill of a category from its approved lessons
// (and removes it when none is left).
func (b *Bot) rebuildLessons(ctx context.Context, category string) error {
	if category == categoryAlways { // in every prompt, not in a skill
		b.forgetTeachings()
		return nil
	}
	sk := b.app.Agent.Skills
	if sk == nil {
		return nil
	}
	list, err := b.st.Lessons(ctx, store.LessonApproved, category, 200)
	if err != nil {
		return err
	}
	name := lessonSkill + category
	if len(list) == 0 {
		if _, ok := sk.Get(name); ok {
			return sk.Delete(name)
		}
		return nil
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# Lessons learned: %s\n\nRules drawn from earlier jobs where a step failed and another way worked, and rules "+
		"the admins taught; an admin checked each one. Follow them unless the task clearly needs otherwise.\n\n", lessonCategories[category])
	for _, x := range list {
		fmt.Fprintf(&body, "- **%s**: %s", x.Title, x.Rule)
		if strings.Contains(x.Key, taughtMark) {
			body.WriteString(" (taught by an admin)")
		}
		if x.Seen > 1 {
			fmt.Fprintf(&body, " (learned %d times)", x.Seen)
		}
		body.WriteString("\n")
	}
	desc := fmt.Sprintf("Lessons from past jobs about %s: read it before such a job and when a step fails.", lessonCategories[category])
	return sk.Save(name, desc, body.String(), category)
}

// adoptLesson approves a lesson, writes it into its skill and tells the owners
// with a button to take it back.
func (b *Bot) adoptLesson(ctx context.Context, id int64, x *store.Lesson) {
	if err := b.st.DecideLesson(ctx, id, store.LessonApproved); err != nil {
		log.Printf("learning: %v", err)
		return
	}
	if err := b.rebuildLessons(ctx, x.Category); err != nil {
		log.Printf("learning: writing the skill: %v", err)
		return
	}
	log.Printf("🧠 a lesson went into the skills: %s (%s)", x.Title, x.Key)
	for owner := range b.owner {
		l := b.lang(owner)
		html := l.t("🧠 <b>یه درس تازه یاد گرفتم</b> و توی مهارت‌هام نوشتمش:") + "\n\n<b>" + esc(x.Title) + "</b>\n" + esc(x.Rule)
		kb := keyboard(row(b.button(l.t("↩️ پس بگیر"), fmt.Sprintf("a:learn:no:%d", id), telegram.StyleDanger),
			b.button(l.t("👁 دیدن و ویرایش"), fmt.Sprintf("a:learn:ln:%d", id), "")))
		b.sendHTML(ctx, owner, html, telegram.SendOptions{Markup: kb})
	}
}
