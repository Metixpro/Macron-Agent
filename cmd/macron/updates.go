package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

const (
	settingLastUpdate = "tg_last_update"
	lastUpdateTrust   = 24 * time.Hour
)

type updateMark struct {
	mu sync.Mutex
	id int64
}

func (b *Bot) lastHandledUpdate() int64 {
	raw, ok := b.st.Setting(settingLastUpdate)
	if !ok {
		return 0
	}
	var botID, id, at int64
	if _, err := fmt.Sscanf(raw, "%d:%d:%d", &botID, &id, &at); err != nil || botID != b.me.ID || time.Since(time.Unix(at, 0)) > lastUpdateTrust {
		return 0
	}
	return id
}

func (b *Bot) markHandled(id int64) {
	b.handled.mu.Lock()
	defer b.handled.mu.Unlock()
	if id <= b.handled.id {
		return
	}
	b.handled.id = id
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := b.st.SetSetting(ctx, settingLastUpdate, fmt.Sprintf("%d:%d:%d", b.me.ID, id, time.Now().Unix())); err != nil {
		log.Printf("saving the last handled update: %v", err)
	}
}

func (b *Bot) confirmUpdates(offset int64) {
	if offset == 0 {
		return
	}
	b.markHandled(offset - 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := b.api.GetUpdates(ctx, offset, 0); err != nil {
		log.Printf("telling Telegram which updates were handled: %v", err)
	}
}
