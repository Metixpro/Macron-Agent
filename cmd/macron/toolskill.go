package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/store"
)

const (
	settingToolSkill = "provider_tools:"
	toolModeAuto     = ""
	toolModeFull     = "full"
	toolModeFocus    = "focus"
	skillMinCalls    = 12
	skillWeakShare   = 0.2
	skillWindow      = 400
)

type toolSkill struct {
	Mode     string `json:"mode,omitempty"`
	Calls    int    `json:"calls"`
	Problems int    `json:"problems"`
}

func (s toolSkill) weak() bool {
	return s.Calls >= skillMinCalls && float64(s.Problems) >= skillWeakShare*float64(s.Calls)
}

func (s toolSkill) focus() bool { return s.Mode == toolModeFocus }

func (s toolSkill) clean() int {
	if s.Calls == 0 {
		return 100
	}
	return max(0, 100-s.Problems*100/s.Calls)
}

func (b *Bot) toolSkill(id int64) toolSkill {
	var s toolSkill
	if v, ok := b.st.Setting(fmt.Sprint(settingToolSkill, id)); ok {
		_ = json.Unmarshal([]byte(v), &s)
	}
	return s
}

func (b *Bot) saveToolSkill(ctx context.Context, id int64, s toolSkill) {
	raw, _ := json.Marshal(s)
	if err := b.st.SetSetting(ctx, fmt.Sprint(settingToolSkill, id), string(raw)); err != nil {
		log.Printf("tool skill of provider %d: %v", id, err)
	}
}

func (b *Bot) focusTools(ctx context.Context) bool {
	p, ok := b.prov.ActiveFor(ctx)
	return ok && b.toolSkill(p.ID).focus()
}

func (b *Bot) learnToolSkill(ctx context.Context, q map[string]agent.CallQuality, custom bool) {
	if len(q) == 0 || custom {
		return
	}
	ids := map[string]int64{}
	for _, s := range b.prov.Snapshot() {
		ids[s.Name] = s.ID
	}
	b.skillMu.Lock()
	defer b.skillMu.Unlock()
	for name, cq := range q {
		id, ok := ids[name]
		if !ok || cq.Calls+cq.Stalled == 0 {
			continue
		}
		s := b.toolSkill(id)
		was := s.weak()
		s.Calls += cq.Calls + cq.Stalled
		s.Problems += cq.Problems()
		for s.Calls > skillWindow {
			s.Calls, s.Problems = s.Calls/2, s.Problems/2
		}
		b.saveToolSkill(ctx, id, s)
		if s.weak() && !was {
			log.Printf("tools: %s calls tools %d%% right over %d calls (it keeps every tool; an admin may focus them on its card)", name, s.clean(), s.Calls)
		}
	}
}

func (b *Bot) toolModeLine(l lang, p store.Provider) string {
	s := b.toolSkill(p.ID)
	mode := map[string]string{toolModeFull: l.t("همه ابزارها"), toolModeFocus: l.t("ابزارهای متمرکز")}[s.Mode]
	if s.Mode == toolModeAuto {
		mode = l.t("خودکار: همه ابزارها")
	}
	line := l.f("\n🧰 ابزارها: <b>%s</b>", mode)
	if s.Calls > 0 {
		line += l.f(" · %s٪ فراخوانی درست از %s", l.num(s.clean()), l.num(s.Calls))
	} else {
		line += l.t(" · هنوز فراخوانی‌ای ثبت نشده")
	}
	return line
}

func nextToolMode(m string) string {
	switch m {
	case toolModeAuto:
		return toolModeFull
	case toolModeFull:
		return toolModeFocus
	}
	return toolModeAuto
}
