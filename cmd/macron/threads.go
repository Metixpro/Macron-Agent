package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Conversations in a group. The bot answers A; B replies to that answer.
// Telegram shows the bot only its own answer, not what A had asked, so the
// bot remembers which of its messages make up the answer to whom and to
// what: B's message then reads as what it is (a person joining the
// conversation about A's question) and gets a natural answer.

const (
	refsPerChat = 400
	refsKept    = 300 // when a chat has too many, the newest stay
)

// answerRef is the request an answer of the bot's was for.
type answerRef struct {
	askerID   int64
	askerName string
	question  string
	at        time.Time
}

// sentCollector gathers the messages sent or edited with one context: the
// parts of one answer, even when other answers go out in the same group at
// the same time.
type sentCollector struct {
	mu  sync.Mutex
	ids map[int64][]int64 // chat → messages
}

type sentKey struct{}

func collectSent(ctx context.Context) (context.Context, *sentCollector) {
	c := &sentCollector{ids: map[int64][]int64{}}
	return context.WithValue(ctx, sentKey{}, c), c
}

func (c *sentCollector) add(chatID, messageID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !slices.Contains(c.ids[chatID], messageID) && len(c.ids[chatID]) < 100 {
		c.ids[chatID] = append(c.ids[chatID], messageID)
	}
}

func (c *sentCollector) in(chatID int64) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.ids[chatID])
}

// linkAnswer ties the messages of an answer to the request they answer.
func (b *Bot) linkAnswer(chatID int64, ids []int64, ref answerRef) {
	if len(ids) == 0 {
		return
	}
	ref.question = textutil.TruncateRunes(strings.Join(strings.Fields(ref.question), " "), 300, "…")
	ref.at = time.Now()
	f := &b.fb
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	refs := f.refs[chatID]
	if refs == nil {
		refs = map[int64]*answerRef{}
		f.refs[chatID] = refs
	}
	r := &ref
	for _, id := range ids {
		refs[id] = r
	}
	if len(refs) > refsPerChat { // the oldest answers go
		type entry struct {
			id int64
			at time.Time
		}
		all := make([]entry, 0, len(refs))
		for id, x := range refs {
			all = append(all, entry{id, x.at})
		}
		slices.SortFunc(all, func(a, b entry) int { return b.at.Compare(a.at) })
		for _, e := range all[refsKept:] {
			delete(refs, e.id)
		}
	}
}

// answerFor is the request an answer message of the bot's was for (nil: not known).
func (b *Bot) answerFor(chatID, messageID int64) *answerRef {
	f := &b.fb
	f.mu.Lock()
	defer f.mu.Unlock()
	f.init()
	if r := f.refs[chatID][messageID]; r != nil {
		c := *r
		return &c
	}
	return nil
}

// replyToAnswer says what a reply in a group to one of the bot's answers
// is: the answer was for someone (who asked what), and now this person
// writes to it.
func (b *Bot) replyToAnswer(m *telegram.Message, uid int64) string {
	r := m.RepliedInChat()
	if r == nil || m.Chat.Private() {
		return ""
	}
	ref := b.answerFor(m.Chat.ID, r.MessageID)
	if ref == nil {
		return "In a group, that message of yours may have been an answer to someone else: the person writing now may be a different person joining in."
	}
	if ref.askerID == uid {
		return fmt.Sprintf("It is your answer to this same person's earlier request («%s»): they are following up on it.", ref.question)
	}
	speaker := "this person"
	if m.From != nil {
		if n := strings.TrimSpace(m.From.FullName()); n != "" {
			speaker = n
		}
	}
	asker := ref.askerName
	if asker == "" {
		asker = "another member"
	}
	return fmt.Sprintf("It is your answer to %s, another member of this group, who had asked: «%s». The person writing now is %s, "+
		"a different person, replying to that answer of yours: they may ask about it, add to it, disagree, joke or ask something new. "+
		"Answer %s naturally, as in a group conversation: talk to them (by name when it fits), use what was asked and answered as "+
		"context, don't repeat your whole earlier answer, and don't take them for %s.", asker, ref.question, speaker, speaker, asker)
}
