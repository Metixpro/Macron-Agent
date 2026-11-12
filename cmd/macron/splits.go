package main

import (
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	splitNear  = 3800
	splitParts = 12
)

var splitWait = 1500 * time.Millisecond

type splitKey struct{ chat, user int64 }

type splitBuf struct {
	msg   *telegram.Message
	parts int
	timer *time.Timer
}

type splitCollector struct {
	mu   sync.Mutex
	bufs map[splitKey]*splitBuf
}

func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

func (c *splitCollector) take(m *telegram.Message, wg *sync.WaitGroup, fire func(*telegram.Message)) bool {
	if m.SplitDone || m.From == nil {
		return false
	}
	key := splitKey{m.Chat.ID, m.From.ID}
	text := m.Text != "" && m.MediaGroupID == "" && m.EditDate == 0
	long := text && utf16Len(m.Text) >= splitNear
	c.mu.Lock()
	defer c.mu.Unlock()
	buf := c.bufs[key]
	switch {
	case buf == nil && !long:
		return false
	case buf != nil && !text:
		c.flushAfter(key, buf, 0, wg, fire)
		return false
	case buf == nil:
		if c.bufs == nil {
			c.bufs = map[splitKey]*splitBuf{}
		}
		first := *m
		first.Entities = append([]telegram.MessageEntity(nil), m.Entities...)
		buf = &splitBuf{msg: &first, parts: 1}
		c.bufs[key] = buf
	default:
		off := utf16Len(buf.msg.Text) + 1
		buf.msg.Text += "\n" + m.Text
		for _, e := range m.Entities {
			e.Offset += off
			buf.msg.Entities = append(buf.msg.Entities, e)
		}
		buf.parts++
	}
	wait := splitWait
	if !long || buf.parts >= splitParts {
		wait = 0
	}
	c.flushAfter(key, buf, wait, wg, fire)
	return true
}

func (c *splitCollector) flushAfter(key splitKey, buf *splitBuf, wait time.Duration, wg *sync.WaitGroup, fire func(*telegram.Message)) {
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
		c.mu.Unlock()
		defer guard.Recover("a long message")
		buf.msg.SplitDone = true
		fire(buf.msg)
	})
}
