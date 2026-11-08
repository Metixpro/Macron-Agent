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

const settingContextWindow = "context_window"

func contextWindow(st *store.Store, prov *providers.Manager, env int) func() int {
	return func() int {
		p, ok := prov.Active()
		return providerWindow(st, p, ok, env)
	}
}

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

func historyBudget(st *store.Store, window int) int {
	fill := agent.FillHistoryTokens(window)
	n := st.SettingInt(store.SettingHistoryTokens, 0)
	if n <= 0 {
		return fill
	}
	return min(max(n, memory.MinBudget), fill)
}

func (b *Bot) historyTokens() int {
	return historyBudget(b.st, b.contextWindowNow())
}

func (b *Bot) contextWindowNow() int {
	if w := b.app.Agent.ContextWindow; w != nil {
		if n := w(); n > 0 {
			return n
		}
	}
	return agent.DefaultContextWindow
}
