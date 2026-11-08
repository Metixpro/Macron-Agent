package main

import (
	"context"
	"fmt"

	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/store"
)

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

func (b *Bot) saveContextCarry(ctx context.Context, uid int64, tokens int, mc *memory.Context) {
	if mc == nil || tokens <= 0 {
		return
	}
	_ = b.st.SetUserPref(ctx, uid, store.PrefContextMeter, fmt.Sprintf("%d/%d", tokens, mc.Compressions))
}
