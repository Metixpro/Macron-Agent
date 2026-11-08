package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Messages during a job. While the agent works on a request, the user's next
// messages in the same chat do not bounce with "I'm busy":
//
//   - a clear "stop" («بیخیال», «ول کن», «cancel»…) stops the job at once;
//   - anything else reaches the model at its next step: it adds it to the job,
//     changes course, answers it too, or stops if that is what it asks;
//   - what arrives once the answer is written runs right after it.

// maxPending is how many messages can wait for the model at once.
const maxPending = 8

// activeRun is a request being worked on.
type activeRun struct {
	cancel  context.CancelFunc
	stopped atomic.Bool
	userID  int64
	chatID  int64
	msgID   int64  // the message that started the job
	guest   bool   // a guest query: one reply, nothing can be added
	key     string // random: a stop button works only for the job it was made for
	tier    tierID // the tier the job runs on

	mu        sync.Mutex
	pending   []*interjection // not seen by the model yet
	seen      []*interjection // given to the model
	answering bool            // the model is done: new messages wait for the next request
	closed    bool
}

// interjection is a message sent while a job runs.
type interjection struct {
	m    *telegram.Message
	text string      // the user's words (without the bot's name in groups)
	msg  llm.Message // what the model sees
	memo string      // how it is remembered
}

func newRun(uid int64, m *telegram.Message, cancel context.CancelFunc, pending []*interjection, tier tierID) *activeRun {
	key := make([]byte, 4)
	_, _ = rand.Read(key)
	return &activeRun{cancel: cancel, userID: uid, chatID: m.Chat.ID, msgID: m.MessageID, guest: m.GuestQueryID != "",
		key: hex.EncodeToString(key), pending: append([]*interjection(nil), pending...), tier: tier}
}

// stopData is the callback data of the job's stop button.
func (r *activeRun) stopData() string { return fmt.Sprintf("run:stop:%d:%s", r.userID, r.key) }

// stop cancels the job, with the messages sent for it so far (what comes
// after the stop runs as the next request).
func (r *activeRun) stop() {
	r.mu.Lock()
	r.pending = nil
	r.mu.Unlock()
	r.stopped.Store(true)
	r.cancel()
}

// push adds a message. queued: it waits for the next request (the answer is
// already written); full: too many are waiting; ok false: the job is over.
func (r *activeRun) push(x *interjection) (ok, queued bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, false
	}
	if n := len(r.pending); n >= maxPending { // never refused: it joins the last one waiting
		r.pending[n-1] = r.pending[n-1].merge(x)
	} else {
		r.pending = append(r.pending, x)
	}
	return true, r.answering || r.stopped.Load()
}

// merge joins a message to the one before it: what the model sees, what is
// remembered, and its pictures (the message it reacts to stays the first).
func (x *interjection) merge(y *interjection) *interjection {
	out := *x
	out.text = strings.TrimSpace(x.text + "\n" + y.text)
	out.memo = strings.TrimSpace(x.memo + " " + y.memo)
	more := strings.TrimPrefix(y.msg.Text(), agent.InterjectionNote)
	out.msg = llm.Message{Role: "user", Content: x.msg.Text() + "\n" + more}
	if len(x.msg.Parts) > 0 || len(y.msg.Parts) > 0 {
		out.msg.Parts = []llm.Part{llm.TextPart(out.msg.Content)}
		for _, p := range append(append([]llm.Part{}, x.msg.Parts...), y.msg.Parts...) {
			if p.Type != "text" {
				out.msg.Parts = append(out.msg.Parts, p)
			}
		}
	}
	return &out
}

// take hands the waiting messages to the model.
func (r *activeRun) take() []*interjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.answering || len(r.pending) == 0 {
		return nil
	}
	xs := r.pending
	r.pending = nil
	r.seen = append(r.seen, xs...)
	return xs
}

// add hands messages to the job (those of the round before it).
func (r *activeRun) add(xs []*interjection) {
	if len(xs) == 0 {
		return
	}
	r.mu.Lock()
	r.pending = append(r.pending, xs...)
	r.mu.Unlock()
}

// answered marks the end of the model's work: what comes now runs next.
func (r *activeRun) answered() {
	r.mu.Lock()
	r.answering = true
	r.mu.Unlock()
}

// finish closes the run and returns the messages nobody handled yet.
func (r *activeRun) finish() []*interjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	xs := r.pending
	r.pending = nil
	return xs
}

// memo is how the messages the model saw are remembered.
func (r *activeRun) memo() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := make([]string, len(r.seen))
	for i, x := range r.seen {
		lines[i] = "- " + x.memo
	}
	return strings.Join(lines, "\n")
}

// interject routes a message to the user's job in this chat; it reports
// false when there is none (the message is then a new request).
func (b *Bot) interject(u *store.User, m *telegram.Message, text string) bool {
	v, ok := b.runs.Load(u.ID)
	if !ok {
		return false
	}
	run := v.(*activeRun)
	if run.guest || run.chatID != m.Chat.ID {
		return false
	}
	l := b.langOf(m.From)
	react := b.newReactor(m)
	defer react.wait()
	if isStopRequest(text) && !m.HasMedia() {
		run.stop()
		react.set("👌", false)
		return true
	}
	if !b.isAdmin(u.ID) {
		if ok, wait := b.flood.Allow(strconv.FormatInt(u.ID, 10)); !ok {
			b.reply(m, l.f(textFloodWait, int(wait.Seconds())+1))
			return true
		}
	}
	ctx, cancel := b.ctx(2 * time.Minute)
	defer cancel()
	x, err := b.interjection(ctx, u, m, text, run)
	if err != nil {
		if errors.Is(err, errTooBig) || errors.Is(err, errWorkspaceFull) {
			b.reply(m, b.fileRefusal(l, err))
		} else {
			b.reply(m, l.t(textError))
		}
		return true
	}
	ok, queued := run.push(x)
	if !ok { // handed over to the job's next round just now: it takes the message
		if v, again := b.runs.Load(u.ID); again && v.(*activeRun) != run && v.(*activeRun).chatID == m.Chat.ID {
			run = v.(*activeRun)
			ok, queued = run.push(x)
		}
	}
	switch {
	case !ok:
		return false // the job ended in the meantime
	case queued:
		react.set("👀", false) // runs right after the current answer
	default:
		react.set("✍", false) // added to the job
	}
	return true
}

// interjection reads a message sent during a job, with its files.
func (b *Bot) interjection(ctx context.Context, u *store.User, m *telegram.Message, text string, run *activeRun) (*interjection, error) {
	l := b.langOf(m.From)
	text = strings.TrimSpace(text)
	var (
		about  []string
		notes  []string
		images []string
	)
	att, err := b.attachAll(ctx, sessionFor(u.ID), m, b.prov.VisionFor(b.route(ctx, run.tier)), "With it the user sent", true)
	if err != nil {
		return nil, err
	}
	if att != nil {
		about, notes, images = append(about, att.about), append(notes, att.note), att.images
		if text == "" {
			text = att.placeholder
		}
	}
	switch {
	case m.Location != nil:
		about = append(about, fmt.Sprintf("They shared a location: latitude %.6f, longitude %.6f.", m.Location.Latitude, m.Location.Longitude))
	case m.Contact != nil:
		about = append(about, fmt.Sprintf("They shared a contact card: %s %s.", m.Contact.FirstName, m.Contact.LastName))
	}
	if text == "" {
		text = l.t("(پیام خالی)")
	}
	if r := m.RepliedInChat(); r != nil && r.MessageID != run.msgID && (r.From == nil || r.From.ID != b.me.ID) {
		if q := strings.TrimSpace(r.Body()); q != "" {
			about = append(about, "It replies to this message: «"+textutil.TruncateRunes(q, 1000, "…")+"»")
		}
	}
	content := agent.InterjectionNote + text
	if len(about) > 0 {
		content += "\n(" + strings.Join(about, " ") + ")"
	}
	msg := llm.Message{Role: "user", Content: content}
	if len(images) > 0 {
		msg.Parts = []llm.Part{llm.TextPart(content)}
		for _, img := range images {
			msg.Parts = append(msg.Parts, llm.ImagePart(img))
		}
	}
	memo := strings.TrimSpace(strings.Join(append(notes, text), " "))
	return &interjection{m: m, text: text, msg: msg, memo: memo}, nil
}

// repliesToOwnJob reports a group message that replies to the request its
// sender's job is working on: it is meant for the bot even without its name.
func (b *Bot) repliesToOwnJob(m *telegram.Message) bool {
	if m.RepliedInChat() == nil || m.From == nil {
		return false
	}
	v, ok := b.runs.Load(m.From.ID)
	if !ok {
		return false
	}
	run := v.(*activeRun)
	return run.chatID == m.Chat.ID && run.msgID == m.RepliedInChat().MessageID
}

// followUp runs the messages that came after a job's answer, as the next
// request: the first one asks, the others join it.
func (b *Bot) followUp(u *store.User, xs []*interjection) {
	if len(xs) == 0 {
		return
	}
	_ = b.handlePrompt(u, xs[0].m, xs[0].text, promptOptions{noFlood: true, pending: xs[1:]})
}

// Words that stop a job when a message says nothing else. Spaces and the
// zero-width non-joiner are removed before comparing.
var stopWords = map[string]bool{
	"بیخیال": true, "بیخیالشو": true, "بیخیالش": true, "بیخیالششو": true, "ولش": true, "ولشکن": true, "ولکن": true,
	"کنسل": true, "کنسلش": true, "کنسلکن": true, "کنسلشکن": true, "لغو": true, "لغوش": true, "لغوکن": true, "لغوشکن": true,
	"نمیخواد": true, "نمیخوام": true, "نخواستم": true, "وایسا": true, "بسه": true, "بسکن": true, "کافیه": true,
	"دستنگهدار": true, "متوقفشو": true, "متوقفکن": true, "متوقفشکن": true, "استپ": true, "تمومشکن": true, "تمومکن": true,
	"ادامهنده": true, "نکن": true, "انجامنده": true,
	"stop": true, "stopit": true, "cancel": true, "cancelit": true, "abort": true, "nevermind": true, "nvm": true,
	"forgetit": true, "quit": true, "halt": true, "dontdoit": true, "stopworking": true, "enough": true,
}

// Filler around a stop word: «نه دیگه بیخیال تسک شو داداش».
var stopFiller = map[string]bool{
	"نه": true, "باشه": true, "اوکی": true, "داداش": true, "لطفا": true, "لطفاً": true, "دیگه": true, "کلا": true,
	"کلاً": true, "اصلا": true, "اصلاً": true, "کار": true, "کارو": true, "تسک": true, "تسکو": true, "اینو": true,
	"این": true, "اون": true, "اونو": true, "رو": true, "را": true, "خب": true, "خوب": true, "فعلا": true, "فعلاً": true,
	"ok": true, "okay": true, "no": true, "please": true, "pls": true, "just": true, "the": true, "task": true,
	"job": true, "that": true, "this": true, "now": true, "bro": true,
}

// isStopRequest reports a message that only asks to stop the job.
func isStopRequest(text string) bool {
	text = strings.ToLower(textutil.Fold(text))
	text = strings.NewReplacer("‌", "", "ي", "ی", "ك", "ک", "'", "", "’", "").Replace(text)
	words := strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
	if len(words) == 0 || len(words) > 6 {
		return false
	}
	var kept []string
	for _, w := range words {
		if !stopFiller[w] {
			kept = append(kept, w)
		}
	}
	return len(kept) > 0 && stopWords[strings.Join(kept, "")]
}

// stopKeyboard is the stop button under a group's live message, for servers
// that do not take buttons inside rich messages: a plain one, not a red alarm.
func (b *Bot) stopKeyboard(l lang, data string) *telegram.InlineKeyboardMarkup {
	return keyboard(row(b.button(l.t("⏹ توقف"), data, "")))
}

// handleRunCallback stops a job from the button of its live message. Only
// the user who asked or an admin of the bot can (a group's admins cannot),
// and a button stops only the job it was made for, in its own chat: never a
// later job of the same user.
func (b *Bot) handleRunCallback(q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	idPart, key, _ := strings.Cut(strings.TrimPrefix(q.Data, "run:stop:"), ":")
	uid, err := strconv.ParseInt(idPart, 10, 64)
	if err != nil {
		answer("", false)
		return
	}
	if q.From.ID != uid && !b.isAdmin(q.From.ID) {
		answer(l.t("این دکمه مال کسیه که درخواست رو داده."), true)
		return
	}
	v, ok := b.runs.Load(uid)
	if !ok {
		answer(l.t("این کار دیگه تموم شده."), false)
		return
	}
	run := v.(*activeRun)
	if key == "" || key != run.key || (q.Message != nil && q.Message.Chat.ID != run.chatID) {
		answer(l.t("این کار دیگه تموم شده."), false)
		return
	}
	if q.From.ID != uid {
		log.Printf("[user %d] job stopped by admin %d", uid, q.From.ID)
	}
	run.stop()
	answer(l.t("⏹ متوقفش کردم"), false)
}

// handleContinueCallback goes on with a job that ended early (the ▶️ button
// under its notice: "run:go:<user>:<the job's message>"), as if its user had
// said «ادامه بده» in reply to it: the same checks as any message (bans,
// maintenance, quota…) apply.
func (b *Bot) handleContinueCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	uidPart, jobPart, _ := strings.Cut(strings.TrimPrefix(q.Data, "run:go:"), ":")
	uid, err := strconv.ParseInt(uidPart, 10, 64)
	if err != nil || q.Message == nil {
		answer("", false)
		return
	}
	if q.From.ID != uid {
		answer(l.t("این دکمه مال کسیه که درخواست رو داده."), true)
		return
	}
	if v, busy := b.runs.Load(uid); busy && v.(*activeRun).chatID == q.Message.Chat.ID {
		answer(l.t("الان دارم روی همین کار کار می‌کنم."), false)
		return
	}
	if jobPart != "" {
		// The button is for one job: a newer one took its place, or it got done.
		jobMsg, _ := strconv.ParseInt(jobPart, 10, 64)
		if j := b.unfinishedJob(uid); j == nil || j.MsgID != jobMsg || j.ChatID != q.Message.Chat.ID {
			answer(l.t("این کار دیگه نیمه‌کاره نیست (یا یه کار تازه‌تر جاشو گرفته). اگه چیزی مونده، بگو تا انجامش بدم."), true)
			_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
			return
		}
		b.noteResume(uid) // this job, even after other requests
	}
	answer("▶️", false)
	b.prov.ResetHealth()                                                   // «try again»: every provider is a first choice again
	_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil) // used once
	notice := *q.Message
	text := l.t("ادامه بده")
	b.handleMessage(&telegram.Message{MessageID: q.Message.MessageID, From: &q.From, Chat: q.Message.Chat, Date: time.Now().Unix(),
		Text: text, ReplyToMessage: &notice})
}

// maxAutoRounds bounds the auto-continue setting.
const maxAutoRounds = 5

// autoRounds is how many more rounds a job whose budget ran out gets by itself.
func (b *Bot) autoRounds() int {
	return min(max(b.st.SettingInt(store.SettingAutoContinue, 2), 0), maxAutoRounds)
}
