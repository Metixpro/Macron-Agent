package main

import (
	"fmt"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	queueWait          = 10 * time.Minute
	scheduledQueueWait = 2 * time.Minute
)

func (b *Bot) acquireQueued(key string, resp *responder, l lang) (func(), guard.Reason) {
	release, why := b.gate.TryAcquire(key)
	for i := 0; why == guard.KeyBusy && i < 6; i++ {
		select {
		case <-time.After(500 * time.Millisecond):
		case <-b.jobCtx.Done():
			return release, why
		}
		release, why = b.gate.TryAcquire(key)
	}
	if why != guard.AtCapacity {
		return release, why
	}
	var notice *telegram.Message
	var once sync.Once
	queued := func(ahead int) {
		g := b.guestReply(resp.guestID)
		if resp.guestID != "" && g == nil {
			return
		}
		once.Do(func() {
			ctx, cancel := b.ctx(10 * time.Second)
			defer cancel()
			html := l.f("⏳ الان سرم شلوغه و توی صف هستی (%s نفر جلوتر از تو). به‌محض اینکه جا باز بشه، شروع می‌کنم.", l.num(ahead))
			if g != nil {
				g.progress(ctx, html, l == fa, true)
				return
			}
			notice = b.sendHTML(ctx, resp.m.Chat.ID, html, telegram.SendOptions{ReplyTo: resp.m.MessageID, Silent: true})
		})
	}
	release, why = b.gate.Acquire(b.jobCtx, key, guard.Normal, queueWait, queued)
	if notice != nil {
		ctx, cancel := b.ctx(10 * time.Second)
		_ = b.api.DeleteMessage(ctx, notice.Chat.ID, notice.MessageID)
		cancel()
	}
	return release, why
}

func (b *Bot) acquireScheduled(key string) (func(), error) {
	release, why := b.gate.Acquire(b.jobCtx, key, guard.Normal, scheduledQueueWait, nil)
	if why != guard.Acquired {
		return nil, fmt.Errorf("no free slot: %v", why)
	}
	return release, nil
}

func (b *Bot) filesTTL() time.Duration { return b.workFiles }

func durationWord(l lang, d time.Duration) string {
	if d <= 0 {
		return l.t("همیشه")
	}
	if h := int(d.Hours()); h%24 == 0 && h >= 48 {
		return l.f("%s روز", l.num(h/24))
	}
	return l.f("%s ساعت", l.num(max(int(d.Hours()), 1)))
}

func keptFor(l lang, d time.Duration) string {
	if d <= 0 {
		return l.t("همیشه نگه داشته می‌شن")
	}
	return l.f("تا %s نگه داشته می‌شن", durationWord(l, d))
}
