package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	draftEvery = 700 * time.Millisecond

	streamFrom      = 600
	streamFromFirst = 1500
	stepEvery       = 250 * time.Millisecond
	draftKeepAlive  = 9 * time.Second
	actionEvery     = 4500 * time.Millisecond
	tickEvery       = 2 * time.Second
)

var (
	groupEditEvery    = 3 * time.Second
	privateEditEvery  = 1200 * time.Millisecond
	privateClockEvery = 5 * time.Second
	traceMsgDelay     = 1200 * time.Millisecond
	groupClockEvery   = 15 * time.Second
	stopButtonAfter   = 6 * time.Second
	guestEditEvery    = 2500 * time.Millisecond
)

type liveView struct {
	b       *Bot
	chatID  int64
	userID  int64
	replyTo int64
	draftID int64
	private bool
	canStop bool
	runKey  string

	mu        sync.Mutex
	drafts    bool
	terminal  bool
	tracing   bool
	worked    bool
	status    string
	text      string
	action    string
	tr        trace
	think     thinkLog
	noting    bool
	started   time.Time
	dirty     bool
	urgent    bool
	lastFlush time.Time

	traceMsg int64
	lastEdit time.Time
	firstAt  time.Time
	rich     bool
	stopData string
	shown    string
	shownKB  bool
	gone     bool

	stopOnce sync.Once
	done     chan struct{}
	finished chan struct{}

	muted bool
	guest *guestReply
	lng   lang

	detached bool
}

func (b *Bot) newLive(m *telegram.Message, userID int64) *liveView {
	l := &liveView{
		b: b, chatID: m.Chat.ID, userID: userID, replyTo: m.MessageID, draftID: rand.Int64N(1<<31) + 1,
		private: m.Chat.Private(), canStop: true,
		drafts: m.Chat.Private() && m.MessageID == 0, terminal: b.st.SettingBool(store.SettingLiveTrace, true),
		status: thinkingStatus(b.langOf(m.From)), action: "typing", dirty: true, started: time.Now(), lng: b.langOf(m.From),
		done: make(chan struct{}), finished: make(chan struct{}),
	}
	if m.GuestQueryID != "" {

		l.muted, l.drafts, l.terminal = true, false, false
		if g := b.guestReply(m.GuestQueryID); g != nil {
			l.muted, l.guest, l.lastEdit, l.dirty = false, g, time.Now(), false
		}
	}

	l.rich = !l.private && !l.drafts && !l.muted && m.MessageID != 0 && !b.richOff.Load()
	return l
}

func (l *liveView) detach(title string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.detached, l.drafts, l.canStop = true, false, false
	l.rich = !l.muted && l.replyTo != 0 && !l.b.richOff.Load()
	l.tr.title = title
}

type draftOwner struct {
	userID, chatID int64
	key            string
}

func (l *liveView) editEvery() time.Duration {
	if l.private {
		return privateEditEvery
	}
	return groupEditEvery
}

func (l *liveView) clockEvery() time.Duration {
	if l.private {
		return privateClockEvery
	}
	return groupClockEvery
}

func (l *liveView) start(ctx context.Context) {
	if l.muted {
		close(l.finished)
		return
	}
	if l.drafts && l.canStop && l.runKey != "" {
		l.b.drafts.Store(l.draftID, draftOwner{userID: l.userID, chatID: l.chatID, key: l.runKey})
	}
	go func() {
		defer close(l.finished)
		tick := time.NewTicker(stepEvery)
		defer tick.Stop()
		if l.guest == nil {
			l.sendAction(ctx)
		}
		lastAction := time.Now()
		l.flush(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-l.done:
				return
			case <-tick.C:
				if time.Since(lastAction) >= actionEvery && l.guest == nil {
					l.sendAction(ctx)
					lastAction = time.Now()
				}
				l.mu.Lock()

				if l.drafts && l.tracing && l.text == "" && time.Since(l.lastFlush) >= tickEvery {
					l.dirty = true
				}
				if l.rich && l.tracing && l.text == "" && time.Since(l.lastEdit) >= l.clockEvery() {
					l.dirty = true
				}
				if l.rich && l.traceMsg != 0 && !l.shownKB && l.wantsStop() {
					l.dirty = true
				}
				since := time.Since(l.lastFlush)
				due := (l.dirty && (since >= draftEvery || l.urgent && since >= stepEvery)) || (l.drafts && since >= draftKeepAlive)
				l.mu.Unlock()
				if due {
					l.flush(ctx)
				}
			}
		}
	}()
}

func (l *liveView) stop() {
	l.stopOnce.Do(func() {
		close(l.done)
		<-l.finished
		l.b.drafts.Delete(l.draftID)
	})
}

func (l *liveView) discard() {
	l.stop()
	l.mu.Lock()
	id := l.traceMsg
	l.traceMsg = 0
	l.mu.Unlock()
	if id != 0 {
		ctx, cancel := l.b.ctx(10 * time.Second)
		defer cancel()
		_ = l.b.api.DeleteMessage(ctx, l.chatID, id)
	}
}

func (l *liveView) toolStart(sub int, name string) { l.toolStartWith(sub, name, "") }

func (l *liveView) toolStartWith(sub int, name, args string) {
	note := ""
	if toolLabel(name) != "" && !l.termOn() {
		note = l.redact(thinkText(l.lng, name, args))
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.worked = true
	if a := actionFor(name); a != "" {
		l.action = a
	}
	if status := statusFor(l.lng, name); status != "" {
		l.status = status
	}

	if toolLabel(name) == "" {
		return
	}
	if l.terminal {
		if l.tr.start(sub, name, time.Now()) && !l.tracing {
			l.tracing, l.firstAt = true, time.Now()
		}
	} else if note != "" {
		l.think.start(sub, name, note, time.Now())
		l.noting = true
	}
	l.text, l.dirty, l.urgent = "", true, true
}

func (l *liveView) redact(s string) string {
	if l.b == nil {
		return s
	}
	return l.b.redact(s)
}

func (l *liveView) termOn() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.terminal
}

func (l *liveView) subAgent(title string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.terminal {
		l.noting = true
		l.text, l.dirty, l.urgent = "", true, true
		return l.think.subStart(l.lng.f("دستیار کمکی: %s", textutil.TruncateRunes(l.redact(title), 40, "…")), time.Now())
	}
	id := l.tr.subStart(l.redact(title), time.Now())
	if !l.tracing {
		l.tracing, l.firstAt = true, time.Now()
	}
	l.text, l.dirty, l.urgent = "", true, true
	return id
}

func (l *liveView) subDone(id int, failed bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id == 0 {
		return
	}
	if l.terminal {
		l.tr.subEnd(id, failed)
	} else {
		l.think.subEnd(id, failed, time.Now())
	}
	l.dirty, l.urgent = true, true
}

func (l *liveView) setPlan(items []agent.PlanItem) {
	shown := make([]agent.PlanItem, len(items))
	for i, it := range items {
		shown[i] = agent.PlanItem{Text: l.b.redact(it.Text), Status: it.Status}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.tr.plan = shown
	l.text, l.dirty, l.urgent = "", true, true
}

func (l *liveView) note() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.terminal {
		l.tr.event("New Message", time.Now())
	}
	l.text, l.status, l.dirty, l.urgent = "", thinkingStatus(l.lng), true, true
}

func (l *liveView) toolProgress(sub int, name, text string, output bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if toolLabel(name) == "" {
		return
	}
	if !l.terminal {
		if !output {
			l.think.progress(sub, name, textutil.OneLine(text))
			l.dirty = true
		}
		return
	}
	l.tr.progress(sub, name, textutil.OneLine(text), output)
	l.dirty = true
}

func (l *liveView) toolEnd(sub int, name, result string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.status, l.action = thinkingStatus(l.lng), "typing"

	if toolLabel(name) == "" {
		return
	}
	if !l.terminal {
		l.think.end(sub, name, failedResult(result), time.Now())
		l.dirty, l.urgent = true, true
		return
	}
	l.tr.end(sub, name, failedResult(result))
	l.dirty, l.urgent = true, true
}

func (l *liveView) context(used, window int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.terminal {
		return
	}
	l.tr.ctxUsed, l.tr.ctxWindow = used, window
	l.dirty = true
}

func (l *liveView) compacted(before, after, window int, summarized bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.terminal {
		return
	}
	l.tr.ctxWindow = window
	l.tr.compacted(before, after, summarized, time.Now())
	l.dirty, l.urgent = true, true
}

func (l *liveView) escalated(string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.terminal {
		return
	}
	l.tr.eventMarked("🧠 ", "Harder than it looked: thinking deeper", time.Now())
	l.dirty, l.urgent = true, true
}

func (l *liveView) setText(partial string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.text, l.dirty = partial, true
}

func (l *liveView) sendAction(ctx context.Context) {
	l.mu.Lock()
	action, quiet := l.action, l.detached
	l.mu.Unlock()
	if quiet {
		return
	}
	actx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = l.b.api.SendChatAction(actx, l.chatID, action)
}

func (l *liveView) flush(ctx context.Context) {
	l.mu.Lock()
	if l.guest != nil {
		l.mu.Unlock()
		l.flushGuest(ctx)
		return
	}
	if !l.drafts {
		rich := l.rich
		l.mu.Unlock()
		if rich {
			l.flushRich(ctx)
		} else {
			l.flushGroup(ctx)
		}
		return
	}
	text, status, tracing := l.text, l.status, l.tracing
	trace := l.tr.markdown(time.Now(), l.started, true)
	plan := l.tr.planMD()
	notes := ""
	if l.noting && !l.terminal {
		notes = l.think.markdown(l.lng, time.Now(), l.started, plan)
	}
	l.dirty, l.urgent, l.lastFlush = false, false, time.Now()
	l.mu.Unlock()

	var rm telegram.RichMessage
	switch {
	case text != "":
		md := telegram.CloseOpenMarkdown(textutil.TruncateRunes(l.b.redact(withoutMediaRefs(text)), telegram.MaxRichChars-200, "…"))
		rm = telegram.RichMessage{Markdown: md, IsRTL: telegram.IsMostlyRTL(text)}
	case tracing:
		rm = telegram.RichMessage{Markdown: trace}
	case notes != "":
		rm = telegram.RichMessage{Markdown: notes, IsRTL: l.lng == fa}
	case plan != "":
		rm = telegram.RichMessage{Markdown: escapeMarkdown(status) + "\n\n" + plan, IsRTL: l.lng == fa}
	default:

		rm = telegram.RichMessage{HTML: "<tg-thinking>" + telegram.EscapeHTML(status) + "</tg-thinking>", IsRTL: l.lng == fa}
	}
	dctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	if set := l.b.premium(); set != nil {
		fancy := rm

		if fancy.HTML != "" {
			fancy.HTML = set.HTML(fancy.HTML)
		} else {
			fancy.Markdown = set.Markdown(fancy.Markdown)
		}
		if (fancy.HTML != rm.HTML || fancy.Markdown != rm.Markdown) && l.b.api.SendRichDraft(dctx, l.chatID, l.draftID, fancy, l.canStop) == nil {
			return
		}
	}
	err := l.b.api.SendRichDraft(dctx, l.chatID, l.draftID, rm, l.canStop)
	if err == nil || ctx.Err() != nil {
		return
	}
	if telegram.IsUnsupported(err) || telegram.IsBadRequest(err) {

		if text != "" || tracing {
			if tracing && text == "" {
				l.mu.Lock()
				l.tracing = false
				l.mu.Unlock()
			}
			return
		}
		log.Printf("[chat %d] drafts disabled: %v", l.chatID, err)
		l.mu.Lock()
		l.drafts = false
		l.mu.Unlock()
	}
}

func (l *liveView) flushPrivateMessage(ctx context.Context) {
	l.mu.Lock()
	id := l.traceMsg
	if l.gone || (id != 0 && (!l.dirty || (!l.urgent && time.Since(l.lastEdit) < l.editEvery()))) {
		l.mu.Unlock()
		return
	}
	body, _ := l.groupBody()
	withStop := l.canStop && l.stopData != ""
	if id != 0 && body == l.shown && withStop == l.shownKB {
		l.dirty, l.urgent = false, false
		l.mu.Unlock()
		return
	}
	l.dirty, l.urgent, l.lastEdit = false, false, time.Now()
	stopData := l.stopData
	l.mu.Unlock()
	html := telegram.MarkdownToHTML(body)
	if set := l.b.premium(); set != nil {
		html = set.HTML(html)
	}
	var kb *telegram.InlineKeyboardMarkup
	if withStop {
		kb = l.b.stopKeyboard(l.lng, stopData)
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var err error
	if id != 0 {
		err = l.b.api.EditMessageText(sctx, l.chatID, id, html, kb)
	} else {
		var sent *telegram.Message
		sent, err = l.b.api.SendHTML(sctx, l.chatID, html, telegram.SendOptions{ReplyTo: l.replyTo, Silent: true, Markup: kb})
		if err == nil {
			id = sent.MessageID
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil && !telegram.IsNotModified(err) {
		l.gone = messageGone(err)
		l.dirty = !l.gone
		return
	}
	l.traceMsg, l.shown, l.shownKB = id, body, withStop
}

func (l *liveView) flushGroup(ctx context.Context) {
	if l.private {
		l.flushPrivateMessage(ctx)
		return
	}
	l.mu.Lock()
	if !l.terminal || !l.tracing || !l.dirty || time.Since(l.lastEdit) < l.editEvery() || time.Since(l.firstAt) < traceMsgDelay {
		l.mu.Unlock()
		return
	}
	html := l.tr.html(time.Now(), l.started, false)
	id := l.traceMsg
	l.dirty, l.urgent, l.lastFlush, l.lastEdit = false, false, time.Now(), time.Now()
	l.mu.Unlock()

	tries := []string{html}
	if set := l.b.premium(); set != nil {
		if fancy := set.HTML(html); fancy != html {
			tries = []string{fancy, html}
		}
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var err error
	if id != 0 {
		for _, h := range tries {
			if err = l.b.api.EditMessageText(sctx, l.chatID, id, h, nil); err == nil || telegram.IsNotModified(err) || !telegram.IsBadRequest(err) {
				break
			}
		}
		if err != nil && !telegram.IsNotModified(err) && telegram.IsBadRequest(err) {
			l.mu.Lock()
			l.terminal = false
			l.mu.Unlock()
		}
		return
	}
	var sent *telegram.Message
	for _, h := range tries {
		if sent, err = l.b.api.SendHTML(sctx, l.chatID, h, telegram.SendOptions{ReplyTo: l.replyTo, Silent: true}); err == nil || !telegram.IsBadRequest(err) {
			break
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if err != nil {
		l.terminal = false
		return
	}
	select {
	case <-l.done:

		go func() {
			dctx, cancel := l.b.ctx(10 * time.Second)
			defer cancel()
			_ = l.b.api.DeleteMessage(dctx, l.chatID, sent.MessageID)
		}()
	default:
		l.traceMsg = sent.MessageID
	}
}

func (l *liveView) flushGuest(ctx context.Context) {
	l.mu.Lock()
	if !l.dirty || time.Since(l.lastEdit) < guestEditEvery {
		l.mu.Unlock()
		return
	}
	body, rtl := l.groupBody()
	l.dirty, l.urgent, l.lastFlush, l.lastEdit = false, false, time.Now(), time.Now()
	l.mu.Unlock()
	l.guest.progress(ctx, body, rtl, l.text == "")
}

func (l *liveView) groupBody() (md string, rtl bool) {
	switch {
	case l.text != "":
		md := telegram.CloseOpenMarkdown(textutil.TruncateRunes(l.b.redact(withoutMediaRefs(l.text)), partChars, "…"))
		return md, telegram.IsMostlyRTL(l.text)
	case l.tracing:
		return l.tr.markdown(time.Now(), l.started, false), false
	case l.noting && !l.terminal:
		return l.bgHead() + l.think.markdown(l.lng, time.Now(), l.started, l.tr.planMD()), l.lng == fa
	case len(l.tr.plan) > 0:
		return l.bgHead() + escapeMarkdown(l.status) + "\n\n" + l.tr.planMD(), l.lng == fa
	}

	return l.bgHead() + l.status, l.lng == fa
}

func (l *liveView) bgHead() string {
	if !l.detached || l.tr.title == "" {
		return ""
	}
	return "**" + escapeMarkdown(l.lng.f("🔄 کار پس‌زمینه: %s", textutil.TruncateRunes(l.tr.title, 60, "…"))) + "**\n"
}

func (l *liveView) stopRow() string {
	align := "left"
	if l.lng == fa {
		align = "right"
	}

	raw := l.lng.t("⏹ توقف")
	content := telegram.EscapeHTML(raw)
	if set := l.b.premium(); set != nil {
		if text, iconID := set.Button(raw); iconID != "" {
			content = fmt.Sprintf(`<tg-emoji emoji-id="%s"></tg-emoji> %s`, iconID, telegram.EscapeHTML(text))
		}
	}
	row := fmt.Sprintf(`<tg-button-row align="%s"><tg-button type="callback_data" style="link" data="%s">%s</tg-button></tg-button-row>`,
		align, telegram.EscapeHTML(l.stopData), content)
	return "\n\n" + row
}

type liveTry struct {
	rm    telegram.RichMessage
	kb    *telegram.InlineKeyboardMarkup
	noRow bool
}

func (l *liveView) wantsStop() bool {
	return l.tracing || l.noting || time.Since(l.started) >= stopButtonAfter
}

func (l *liveView) flushRich(ctx context.Context) {
	l.mu.Lock()
	id := l.traceMsg
	if l.gone || (id != 0 && (!l.dirty || time.Since(l.lastEdit) < l.editEvery())) {
		l.mu.Unlock()
		return
	}
	body, rtl := l.groupBody()
	withStop := l.wantsStop() && l.stopData != ""
	l.dirty, l.urgent, l.lastFlush = false, false, time.Now()
	if id != 0 && body == l.shown && withStop == l.shownKB {
		l.mu.Unlock()
		return
	}
	l.lastEdit = time.Now()
	l.mu.Unlock()

	row := withStop && !l.private && !l.b.stopRowOff.Load()
	full, kb := body, (*telegram.InlineKeyboardMarkup)(nil)
	switch {
	case row:
		full += l.stopRow()
	case withStop:
		kb = l.b.stopKeyboard(l.lng, l.stopData)
	}
	var tries []liveTry
	if set := l.b.premium(); set != nil {
		md := body

		md = set.Markdown(md)
		if row {
			md += l.stopRow()
		}
		tries = append(tries, liveTry{rm: telegram.RichMessage{Markdown: md, IsRTL: rtl}, kb: kb})
	}
	tries = append(tries, liveTry{rm: telegram.RichMessage{Markdown: full, IsRTL: rtl}, kb: kb})
	if row {
		tries = append(tries, liveTry{rm: telegram.RichMessage{Markdown: body, IsRTL: rtl}, kb: l.b.stopKeyboard(l.lng, l.stopData), noRow: true})
	}

	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var (
		sent *telegram.Message
		err  error
	)
	for _, t := range tries {
		if id != 0 {
			err = l.b.api.EditRichMessage(sctx, l.chatID, id, t.rm, t.kb)
		} else {
			sent, err = l.b.api.SendRichMessage(sctx, l.chatID, t.rm, telegram.SendOptions{ReplyTo: l.replyTo, Silent: true, Markup: t.kb})
		}
		if err == nil {
			if t.noRow {
				log.Printf("buttons inside rich messages are not available; the stop button goes under the live message")
				l.b.stopRowOff.Store(true)
			}
			break
		}
		if !telegram.IsBadRequest(err) || messageGone(err) || sctx.Err() != nil {
			break
		}
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	if id != 0 {
		switch {
		case err == nil:
			l.shown, l.shownKB = body, withStop
		case messageGone(err):
			l.gone, l.traceMsg = true, 0
		}

		return
	}
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("[chat %d] live message: %v", l.chatID, err)
		}

		l.rich, l.dirty, l.lastEdit = false, true, time.Time{}
		return
	}
	l.shown, l.shownKB = body, withStop
	select {
	case <-l.done:

		go func() {
			dctx, cancel := l.b.ctx(10 * time.Second)
			defer cancel()
			_ = l.b.api.DeleteMessage(dctx, l.chatID, sent.MessageID)
		}()
	default:
		l.traceMsg = sent.MessageID
	}
}

func messageGone(err error) bool {
	return telegram.IsBadRequest(err) && strings.Contains(strings.ToLower(err.Error()), "not found")
}

func (l *liveView) replace(ctx context.Context, rm telegram.RichMessage, kb *telegram.InlineKeyboardMarkup) error {
	l.stop()
	l.mu.Lock()
	id := l.traceMsg
	ok := l.rich && !l.gone && id != 0
	l.mu.Unlock()
	if !ok {
		return errNoLiveMessage
	}
	if kb == nil {
		kb = &telegram.InlineKeyboardMarkup{InlineKeyboard: [][]telegram.InlineKeyboardButton{}}
	}
	if err := l.b.api.EditRichMessage(ctx, l.chatID, id, rm, kb); err != nil {
		return err
	}
	l.mu.Lock()
	if l.traceMsg == id {
		l.traceMsg = 0
	}
	l.mu.Unlock()
	return nil
}

var errNoLiveMessage = errors.New("no live message")

func (l *liveView) replaceHTML(ctx context.Context, html string, rtl bool, kb *telegram.InlineKeyboardMarkup) error {
	if set := l.b.premium(); set != nil {
		if fancy := set.HTML(html); fancy != html && l.replace(ctx, telegram.RichMessage{HTML: fancy, IsRTL: rtl}, kb) == nil {
			return nil
		}
	}
	return l.replace(ctx, telegram.RichMessage{HTML: html, IsRTL: rtl}, kb)
}

func (l *liveView) inPlace() func(ctx context.Context, md string) error {
	l.mu.Lock()
	ok := l.rich && !l.gone && l.traceMsg != 0 && !l.detached
	l.mu.Unlock()
	if !ok {
		return nil
	}
	return func(ctx context.Context, md string) error {
		rtl := telegram.IsMostlyRTL(md)
		if set := l.b.premium(); set != nil {
			if err := l.replace(ctx, telegram.RichMessage{Markdown: set.Markdown(md), IsRTL: rtl}, nil); err == nil {
				return nil
			}
		}
		return l.replace(ctx, telegram.RichMessage{Markdown: md, IsRTL: rtl}, nil)
	}
}

func (l *liveView) streams(partial string) bool {
	l.mu.Lock()
	from := streamFromFirst
	if l.worked {
		from = streamFrom
	}
	l.mu.Unlock()
	return utf8.RuneCountInString(partial) >= from
}
