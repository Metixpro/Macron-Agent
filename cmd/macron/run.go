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

const maxPending = 8

type activeRun struct {
	cancel  context.CancelFunc
	stopped atomic.Bool
	userID  int64
	chatID  int64
	msgID   int64
	guest   bool
	key     string
	tier    tierID

	mu            sync.Mutex
	pending       []*interjection
	seen          []*interjection
	answering     bool
	queueNext     bool
	closed        bool
	steering      []*interjection
	modelCancel   context.CancelFunc
	modelRedirect bool
	delegates     int
}

type interjection struct {
	m    *telegram.Message
	text string
	msg  llm.Message
	memo string
}

func newRun(uid int64, m *telegram.Message, cancel context.CancelFunc, pending []*interjection, tier tierID) *activeRun {
	key := make([]byte, 4)
	_, _ = rand.Read(key)
	return &activeRun{cancel: cancel, userID: uid, chatID: m.Chat.ID, msgID: m.MessageID, guest: m.GuestQueryID != "", queueNext: m.Chat.Private(),
		key: hex.EncodeToString(key), pending: append([]*interjection(nil), pending...), tier: tier}
}

func (r *activeRun) modelRequest(ctx context.Context) (context.Context, func() bool) {
	r.mu.Lock()
	requestCtx, cancel := context.WithCancel(ctx)
	r.modelCancel, r.modelRedirect = cancel, false
	if len(r.steering) > 0 {
		r.modelRedirect = true
		cancel()
	}
	r.mu.Unlock()
	return requestCtx, func() bool {
		r.mu.Lock()
		defer r.mu.Unlock()
		redirected := r.modelRedirect
		r.modelCancel, r.modelRedirect = nil, false
		cancel()
		return redirected
	}
}

func (r *activeRun) steer(x *interjection) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || r.answering || r.stopped.Load() || r.delegates > 0 || len(r.steering) >= maxPending {
		return false
	}
	r.steering = append(r.steering, x)
	if r.modelCancel != nil {
		r.modelRedirect = true
		r.modelCancel()
	}
	return true
}

func (r *activeRun) delegation(delta int) {
	r.mu.Lock()
	r.delegates += delta
	r.mu.Unlock()
}

func (r *activeRun) stopData() string { return fmt.Sprintf("run:stop:%d:%s", r.userID, r.key) }

func (r *activeRun) stop() {
	r.mu.Lock()
	if !r.queueNext {
		r.pending = nil
	}
	r.mu.Unlock()
	r.stopped.Store(true)
	r.cancel()
}

func (r *activeRun) push(x *interjection) (ok, queued bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false, false
	}
	if r.queueNext && len(r.pending) >= maxPending {
		return false, true
	}
	if n := len(r.pending); n >= maxPending {
		r.pending[n-1] = r.pending[n-1].merge(x)
	} else {
		r.pending = append(r.pending, x)
	}
	return true, r.queueNext || r.answering || r.stopped.Load()
}

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

func (r *activeRun) take() []*interjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.steering) > 0 && !r.answering {
		xs := r.steering
		r.steering = nil
		r.seen = append(r.seen, xs...)
		return xs
	}
	if r.queueNext || r.answering || len(r.pending) == 0 {
		return nil
	}
	xs := r.pending
	r.pending = nil
	r.seen = append(r.seen, xs...)
	return xs
}

func (r *activeRun) add(xs []*interjection) {
	if len(xs) == 0 {
		return
	}
	r.mu.Lock()
	r.pending = append(r.pending, xs...)
	r.mu.Unlock()
}

func (r *activeRun) answered() {
	r.mu.Lock()
	r.answering = true
	r.mu.Unlock()
}

func (r *activeRun) finish() []*interjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	xs := append(r.steering, r.pending...)
	r.steering, r.pending = nil, nil
	return xs
}

func (r *activeRun) memo() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	lines := make([]string, len(r.seen))
	for i, x := range r.seen {
		lines[i] = "- " + x.memo
	}
	return strings.Join(lines, "\n")
}

func (b *Bot) interject(u *store.User, m *telegram.Message, text string) bool {
	return b.interjectMode(u, m, text, false)
}

func (b *Bot) interjectMode(u *store.User, m *telegram.Message, text string, queue bool) bool {
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
	var x *interjection
	var err error
	if !queue && run.queueNext && !m.HasMedia() && strings.TrimSpace(text) != "" {
		x = &interjection{m: m, text: strings.TrimSpace(text), msg: llm.Message{Role: "user", Content: strings.TrimSpace(text)}, memo: strings.TrimSpace(text)}
		if run.steer(x) {
			react.set("✍", false)
			return true
		}
	}
	if run.queueNext {
		x = &interjection{m: m, text: strings.TrimSpace(text)}
	} else {
		x, err = b.interjection(ctx, u, m, text, run)
	}
	if err != nil {
		if errors.Is(err, errTooBig) || errors.Is(err, errWorkspaceFull) {
			b.reply(m, b.fileRefusal(l, err))
		} else {
			b.reply(m, l.t(textError))
		}
		return true
	}
	ok, queued := run.push(x)
	if !ok && queued {
		b.reply(m, l.t("صف پر شده؛ بعد از تمام‌شدن یکی از کارها دوباره بفرست."))
		return true
	}
	if !ok {
		if v, again := b.runs.Load(u.ID); again && v.(*activeRun) != run && v.(*activeRun).chatID == m.Chat.ID {
			run = v.(*activeRun)
			ok, queued = run.push(x)
		}
	}
	switch {
	case !ok && queued:
		b.reply(m, l.t("صف پر شده؛ بعد از تمام‌شدن یکی از کارها دوباره بفرست."))
		return true
	case !ok:
		return false
	case queued:
		react.set(b.seenReaction(), false)
	default:
		react.set("✍", false)
	}
	return true
}

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

func (b *Bot) followUp(u *store.User, xs []*interjection) {
	if len(xs) == 0 {
		return
	}
	_ = b.handlePrompt(u, xs[0].m, xs[0].text, promptOptions{noFlood: true, pending: xs[1:]})
}

var stopWords = map[string]bool{
	"بیخیال": true, "بیخیالشو": true, "بیخیالش": true, "بیخیالششو": true, "ولش": true, "ولشکن": true, "ولکن": true,
	"کنسل": true, "کنسلش": true, "کنسلکن": true, "کنسلشکن": true, "لغو": true, "لغوش": true, "لغوکن": true, "لغوشکن": true,
	"نمیخواد": true, "نمیخوام": true, "نخواستم": true, "وایسا": true, "بسه": true, "بسکن": true, "کافیه": true,
	"دستنگهدار": true, "متوقفشو": true, "متوقفکن": true, "متوقفشکن": true, "استپ": true, "تمومشکن": true, "تمومکن": true,
	"ادامهنده": true, "نکن": true, "انجامنده": true,
	"stop": true, "stopit": true, "cancel": true, "cancelit": true, "abort": true, "nevermind": true, "nvm": true,
	"forgetit": true, "quit": true, "halt": true, "dontdoit": true, "stopworking": true, "enough": true,
}

var stopFiller = map[string]bool{
	"نه": true, "باشه": true, "اوکی": true, "داداش": true, "لطفا": true, "لطفاً": true, "دیگه": true, "کلا": true,
	"کلاً": true, "اصلا": true, "اصلاً": true, "کار": true, "کارو": true, "تسک": true, "تسکو": true, "اینو": true,
	"این": true, "اون": true, "اونو": true, "رو": true, "را": true, "خب": true, "خوب": true, "فعلا": true, "فعلاً": true,
	"ok": true, "okay": true, "no": true, "please": true, "pls": true, "just": true, "the": true, "task": true,
	"job": true, "that": true, "this": true, "now": true, "bro": true,
}

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

func (b *Bot) stopKeyboard(l lang, data string) *telegram.InlineKeyboardMarkup {
	return keyboard(row(b.button(l.t("⏹ توقف"), data, "")))
}

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

		jobMsg, _ := strconv.ParseInt(jobPart, 10, 64)
		if j := b.unfinishedJob(uid); j == nil || j.MsgID != jobMsg || j.ChatID != q.Message.Chat.ID {
			answer(l.t("این کار دیگه نیمه‌کاره نیست (یا یه کار تازه‌تر جاشو گرفته). اگه چیزی مونده، بگو تا انجامش بدم."), true)
			_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
			return
		}
		b.noteResume(uid)
	}
	answer("▶️", false)
	b.prov.ResetHealth()
	_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
	notice := *q.Message
	text := l.t("ادامه بده")
	b.handleMessage(&telegram.Message{MessageID: q.Message.MessageID, From: &q.From, Chat: q.Message.Chat, Date: time.Now().Unix(),
		Text: text, ReplyToMessage: &notice})
}

const (
	autoForever = 0
	autoOff     = -1
)

func (b *Bot) autoContinue() int {
	n := b.st.SettingInt(store.SettingAutoContinue, autoForever)
	if n < 0 {
		return autoOff
	}
	return n
}

func (b *Bot) agentExtensions() int {
	switch n := b.autoContinue(); n {
	case autoForever:
		return agent.ExtendForever
	case autoOff:
		return 0
	default:
		return n
	}
}

var autoSteps = map[int]int{autoForever: 3, 3: 10, 10: autoOff, autoOff: autoForever}

func (b *Bot) idleTimeout() time.Duration {
	return time.Duration(max(b.st.SettingInt(settingIdleTimeout, 15), 0)) * time.Minute
}

const settingIdleTimeout = "idle_timeout"
