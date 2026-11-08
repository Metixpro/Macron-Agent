package main

import (
	"context"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Guest mode (Bot API 10.0): with "Guest Mode" on in BotFather's Mini App,
// anyone can write "@WazeAiChatBot 25 * 40" in any private chat or group,
// even one the bot is not in, or reply to the bot's guest answer. The bot
// receives Update.guest_message (the summoning message and the one it
// replies to) and sends exactly one reply with answerGuestQuery.

// handleGuest answers a guest message.
func (b *Bot) handleGuest(m *telegram.Message) {
	if b.personal && !m.Chat.Private() {
		return
	}
	l := b.langOf(m.From)
	if m.From == nil || m.From.IsBot || m.GuestQueryID == "" {
		return
	}
	ctx, cancel := b.ctx(20 * time.Second)
	defer cancel()
	text := stripMention(m.Body(), b.me.Username)
	if text == "" && !m.HasMedia() {
		text = b.calledOnly(m)
	}

	user, _, err := b.st.TouchUser(ctx, userInfo(m.From))
	if err != nil {
		log.Printf("guest %d: %v", m.From.ID, err)
		b.answerGuestHTML(ctx, m.GuestQueryID, l.t(textError), nil)
		return
	}
	about := fmt.Sprintf(guestAbout, chatKindEn(m.Chat.Type))
	// The one reply goes out now, as «thinking»; it becomes the answer.
	g := b.guestThinking(ctx, m.GuestQueryID, l)
	_ = b.handlePrompt(user, m, text, promptOptions{noReact: true, about: about})
	if g != nil {
		b.finishGuest(g, l)
	}
}

// finishGuest ends a placeholder the run left alone: never «thinking» for good.
func (b *Bot) finishGuest(g *guestReply, l lang) {
	if g.isFinal() {
		return
	}
	ctx, cancel := b.ctx(15 * time.Second)
	defer cancel()
	g.finishHTML(ctx, l.t(textGuestNoAnswer), nil)
}

const guestAbout = `You were summoned in guest mode: someone mentioned you in a %s you are not a member of. You can send exactly ONE reply, right now: no files, polls, reactions or follow-up messages, and you cannot see the chat's history, only this message and the message it replies to. Answer completely but compactly. If the request is code or starts with a language shorthand (py, js, sh, php, go, c), run it and show the real output in a code block; for a calculation, compute it exactly.`

func chatKindEn(t string) string {
	switch t {
	case "private":
		return "private chat between other people"
	case "channel":
		return "channel"
	}
	return "group"
}

var mentionSep = regexp.MustCompile(`^[\s,:،\-]+`)

// stripMention removes "@botname" from the text.
func stripMention(text, username string) string {
	if username != "" {
		re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(username) + `\b`)
		text = re.ReplaceAllString(text, "")
	}
	return strings.TrimSpace(mentionSep.ReplaceAllString(strings.TrimSpace(text), ""))
}

// guestReply is the one message a guest query gets when the work takes a
// while: a «thinking» placeholder sent right away, edited with what the bot
// is doing, and finally with the answer (or a notice). After that it does
// not change any more.
type guestReply struct {
	b        *Bot
	queryID  string
	inlineID string // the placeholder, edited by its inline_message_id
	lng      lang

	mu    sync.Mutex // one edit at a time: a late status never lands on the answer
	rich  bool       // the placeholder is a rich message (edits stay rich)
	final bool       // the answer is in
	shown string     // what a status edit shows now
}

// guestThinking sends the placeholder. nil when it could not be sent, or
// the server gave no id to edit it with: the answer then goes the old way.
func (b *Bot) guestThinking(ctx context.Context, queryID string, l lang) *guestReply {
	if b.guestStubOff.Load() || b.guestReply(queryID) != nil {
		return nil
	}
	if _, dup := b.guestAnswered.Load(queryID); dup {
		return nil
	}
	g := &guestReply{b: b, queryID: queryID, lng: l}
	status := thinkingStatus(l)
	var tries []any
	if !b.richOff.Load() {
		rm := telegram.RichMessage{Markdown: status, IsRTL: l == fa}
		if set := b.premium(); set != nil {
			fancy := rm
			fancy.Markdown = set.Markdown(rm.Markdown)
			if fancy.Markdown != rm.Markdown {
				tries = append(tries, telegram.GuestRich(fancy, nil))
			}
		}
		tries = append(tries, telegram.GuestRich(rm, nil))
	}
	html := telegram.EscapeHTML(status)
	if set := b.premium(); set != nil {
		if fancy := set.HTML(html); fancy != html {
			tries = append(tries, telegram.GuestHTML(fancy, nil))
		}
	}
	tries = append(tries, telegram.GuestHTML(html, nil))
	for _, try := range tries {
		id, err := b.api.AnswerGuestQuery(ctx, queryID, try)
		if err != nil {
			if !telegram.IsBadRequest(err) || ctx.Err() != nil {
				log.Printf("guest query %s: placeholder: %v", queryID, err)
				return nil
			}
			continue // a plainer look
		}
		if id == "" {
			// An older server: nothing to edit. The answer tries the old way
			// (one answerGuestQuery), and later queries skip the placeholder.
			log.Printf("guest query %s: no inline_message_id: guest answers go without a «thinking» placeholder", queryID)
			b.guestStubOff.Store(true)
			return nil
		}
		b.guestAnswered.Store(queryID, true) // the one answerGuestQuery is spent
		time.AfterFunc(10*time.Minute, func() { b.guestAnswered.Delete(queryID) })
		g.inlineID = id
		_, g.rich = try.(map[string]any)["input_message_content"].(map[string]any)["rich_message"]
		g.shown = status
		b.guestReplies.Store(queryID, g)
		time.AfterFunc(2*time.Hour, func() { b.guestReplies.CompareAndDelete(queryID, g) })
		return g
	}
	return nil
}

func (g *guestReply) isFinal() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.final
}

// guestReply is the placeholder of a guest query, if it has one.
func (b *Bot) guestReply(queryID string) *guestReply {
	if v, ok := b.guestReplies.Load(queryID); ok {
		return v.(*guestReply)
	}
	return nil
}

// progress shows what the bot is doing (a status line), or the answer as it
// is written, until the answer is in.
func (g *guestReply) progress(ctx context.Context, md string, rtl, status bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.final || md == g.shown || strings.TrimSpace(md) == "" {
		return
	}
	ectx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if g.edit(ectx, md, rtl, status) == nil {
		g.shown = md
	}
}

// finishHTML puts a notice in place of the placeholder; false when it is
// already final.
func (g *guestReply) finishHTML(ctx context.Context, html string, kb *telegram.InlineKeyboardMarkup) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.final {
		log.Printf("guest query %s: already answered", g.queryID)
		return false
	}
	g.final = true
	html = firstHTMLPart(html, telegram.MaxGuestTextChars)
	if set := g.b.premium(); set != nil {
		if err := g.b.api.EditInlineHTML(ctx, g.inlineID, set.HTML(html), kb); err == nil {
			return true
		}
	}
	if err := g.b.api.EditInlineHTML(ctx, g.inlineID, html, kb); err != nil {
		log.Printf("guest query %s: %v", g.queryID, err)
	}
	return true
}

// finishMarkdown puts the answer in place of the placeholder: rich when the
// placeholder is rich, otherwise HTML (cut to fit one message).
func (g *guestReply) finishMarkdown(ctx context.Context, md string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.final {
		log.Printf("guest query %s: already answered", g.queryID)
		return
	}
	g.final = true
	more := g.lng.f(textGuestMore, g.b.me.Username)
	if g.rich {
		body := telegram.FoldLong(md, foldOptions(g.lng)) // one reply: a long one folds
		if len([]rune(body)) > telegram.MaxRichChars-400 {
			body = telegram.CloseOpenMarkdown(textutil.TruncateRunes(md, telegram.MaxRichChars-400, "…")) + "\n\n" + more
		}
		err := g.edit(ctx, body, telegram.IsMostlyRTL(body), false)
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("guest query %s: rich answer refused (%v); sending text", g.queryID, err)
	}
	html := telegram.MarkdownToHTML(md)
	if parts := telegram.SplitHTML(html, telegram.MaxGuestTextChars-200); len(parts) > 1 {
		html = parts[0] + "\n\n" + more
	}
	if set := g.b.premium(); set != nil {
		if err := g.b.api.EditInlineHTML(ctx, g.inlineID, set.HTML(html), nil); err == nil {
			return
		}
	}
	if err := g.b.api.EditInlineHTML(ctx, g.inlineID, html, nil); err != nil {
		log.Printf("guest query %s: %v", g.queryID, err)
	}
}

// edit replaces the placeholder with md (g.mu held), premium emoji first; a
// status line also gets its own icon.
func (g *guestReply) edit(ctx context.Context, md string, rtl, status bool) error {
	if !g.rich {
		html := telegram.MarkdownToHTML(md)
		if set := g.b.premium(); set != nil {
			fancy := html
			if fancy = set.HTML(fancy); fancy != html && g.b.api.EditInlineHTML(ctx, g.inlineID, fancy, nil) == nil {
				return nil
			}
		}
		return g.b.api.EditInlineHTML(ctx, g.inlineID, html, nil)
	}
	if set := g.b.premium(); set != nil {
		fancy := md
		if fancy = set.Markdown(fancy); fancy != md && g.b.api.EditInlineRich(ctx, g.inlineID, telegram.RichMessage{Markdown: fancy, IsRTL: rtl}, nil) == nil {
			return nil
		}
	}
	return g.b.api.EditInlineRich(ctx, g.inlineID, telegram.RichMessage{Markdown: md, IsRTL: rtl}, nil)
}

// guestOnce makes sure a guest query is answered once (Telegram allows one reply).
func (b *Bot) guestOnce(queryID string) bool {
	if _, dup := b.guestAnswered.LoadOrStore(queryID, true); dup {
		log.Printf("guest query %s: already answered", queryID)
		return false
	}
	time.AfterFunc(10*time.Minute, func() { b.guestAnswered.Delete(queryID) })
	return true
}

// answerGuestHTML sends a short HTML reply (premium emoji when possible).
func (b *Bot) answerGuestHTML(ctx context.Context, queryID, html string, kb *telegram.InlineKeyboardMarkup) {
	if g := b.guestReply(queryID); g != nil {
		g.finishHTML(ctx, html, kb)
		return
	}
	if !b.guestOnce(queryID) {
		return
	}
	html = firstHTMLPart(html, telegram.MaxGuestTextChars)
	if set := b.premium(); set != nil {
		if _, err := b.api.AnswerGuestQuery(ctx, queryID, telegram.GuestHTML(set.HTML(html), kb)); err == nil {
			return
		}
	}
	if _, err := b.api.AnswerGuestQuery(ctx, queryID, telegram.GuestHTML(html, kb)); err != nil {
		log.Printf("guest query %s: %v", queryID, err)
	}
}

// answerGuestMarkdown sends the assistant's answer: a rich message when the
// server supports it, otherwise classic HTML (cut to fit one message).
func (b *Bot) answerGuestMarkdown(ctx context.Context, queryID, md string, l lang) {
	if g := b.guestReply(queryID); g != nil {
		g.finishMarkdown(ctx, md)
		return
	}
	if !b.guestOnce(queryID) {
		return
	}
	more := l.f(textGuestMore, b.me.Username)
	if !b.richOff.Load() {
		body := telegram.FoldLong(md, foldOptions(l)) // one reply: a long one folds
		if len([]rune(body)) > telegram.MaxRichChars-400 {
			body = md // too long even folded: cut, never inside a folded block
			body = telegram.CloseOpenMarkdown(textutil.TruncateRunes(body, telegram.MaxRichChars-400, "…")) + "\n\n" + more
		}
		rtl := telegram.IsMostlyRTL(body)
		if set := b.premium(); set != nil {
			if _, err := b.api.AnswerGuestQuery(ctx, queryID, telegram.GuestRich(telegram.RichMessage{Markdown: set.Markdown(body), IsRTL: rtl}, nil)); err == nil {
				return
			}
		}
		_, err := b.api.AnswerGuestQuery(ctx, queryID, telegram.GuestRich(telegram.RichMessage{Markdown: body, IsRTL: rtl}, nil))
		if err == nil || ctx.Err() != nil {
			return
		}
		log.Printf("guest query %s: rich answer refused (%v); sending text", queryID, err)
	}
	html := telegram.MarkdownToHTML(md)
	if parts := telegram.SplitHTML(html, telegram.MaxGuestTextChars-200); len(parts) > 1 {
		html = parts[0] + "\n\n" + more
	}
	if _, err := b.api.AnswerGuestQuery(ctx, queryID, telegram.GuestHTML(html, nil)); err != nil {
		log.Printf("guest query %s: %v", queryID, err)
	}
}

// firstHTMLPart keeps what fits in one message, cutting at a safe place.
func firstHTMLPart(html string, max int) string {
	if parts := telegram.SplitHTML(html, max-10); len(parts) > 1 {
		return parts[0] + "…"
	}
	return html
}
