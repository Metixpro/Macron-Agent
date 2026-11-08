package main

import (
	"context"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Feedback: how people take the answers. A reaction to one of the bot's
// messages (👍 or 👎), a short complaint right after an answer («اشتباهه»,
// «it didn't work») and the same request again soon after are counted per
// day, with the tools of the answer they are about; with each job's tool
// calls and failures this makes the weekly report (feedback_report.go).
// Nothing a user wrote is kept: only counts, tool names and error lines.

const (
	sentPerChat    = 200              // the bot's messages remembered per chat
	feedbackWindow = 30 * time.Minute // a complaint is about an answer this recent
	repeatWindow   = 15 * time.Minute // the same request again within this is a repeat
)

type answerNote struct {
	tools []string
	at    time.Time
}

type promptNote struct {
	text string
	at   time.Time
}

type feedbackState struct {
	mu      sync.Mutex
	sent    map[int64][]int64 // chat → the bot's latest message ids
	answers map[int64]answerNote
	prompts map[int64]promptNote           // user → their last request
	refs    map[int64]map[int64]*answerRef // chat → message → the request it answers
}

func (f *feedbackState) init() {
	if f.sent == nil {
		f.sent, f.answers, f.prompts, f.refs = map[int64][]int64{}, map[int64]answerNote{}, map[int64]promptNote{}, map[int64]map[int64]*answerRef{}
	}
}

// noteSent remembers a message the bot sent (telegram.Bot.OnSent).
func (b *Bot) noteSent(ctx context.Context, chatID, messageID int64) {
	if c, _ := ctx.Value(sentKey{}).(*sentCollector); c != nil {
		c.add(chatID, messageID)
	}
	f := &b.fb
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	ids := f.sent[chatID]
	if i := slices.Index(ids, messageID); i >= 0 { // an edit (a live message): no second copy
		ids = slices.Delete(ids, i, i+1)
	}
	ids = append(ids, messageID)
	if len(ids) > sentPerChat {
		ids = ids[len(ids)-sentPerChat:]
	}
	f.sent[chatID] = ids
	if len(f.sent) > 20000 { // a bound on chats: the oldest ones go
		for id := range f.sent {
			delete(f.sent, id)
			delete(f.refs, id)
			if len(f.sent) <= 15000 {
				break
			}
		}
	}
}

func (f *feedbackState) isSent(chatID, messageID int64) bool {
	for _, id := range f.sent[chatID] {
		if id == messageID {
			return true
		}
	}
	return false
}

// noteJob counts a finished job: the answer, its tools and their failures.
func (b *Bot) noteJob(chatID int64, res *agent.Result) {
	if res == nil {
		return
	}
	ctx, cancel := b.ctx(10 * time.Second) // a long delivery may have used the answer's time
	defer cancel()
	counts := []store.DailyCount{{Kind: store.DailyJob, N: 1}}
	if res.Partial {
		counts = append(counts, store.DailyCount{Kind: store.DailyPartial, N: 1})
	}
	for tool, n := range res.Calls {
		counts = append(counts, store.DailyCount{Kind: store.DailyCall, Tool: tool, N: n})
	}
	for tool, n := range res.Failed {
		note := scrubLesson(b.redact(res.Errors[tool]))
		counts = append(counts, store.DailyCount{Kind: store.DailyFail, Tool: tool, N: n, Note: note})
	}
	_ = b.st.AddDaily(ctx, counts...)
	f := &b.fb
	f.mu.Lock()
	f.init()
	f.answers[chatID] = answerNote{tools: append([]string(nil), res.ToolsUsed...), at: time.Now()}
	f.mu.Unlock()
}

// goodReactions and badReactions are how a reaction is read.
var (
	goodReactions = map[string]bool{"👍": true, "❤": true, "❤️": true, "🔥": true, "🥰": true, "👏": true, "😁": true, "🎉": true, "🤩": true,
		"🙏": true, "👌": true, "😍": true, "💯": true, "🤝": true, "❤‍🔥": true, "⚡": true, "🏆": true, "🆒": true, "😘": true, "🤗": true}
	badReactions = map[string]bool{"👎": true, "😢": true, "💩": true, "🤮": true, "🤬": true, "😡": true, "🥱": true, "😭": true, "🤡": true, "💔": true}
)

// handleReaction counts a reaction to one of the bot's messages.
func (b *Bot) handleReaction(r *telegram.MessageReactionUpdated) {
	if r.User == nil || r.User.IsBot {
		return
	}
	f := &b.fb
	f.mu.Lock()
	f.init()
	ours, last := f.isSent(r.Chat.ID, r.MessageID), f.answers[r.Chat.ID]
	f.mu.Unlock()
	if !ours {
		return
	}
	kind := ""
	for _, e := range r.Added() {
		switch {
		case goodReactions[e]:
			kind = store.DailyUp
		case badReactions[e]:
			kind = store.DailyDown
		}
	}
	if kind == "" {
		return
	}
	ctx, cancel := b.ctx(10 * time.Second)
	defer cancel()
	_ = b.st.AddDaily(ctx, b.aboutTools(kind, last)...)
}

// aboutTools is a count with one more for each tool of the answer it is about.
func (b *Bot) aboutTools(kind string, a answerNote) []store.DailyCount {
	counts := []store.DailyCount{{Kind: kind, N: 1}}
	if time.Since(a.at) < 24*time.Hour {
		for _, t := range a.tools {
			counts = append(counts, store.DailyCount{Kind: kind, Tool: t, N: 1})
		}
	}
	return counts
}

// complaintRe is a short message saying the last answer was wrong or did not work.
var complaintRe = regexp.MustCompile(`(?i)^\s*(?:نه[،,\s]+)?(?:اشتباه|غلط|درست نیست|کار نکرد|کار نمی\s?کنه|نشد|جواب نداد|باز نمی\s?شه|منظورم این نبود|اینو نخواستم|این نبود|چرت|wrong|incorrect|not what i (?:asked|wanted|meant)|(?:it )?(?:doesn'?t|didn'?t|does not|did not) work|that'?s not (?:it|right))`)

// noteRequest reads a new request for feedback on the answer before it: a
// complaint, or the same request again (the answer did not do it).
func (b *Bot) noteRequest(ctx context.Context, uid, chatID int64, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	norm := normalizeRequest(text)
	f := &b.fb
	f.mu.Lock()
	f.init()
	last, prev := f.answers[chatID], f.prompts[uid]
	f.prompts[uid] = promptNote{text: norm, at: time.Now()}
	if len(f.prompts) > 50000 {
		f.prompts = map[int64]promptNote{uid: f.prompts[uid]}
	}
	f.mu.Unlock()
	recent := time.Since(last.at) < feedbackWindow
	switch {
	case recent && utf8.RuneCountInString(text) <= 80 && complaintRe.MatchString(text):
		_ = b.st.AddDaily(ctx, b.aboutTools(store.DailyComplaint, last)...)
	case recent && len(strings.Fields(norm)) >= 3 && norm == prev.text && time.Since(prev.at) < repeatWindow: // «ادامه بده» twice is no repeat
		_ = b.st.AddDaily(ctx, b.aboutTools(store.DailyRepeat, last)...)
	}
}

// normalizeRequest is a request without case, punctuation and extra spaces.
func normalizeRequest(s string) string {
	s = strings.ToLower(s)
	s = strings.Map(func(r rune) rune {
		if unicode.IsPunct(r) || unicode.IsSymbol(r) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
