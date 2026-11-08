package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
)

// modelDownAlerts keeps the owners from being told of the same outage over
// and over: once in ten minutes.
var modelDownAlerts = guard.NewRateLimiter(1, 10*time.Minute)

// alertModelDown tells the owners (in the log chat, else themselves) that a
// request failed because no model answered, with what each provider last said:
// an outage that only the users see is one nobody fixes. It never blocks the
// request that failed.
func (b *Bot) alertModelDown(uid int64, err error) {
	if ok, _ := modelDownAlerts.Allow("all"); !ok {
		return
	}
	go func() {
		ctx, cancel := b.ctx(15 * time.Second)
		defer cancel()
		l := b.lang(b.firstOwner())
		var lines []string
		for _, p := range b.prov.Snapshot() {
			if !p.Enabled {
				continue
			}
			state := "✅"
			if p.Cooling {
				state = "⏳"
			}
			why := ""
			if p.LastErrorAt != nil && (p.LastOKAt == nil || p.LastErrorAt.After(*p.LastOKAt)) && time.Since(*p.LastErrorAt) < 15*time.Minute {
				state, why = "❌", " — <code>"+esc(shortText(p.LastError, 120))+"</code>"
			}
			lines = append(lines, fmt.Sprintf("%s %s%s", state, esc(p.Name), why))
		}
		b.notifyLog(ctx, l.f("⚠️ <b>مدل جواب نمی‌ده</b>\nدرخواست کاربر <code>%d</code> بی‌جواب موند: <code>%s</code>\n\n%s\n\nکاربرها «الان به مغزم دسترسی ندارم» می‌بینن. هر بار که کاربری «دوباره امتحان کن» رو بزنه، همه پروایدرها از اول امتحان می‌شن.",
			uid, esc(shortText(errText(err), 200)), strings.Join(lines, "\n")))
	}()
}

// errText is an error for a log line: the provider's own words first.
func errText(err error) string {
	var api *llm.APIError
	if errors.As(err, &api) {
		return fmt.Sprintf("HTTP %d: %s", api.Status, strings.TrimSpace(api.Body))
	}
	return err.Error()
}
