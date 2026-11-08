package main

import (
	"context"

	"github.com/Metixpro/macron-agent/pkg/providers"
)

type tierID string

const tierOwner tierID = "owner"

const (
	settingOwnerProvider = "owner_provider"
	settingOwnerContext  = "owner_context"
)

type tierConf struct {
	id       tierID
	provider int64
	context  int
}

func (b *Bot) tier(id tierID) tierConf {
	return tierConf{id: id, provider: int64(b.st.SettingInt(settingOwnerProvider, 0)), context: b.st.SettingInt(settingOwnerContext, 0)}
}

type standing struct {
	tier tierID
}

func (b *Bot) route(ctx context.Context, id tierID) context.Context {
	return providers.Prefer(ctx, b.tier(id).provider)
}

func (b *Bot) tierWindow(conf tierConf) int {
	p, ok := b.prov.ActiveFor(providers.Prefer(context.Background(), conf.provider))
	window := providerWindow(b.st, p, ok, b.envWindow)
	if conf.context > 0 {
		window = conf.context
		if ok && p.ContextWindow > 0 {
			window = min(window, p.ContextWindow)
		}
	}
	return window
}

func (b *Bot) windowOf(st standing) int { return b.tierWindow(b.tier(st.tier)) }

func (b *Bot) historyFor(context.Context, int64) int {
	return historyBudget(b.st, b.windowOf(standing{tier: tierOwner}))
}

func parseTier(s string) (tierID, bool) { return tierOwner, s == string(tierOwner) }
