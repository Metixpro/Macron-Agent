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

// When the assistant needs the user's opinion or decision, it asks with a
// native Telegram poll (ask_user). Its turn ends there; the vote comes back
// as the next request, so the work continues with the user's choice.

const (
	pollMaxOptions  = 10
	pollQuestionLen = 300
	pollOptionLen   = 100
	pollMaxAge      = 7 * 24 * time.Hour // older votes are ignored
)

type pollQuestion struct {
	Question string
	Options  []string
	Multiple bool
}

// parsePollArgs validates the ask_user arguments.
func parsePollArgs(args map[string]any) (*pollQuestion, error) {
	raw, _ := args["question"].(string)
	q := strings.TrimSpace(textutil.OneLine(raw))
	if q == "" {
		return nil, errors.New("question is required")
	}
	var opts []string
	seen := map[string]bool{}
	if list, ok := args["options"].([]any); ok {
		for _, v := range list {
			s, _ := v.(string)
			s = textutil.TruncateRunes(strings.TrimSpace(textutil.OneLine(s)), pollOptionLen, "…")
			if s == "" || seen[strings.ToLower(s)] {
				continue
			}
			seen[strings.ToLower(s)] = true
			opts = append(opts, s)
		}
	}
	if len(opts) < 2 {
		return nil, errors.New("give 2 to 10 different, non-empty options")
	}
	if len(opts) > pollMaxOptions {
		opts = opts[:pollMaxOptions]
	}
	multiple, _ := args["multiple"].(bool)
	return &pollQuestion{Question: textutil.TruncateRunes(q, pollQuestionLen, "…"), Options: opts, Multiple: multiple}, nil
}

func (b *Bot) askUserTool(box *outbox) agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolAskUser,
			Description: "Let the user choose with a poll when the choice is really theirs (which item, a trade-off, an irreversible " +
				"step). Your turn ends; their choice comes as their next message. Don't repeat the options in your text.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"question": map[string]any{"type": "string", "description": "≤300 characters"},
				"options": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "minItems": 2, "maxItems": pollMaxOptions,
					"description": "Short, distinct (≤100 each)"},
				"multiple": map[string]any{"type": "boolean"},
			}, "required": []string{"question", "options"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			q, err := parsePollArgs(args)
			if err != nil {
				return agent.ToolOutput{Text: "error: " + err.Error()}, nil
			}
			box.mu.Lock()
			defer box.mu.Unlock()
			if box.ask != nil {
				return agent.ToolOutput{Text: "error: only one question per turn"}, nil
			}
			box.ask = q
			return agent.ToolOutput{Text: "The poll is sent to the user now and your turn ends. Their choice will arrive as their next message.", EndTurn: true}, nil
		},
	}
}

// sendQuestion sends the poll (as a reply when no text came before it),
// records it and returns how the question is remembered. If the poll is
// refused, the question goes out as text with numbered options.
func (b *Bot) sendQuestion(ctx context.Context, u *store.User, m *telegram.Message, q *pollQuestion, reply bool) (remembered, pollID string) {
	l := b.langOf(m.From)
	opts := telegram.SendOptions{}.InTopic(m)
	if reply || !m.Chat.Private() {
		opts.ReplyTo = m.MessageID
	}
	remembered = fmt.Sprintf("[I asked with a poll: «%s» — options: %s]", q.Question, strings.Join(q.Options, " | "))
	sent, err := b.api.SendPoll(ctx, m.Chat.ID, q.Question, q.Options, q.Multiple, opts)
	if err == nil && sent.Poll != nil {
		if err := b.st.SavePoll(ctx, store.Poll{PollID: sent.Poll.ID, UserID: u.ID, ChatID: m.Chat.ID, ChatType: m.Chat.Type,
			MessageID: sent.MessageID, Question: q.Question, Options: q.Options, Multiple: q.Multiple}); err != nil {
			log.Printf("[user %d] saving poll: %v", u.ID, err)
		}
		return remembered, sent.Poll.ID
	}
	if err != nil && !telegram.IsUnreachable(err) {
		log.Printf("[chat %d] poll refused (%v); asking in text", m.Chat.ID, err)
	}
	var sb strings.Builder
	sb.WriteString("<b>" + esc(q.Question) + "</b>\n")
	for i, o := range q.Options {
		fmt.Fprintf(&sb, "\n%d. %s", i+1, esc(o))
	}
	sb.WriteString(l.t("\n\n<i>شماره یا اسم گزینه‌ات رو بفرست.</i>"))
	b.sendHTML(ctx, m.Chat.ID, sb.String(), opts)
	return strings.Replace(remembered, "with a poll", "in text", 1), ""
}

// handlePollAnswer continues the conversation with the user's vote.
func (b *Bot) handlePollAnswer(a *telegram.PollAnswer) {
	if a.User == nil || len(a.OptionIDs) == 0 { // anonymous, or a retracted vote
		return
	}
	ctx, cancel := b.ctx(20 * time.Second)
	defer cancel()
	p, err := b.st.Poll(ctx, a.PollID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			log.Printf("poll %s: %v", a.PollID, err)
		}
		return
	}
	// In a group anyone may vote; only the person who was asked continues the task.
	if p.UserID != a.User.ID || time.Since(p.CreatedAt) > pollMaxAge {
		return
	}
	if ok, err := b.st.MarkPollAnswered(ctx, p.PollID); err != nil || !ok {
		return
	}
	_ = b.api.StopPoll(ctx, p.ChatID, p.MessageID) // closes it and shows the choice

	var choices []string
	for _, id := range a.OptionIDs {
		if id >= 0 && id < len(p.Options) {
			choices = append(choices, p.Options[id])
		}
	}
	if len(choices) == 0 {
		return
	}
	user, _, err := b.st.TouchUser(ctx, userInfo(a.User))
	if err != nil {
		log.Printf("poll %s: %v", a.PollID, err)
		return
	}
	m := &telegram.Message{MessageID: p.MessageID, From: a.User, Chat: telegram.Chat{ID: p.ChatID, Type: p.ChatType}, Date: time.Now().Unix()}
	l := b.langOf(a.User)
	picked := strings.Join(choices, "، ")
	text := l.f("(توی نظرسنجی «%s» انتخاب کردم: %s)", p.Question, picked)
	about := fmt.Sprintf("The user answered your poll «%s» by choosing: %s. Continue the task you were working on, following this choice.", p.Question, picked)
	// A vote that comes while the user has another job joins it (in this chat)
	// or waits for it to end: it is never lost to "busy".
	resume := b.resumeOf(p.PollID) // the job that asked, as it stopped
	if b.interject(user, m, text) {
		return
	}
	b.waitIdle(user.ID, pollWait)
	b.handlePrompt(user, m, text, promptOptions{free: true, vote: true, noReact: true, about: about, resume: resume})
}

// Jobs waiting for a poll's answer, as they stopped (agent.Suspended): the
// vote resumes the job itself, every tool result still in view. Kept in
// memory for suspendTTL, at most suspendMax of them; after that, or after a
// restart, the vote goes on from the conversation's notes as before.
const (
	suspendTTL  = 12 * time.Hour
	suspendMax  = 200
	suspendSize = 6 << 20 // a bigger job is not kept
)

type suspension struct {
	s  *agent.Suspended
	at time.Time
}

// suspendFor keeps a job that asked with a poll.
func (b *Bot) suspendFor(pollID string, s *agent.Suspended) {
	if pollID == "" || s == nil || s.Size() > suspendSize {
		return
	}
	n, oldest, oldestAt := 0, any(nil), time.Now()
	b.suspended.Range(func(k, v any) bool {
		n++
		if at := v.(suspension).at; time.Since(at) > suspendTTL {
			b.suspended.Delete(k)
		} else if at.Before(oldestAt) {
			oldest, oldestAt = k, at
		}
		return true
	})
	if n >= suspendMax && oldest != nil {
		b.suspended.Delete(oldest)
	}
	b.suspended.Store(pollID, suspension{s: s, at: time.Now()})
}

// resumeOf takes the job a poll's answer resumes (nil: none kept).
func (b *Bot) resumeOf(pollID string) *agent.Suspended {
	v, ok := b.suspended.LoadAndDelete(pollID)
	if !ok || time.Since(v.(suspension).at) > suspendTTL {
		return nil
	}
	return v.(suspension).s
}

// pollWait is how long a vote waits for the user's running job (a variable for the tests).
var pollWait = 10 * time.Minute

// waitIdle waits until the user has no job running, at most max.
func (b *Bot) waitIdle(uid int64, max time.Duration) {
	key := strconv.FormatInt(uid, 10)
	deadline := time.Now().Add(max)
	for b.gate.Busy(key) && time.Now().Before(deadline) {
		select {
		case <-time.After(time.Second):
		case <-b.jobCtx.Done():
			return
		}
	}
}
