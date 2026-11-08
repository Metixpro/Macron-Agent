package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (l *liveView) richLabel(text string, premium bool) any {
	if !premium {
		return text
	}
	icon, rest, ok := strings.Cut(text, " ")
	if !ok {
		return text
	}
	if set := l.b.premium(); set != nil {
		if id, found := set.ID(icon); found {
			return []any{telegram.RichTextSpan{Type: "custom_emoji", CustomEmojiID: id, AlternativeText: icon}, " " + rest}
		}
	}
	return text
}

func (l *liveView) privateRichCard(now time.Time, premium bool) telegram.RichMessage {
	if l.text != "" {
		body, rtl := l.groupBody()
		if premium {
			if set := l.b.premium(); set != nil {
				body = set.Markdown(body)
			}
		}
		return telegram.RichMessage{Markdown: body, IsRTL: rtl}
	}
	blocks := []telegram.InputRichBlock{
		{Type: "heading", Size: 4, Text: l.richLabel("💭 "+l.lng.t("مکرون ایجنت"), premium)},
		{Type: "paragraph", Text: l.richLabel(l.status, premium)},
	}
	if l.detached && l.tr.title != "" {
		blocks = append(blocks, telegram.InputRichBlock{Type: "paragraph", Text: l.lng.f("کار پس‌زمینه: %s", l.tr.title)})
	}
	if len(l.tr.plan) > 0 {
		done := 0
		items := make([]telegram.InputRichListItem, 0, len(l.tr.plan))
		for _, it := range l.tr.plan {
			var text any = it.Text
			if it.Status == agent.PlanDone {
				done++
			}
			if it.Status == agent.PlanInProgress {
				text = telegram.RichTextSpan{Type: "bold", Text: "⏳ " + it.Text}
			}
			items = append(items, telegram.InputRichListItem{HasCheckbox: true, IsChecked: it.Status == agent.PlanDone, Blocks: []telegram.InputRichBlock{{Type: "paragraph", Text: text}}})
		}
		blocks = append(blocks, telegram.InputRichBlock{Type: "paragraph", Text: telegram.RichTextSpan{Type: "bold", Text: l.lng.f("طرح کار · %s/%s", l.lng.num(done), l.lng.num(len(items)))}}, telegram.InputRichBlock{Type: "list", Items: items})
	}
	if l.tracing {
		_, body := l.tr.card(now, l.started, false)
		lines := strings.Split(body, "\n")
		if len(lines) > 0 && lines[0] == traceRule {
			lines = lines[1:]
		}
		if len(lines) > 0 && strings.HasPrefix(lines[0], "ctx ") {
			lines = lines[1:]
		}
		code := strings.TrimSpace(strings.Join(lines, "\n"))
		if code != "" {
			blocks = append(blocks, telegram.InputRichBlock{Type: "details", Summary: l.lng.f("مراحل اجرا · %s", l.lng.num(l.tr.calls)), IsOpen: len(lines) <= 8, Blocks: []telegram.InputRichBlock{{Type: "pre", Language: "bash", Text: code}}})
		}
	} else if l.noting {
		steps := l.think.steps
		if len(steps) > thinkShown {
			steps = steps[len(steps)-thinkShown:]
		}
		items := make([]telegram.InputRichListItem, 0, len(steps))
		for _, s := range steps {
			mark := "✓ "
			if s.active {
				mark = "⏳ "
			} else if s.failed {
				mark = "⚠️ "
			}
			items = append(items, telegram.InputRichListItem{Blocks: []telegram.InputRichBlock{{Type: "paragraph", Text: mark + s.text}}})
		}
		blocks = append(blocks, telegram.InputRichBlock{Type: "list", Items: items})
	}
	elapsed := max(time.Duration(0), now.Sub(l.started))
	footer := fmt.Sprintf("%d:%02d", int(elapsed.Minutes()), int(elapsed.Seconds())%60)
	if l.tr.calls > 0 {
		footer += " · " + l.lng.f("عملیات: %s", l.lng.num(l.tr.calls))
	}
	if l.tr.ctxWindow > 0 && l.tr.ctxUsed > 0 {
		footer += " · context " + agent.HumanTokens(l.tr.ctxUsed) + " / " + agent.HumanTokens(l.tr.ctxWindow)
	}
	blocks = append(blocks, telegram.InputRichBlock{Type: "footer", Text: footer})
	return telegram.RichMessage{Blocks: blocks, IsRTL: l.lng == fa, SkipEntityDetection: true}
}

func (l *liveView) flushPrivateRich(ctx context.Context) {
	l.mu.Lock()
	now := time.Now()
	if l.gone || (!l.dirty && l.traceMsg != 0) || (!l.urgent && !l.lastEdit.IsZero() && now.Sub(l.lastEdit) < l.editEvery()) {
		l.mu.Unlock()
		return
	}
	id := l.traceMsg
	frame := l.privateRichCard(now, !l.richPlain)
	plain := l.privateRichCard(now, false)
	key, _ := json.Marshal(frame)
	withStop := l.stopData != "" && (l.canStop || l.detached)
	if id != 0 && l.shown == string(key) && l.shownKB == withStop {
		l.dirty, l.urgent = false, false
		l.mu.Unlock()
		return
	}
	l.dirty, l.urgent, l.lastEdit, l.lastFlush = false, false, now, now
	stopData := l.stopData
	l.mu.Unlock()
	var kb *telegram.InlineKeyboardMarkup
	if withStop {
		kb = l.b.stopKeyboard(l.lng, stopData)
	}
	sctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	send := func(rm telegram.RichMessage) error {
		if id != 0 {
			return l.b.api.EditRichMessage(sctx, l.chatID, id, rm, kb)
		}
		sent, err := l.b.api.SendRichMessage(sctx, l.chatID, rm, telegram.SendOptions{ReplyTo: l.replyTo, Silent: true, Markup: kb})
		if err == nil {
			id = sent.MessageID
		}
		return err
	}
	plainUsed := false
	err := send(frame)
	if err != nil && telegram.IsBadRequest(err) && !telegram.IsUnsupported(err) && !messageGone(err) && sctx.Err() == nil {
		err = send(plain)
		plainUsed = err == nil
	}
	l.mu.Lock()
	if err == nil {
		if plainUsed {
			l.richPlain = true
			key, _ = json.Marshal(plain)
		}
		l.traceMsg, l.shown, l.shownKB = id, string(key), withStop
		l.mu.Unlock()
		return
	}
	l.dirty = true
	if id != 0 && messageGone(err) {
		l.gone = true
		l.traceMsg = 0
	}
	fallback := telegram.IsUnsupported(err) || telegram.IsBadRequest(err) && !messageGone(err)
	if fallback {
		l.rich = false
		l.lastEdit = time.Time{}
	}
	l.mu.Unlock()
	if fallback && ctx.Err() == nil {
		l.flushPrivateMessage(ctx)
	}
}
