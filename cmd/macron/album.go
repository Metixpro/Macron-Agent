package main

import (
	"context"
	"fmt"
	"log"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

var (
	albumQuiet = 1200 * time.Millisecond
	albumMax   = 6 * time.Second
)

const (
	albumItems    = 10
	albumImagesIn = 8
)

type albumKey struct {
	chat  int64
	group string
}

type albumBuf struct {
	items []*telegram.Message
	first time.Time
	timer *time.Timer
}

type albumCollector struct {
	mu   sync.Mutex
	bufs map[albumKey]*albumBuf
}

func (c *albumCollector) add(m *telegram.Message, wg *sync.WaitGroup, fire func([]*telegram.Message)) {
	key := albumKey{m.Chat.ID, m.MediaGroupID}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.bufs == nil {
		c.bufs = map[albumKey]*albumBuf{}
	}
	buf := c.bufs[key]
	if buf == nil {
		buf = &albumBuf{first: time.Now()}
		c.bufs[key] = buf
	}
	buf.items = append(buf.items, m)
	wait := albumQuiet
	if rest := albumMax - time.Since(buf.first); rest < wait {
		wait = max(rest, 0)
	}
	if len(buf.items) >= albumItems {
		wait = 0
	}
	if buf.timer != nil && buf.timer.Stop() {
		wg.Done()
	}
	wg.Add(1)
	buf.timer = time.AfterFunc(wait, func() {
		defer wg.Done()
		c.mu.Lock()
		if c.bufs[key] != buf {
			c.mu.Unlock()
			return
		}
		delete(c.bufs, key)
		items := buf.items
		c.mu.Unlock()
		defer func() {
			if p := recover(); p != nil {
				log.Printf("panic while handling an album: %v\n%s", p, debug.Stack())
			}
		}()
		fire(items)
	})
}

func mergeAlbum(items []*telegram.Message) *telegram.Message {
	items = append([]*telegram.Message(nil), items...)
	sort.SliceStable(items, func(i, j int) bool { return items[i].MessageID < items[j].MessageID })
	lead := 0
	for i, it := range items {
		if strings.TrimSpace(it.Caption) != "" {
			lead = i
			break
		}
	}
	merged := *items[lead]
	for i, it := range items {
		if i != lead {
			merged.Album = append(merged.Album, it)
			if merged.ReplyToMessage == nil && merged.ExternalReply == nil && (it.ReplyToMessage != nil || it.ExternalReply != nil) {
				merged.ReplyToMessage, merged.ExternalReply, merged.Quote = it.ReplyToMessage, it.ExternalReply, it.Quote
			}
		}
	}
	merged.AlbumDone = true
	return &merged
}

func (b *Bot) attachAll(ctx context.Context, session string, m *telegram.Message, vision bool, lead string, fresh bool) (*attachment, error) {
	if len(m.Album) == 0 {
		return b.attach(ctx, session, m, vision, lead, fresh)
	}
	l := b.langOf(m.From)
	items := append([]*telegram.Message{m}, m.Album...)
	var parts []*attachment
	var firstErr error
	var failed []string
	for i, it := range items {
		a, err := b.attach(ctx, session, it, vision, fmt.Sprintf("Item %d of %d of the album is", i+1, len(items)), fresh)
		switch {
		case err != nil:
			if firstErr == nil {
				firstErr = err
			}
			failed = append(failed, fmt.Sprintf("item %d could not be opened (%v)", i+1, err))
		case a != nil:
			parts = append(parts, a)
		}
	}
	if len(parts) == 0 {
		return nil, firstErr
	}
	out := &attachment{placeholder: l.f("(یه آلبوم با %d فایل فرستاد)", len(items))}
	lines := []string{fmt.Sprintf("The user sent %d files together as one album (one message); what they wrote with it is the request for all of them.", len(items))}
	for _, p := range parts {
		lines = append(lines, p.about)
		if p.note != "" {
			out.note += p.note + " "
		}
		for _, img := range p.images {
			if len(out.images) < albumImagesIn {
				out.images = append(out.images, img)
			}
		}
	}
	if len(failed) > 0 {
		lines = append(lines, "("+strings.Join(failed, "; ")+")")
	}
	out.about = strings.Join(lines, "\n")
	out.note = strings.TrimSpace(out.note)
	return out, nil
}
