package main

import (
	"context"
	"fmt"

	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/store"
)

// The context meter of the live view belongs to the conversation: each
// request goes on from where the last one left it, and only a compaction
// (announced on the card) lowers it. It is kept with the user's preferences,
// so a restart does not reset it either.

// contextCarry is where the meter stood after the user's last request, and
// whether the conversation was compacted since (0 after a reset: nothing to carry).
func (b *Bot) contextCarry(ctx context.Context, uid int64, mc *memory.Context) (tokens int, compacted bool) {
	if mc == nil || (mc.Messages == 0 && mc.Summary == "") {
		return 0, false
	}
	v, err := b.st.UserPref(ctx, uid, store.PrefContextMeter)
	if err != nil || v == "" {
		return 0, false
	}
	var compressions int
	if n, _ := fmt.Sscanf(v, "%d/%d", &tokens, &compressions); n != 2 || tokens < 0 {
		return 0, false
	}
	return tokens, mc.Compressions > compressions
}

// saveContextCarry keeps where the meter stands now.
func (b *Bot) saveContextCarry(ctx context.Context, uid int64, tokens int, mc *memory.Context) {
	if mc == nil || tokens <= 0 {
		return
	}
	_ = b.st.SetUserPref(ctx, uid, store.PrefContextMeter, fmt.Sprintf("%d/%d", tokens, mc.Compressions))
}
