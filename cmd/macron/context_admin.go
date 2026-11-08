package main

import (
	"strconv"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// The model's context window: the provider's own (its view in the panel),
// else the panel's (⚙️ → 🪟), else WAZE_CONTEXT_WINDOW, else guessed from the
// provider's model name. A user's tier may set a smaller one (tiers.go).

const settingContextWindow = "context_window"

// contextWindow is the window for the next request.
func contextWindow(st *store.Store, prov *providers.Manager, env int) func() int {
	return func() int {
		p, ok := prov.Active()
		return providerWindow(st, p, ok, env)
	}
}

// providerWindow is the window of a provider (ok: there is one).
func providerWindow(st *store.Store, p store.Provider, ok bool, env int) int {
	if ok && p.ContextWindow > 0 {
		return p.ContextWindow
	}
	if n := st.SettingInt(settingContextWindow, 0); n > 0 {
		return n
	}
	if env > 0 {
		return env
	}
	if ok {
		return agent.ContextWindow(p.Model)
	}
	return agent.DefaultContextWindow
}

// parseTokens reads "256000", "256k", "1m" or "۲۵۶k" (0 = automatic).
func parseTokens(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(textutil.Fold(s)))
	s = strings.NewReplacer(",", "", "_", "", " ", "", "٬", "").Replace(s)
	mult := 1
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1000000, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 {
		return 0, false
	}
	n := int(f * float64(mult))
	if n != 0 && (n < 8000 || n > 10000000) {
		return 0, false
	}
	return n, true
}

// historyBudget is how much of the latest conversation a request carries word
// for word; the memory folds what is older into its summary. The panel's 🧠
// is automatic (0) by default: the conversation fills the model's window up
// to where a compaction starts, then half of it is folded. An admin may set
// a smaller number to spend fewer tokens.
func historyBudget(st *store.Store, window int) int {
	fill := agent.FillHistoryTokens(window)
	n := st.SettingInt(store.SettingHistoryTokens, 0)
	if n <= 0 { // automatic: word for word until the window fills
		return fill
	}
	return min(max(n, memory.MinBudget), fill)
}

// historyTokens is historyBudget for the model in use.
func (b *Bot) historyTokens() int {
	return historyBudget(b.st, b.contextWindowNow())
}

// contextWindowNow is the window of the model in use.
func (b *Bot) contextWindowNow() int {
	if w := b.app.Agent.ContextWindow; w != nil {
		if n := w(); n > 0 {
			return n
		}
	}
	return agent.DefaultContextWindow
}
