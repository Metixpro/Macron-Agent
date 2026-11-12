package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	settingLateAnswers = "late_answers"
	lateMax            = 50
	lateKeep           = 24 * time.Hour
	lateEvery          = 30 * time.Second
)

var sendRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 15 * time.Second, 30 * time.Second}

type lateReply struct {
	ID     int64  `json:"id"`
	Chat   int64  `json:"chat"`
	Thread int64  `json:"thread,omitempty"`
	Reply  int64  `json:"reply,omitempty"`
	Text   string `json:"text"`
	Mark   string `json:"mark"`
	At     int64  `json:"at"`
}

type lateAnswers struct {
	mu     sync.Mutex
	items  []lateReply
	nextID int64
	wake   chan struct{}
	loaded sync.Once
}

func (o *lateAnswers) load(b *Bot) {
	o.loaded.Do(func() {
		o.wake = make(chan struct{}, 1)
		raw, ok := b.st.Setting(settingLateAnswers)
		if !ok || strings.TrimSpace(raw) == "" {
			return
		}
		var kept []lateReply
		if json.Unmarshal([]byte(raw), &kept) != nil || len(kept) == 0 {
			return
		}
		o.mu.Lock()
		for _, r := range kept {
			o.nextID = max(o.nextID, r.ID)
		}
		o.items = append(kept, o.items...)
		o.mu.Unlock()
		log.Printf("📮 %d answers were not delivered before the restart; sending them now", len(kept))
	})
}

func retryableSend(err error) bool {
	if err == nil || telegram.IsUnreachable(err) {
		return false
	}
	var api *telegram.APIError
	if errors.As(err, &api) {
		return api.Code == http.StatusTooManyRequests || api.Code >= 500
	}
	return true
}

func netDown(err error) bool {
	var api *telegram.APIError
	if errors.As(err, &api) {
		return api.Code == http.StatusTooManyRequests
	}
	return err != nil
}

func retryWait(err error, d time.Duration) time.Duration {
	var api *telegram.APIError
	if errors.As(err, &api) && api.RetryAfter > 0 {
		return time.Duration(api.RetryAfter)*time.Second + 500*time.Millisecond
	}
	return d
}

func (b *Bot) sendSure(ctx context.Context, m *telegram.Message, md string, opts telegram.SendOptions) error {
	err := b.sendRich(ctx, m.Chat.ID, md, opts)
	for _, d := range sendRetryDelays {
		if err == nil || !retryableSend(err) || ctx.Err() != nil {
			break
		}
		log.Printf("[chat %d] sending the answer failed (%v); trying again", m.Chat.ID, err)
		select {
		case <-time.After(retryWait(err, d)):
		case <-ctx.Done():
		}
		if ctx.Err() != nil {
			break
		}
		err = b.sendRich(ctx, m.Chat.ID, md, opts)
	}
	if err != nil && retryableSend(err) {
		b.keepLate(lateReply{Chat: m.Chat.ID, Thread: opts.ThreadID, Reply: opts.ReplyTo, Text: md,
			Mark: b.langOf(m.From).t("♻️ این جواب دیر رسید، چون اتصال به تلگرام قطع بود.")})
	}
	return err
}

func (b *Bot) keepLate(r lateReply) {
	o := &b.late
	o.load(b)
	o.mu.Lock()
	o.nextID++
	r.ID, r.At = o.nextID, time.Now().Unix()
	o.items = append(o.items, r)
	if n := len(o.items); n > lateMax {
		o.items = o.items[n-lateMax:]
	}
	o.mu.Unlock()
	log.Printf("[chat %d] the answer could not be sent; it is kept and goes out once Telegram is reachable", r.Chat)
	b.saveLate()
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

func (b *Bot) saveLate() {
	o := &b.late
	o.mu.Lock()
	value := ""
	if len(o.items) > 0 {
		raw, _ := json.Marshal(o.items)
		value = string(raw)
	}
	o.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.st.SetSetting(ctx, settingLateAnswers, value); err != nil {
		log.Printf("saving the late answers: %v", err)
	}
}

func (b *Bot) runLate(ctx context.Context) {
	o := &b.late
	o.load(b)
	t := time.NewTicker(lateEvery)
	defer t.Stop()
	for {
		b.flushLate(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-o.wake:
			select {
			case <-time.After(lateEvery):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (b *Bot) flushLate(ctx context.Context) {
	o := &b.late
	o.mu.Lock()
	items := append([]lateReply(nil), o.items...)
	o.mu.Unlock()
	if len(items) == 0 {
		return
	}
	gone := map[int64]bool{}
	for _, r := range items {
		if ctx.Err() != nil {
			break
		}
		if time.Since(time.Unix(r.At, 0)) > lateKeep {
			log.Printf("[chat %d] a late answer is dropped: Telegram was out of reach for %s", r.Chat, lateKeep)
			gone[r.ID] = true
			continue
		}
		sctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		opts := telegram.SendOptions{ReplyTo: r.Reply, ThreadID: r.Thread}
		text := r.Mark + "\n\n" + r.Text
		err := b.sendRich(sctx, r.Chat, text, opts)
		if err != nil && !retryableSend(err) && opts.ReplyTo != 0 {
			opts.ReplyTo = 0
			err = b.sendRich(sctx, r.Chat, text, opts)
		}
		cancel()
		if err != nil && retryableSend(err) {
			break
		}
		if err != nil {
			log.Printf("[chat %d] a late answer is dropped: %v", r.Chat, err)
		} else {
			log.Printf("[chat %d] a late answer was delivered", r.Chat)
		}
		gone[r.ID] = true
		time.Sleep(350 * time.Millisecond)
	}
	if len(gone) == 0 {
		return
	}
	o.mu.Lock()
	left := o.items[:0]
	for _, r := range o.items {
		if !gone[r.ID] {
			left = append(left, r)
		}
	}
	o.items = left
	o.mu.Unlock()
	b.saveLate()
}

func (b *Bot) startLate(ctx context.Context) {
	go guard.KeepRunning(ctx, "the late answers", b.runLate)
}
