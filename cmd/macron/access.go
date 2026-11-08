package main

import (
	"context"

	"github.com/Metixpro/macron-agent/pkg/providers"
)

// There is one owner and no plans, so no quota, price or tier: every request
// is the owner's, unlimited. What remains of the old tiers is the one place
// that says which provider and which context window a request has (the
// routing panel chooses a light and a strong model for it).

// tierID names where a request's settings live; there is only the owner's.
type tierID string

const tierOwner tierID = "owner"

const (
	settingOwnerProvider = "owner_provider" // the provider requests go to first (0: the panel's active one)
	settingOwnerContext  = "owner_context"  // a context window smaller than the model's own (0: the model's)
)

type tierConf struct {
	id       tierID
	provider int64 // 0: the panel's active provider
	context  int   // the context window in tokens (0 = the model's own)
}

func (b *Bot) tier(id tierID) tierConf {
	return tierConf{id: id, provider: int64(b.st.SettingInt(settingOwnerProvider, 0)), context: b.st.SettingInt(settingOwnerContext, 0)}
}

// standing is where a request stands.
type standing struct {
	tier tierID
}

// route sends the model calls made with ctx to the provider requests go to first.
func (b *Bot) route(ctx context.Context, id tierID) context.Context {
	return providers.Prefer(ctx, b.tier(id).provider)
}

// tierWindow is the context window of a request: the configured one (never
// more than its provider's own), else the provider's.
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

// windowOf is the context window of the owner's requests.
func (b *Bot) windowOf(st standing) int { return b.tierWindow(b.tier(st.tier)) }

// historyFor is the conversation budget: the window's.
func (b *Bot) historyFor(context.Context, int64) int {
	return historyBudget(b.st, b.windowOf(standing{tier: tierOwner}))
}

// parseTier reads the tier of a panel button.
func parseTier(s string) (tierID, bool) { return tierOwner, s == string(tierOwner) }
