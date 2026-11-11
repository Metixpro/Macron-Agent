package tools

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolGenerateBriefing = "generate_briefing"

func (r *Registry) GenerateMorningBriefing(ctx context.Context, sessionID string) (string, error) {
	loc, err := config.Location()
	if err != nil {
		loc = time.Local
	}
	now := time.Now().In(loc)
	var (
		wg               sync.WaitGroup
		rates, tse, cryp string
		rateErr, tseErr  error
		cryptoErr        error
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		rates, rateErr = r.marketPrice(ctx, sessionID, map[string]any{"items": "usd,eur,aed,gold18,emami,bahar"})
	}()
	go func() { defer wg.Done(); tse, tseErr = r.QueryTSETMC(ctx, "") }()
	go func() {
		defer wg.Done()
		cryp, cryptoErr = r.cryptoPrice(ctx, sessionID, map[string]any{"symbols": "BTC,ETH"})
	}()
	wg.Wait()
	var sb strings.Builder
	fmt.Fprintf(&sb, "Briefing data for %s (%s, %s). Write the briefing from it; leave out what is unavailable.\n", textutil.JalaliDateFull(now), now.Format("2006-01-02"), now.Format("15:04 MST"))
	section := func(title, body string, err error) {
		switch {
		case err != nil:
			fmt.Fprintf(&sb, "\n[%s] unavailable: %v\n", title, err)
		case strings.HasPrefix(strings.TrimSpace(body), "error"):
			fmt.Fprintf(&sb, "\n[%s] unavailable: %s\n", title, strings.TrimSpace(body))
		case strings.TrimSpace(body) != "":
			fmt.Fprintf(&sb, "\n[%s]\n%s\n", title, strings.TrimSpace(body))
		}
	}
	section("rates (Toman)", rates, rateErr)
	section("crypto", cryp, cryptoErr)
	section("Tehran Stock Exchange", tse, tseErr)
	if alerts, err := r.ListPriceAlerts(sessionID); err == nil && len(alerts) > 0 {
		sb.WriteString("\n[price alerts]")
		for _, a := range alerts {
			state := "watching"
			if a.Triggered {
				state = "triggered"
			}
			fmt.Fprintf(&sb, "\n%s %s %s: %s", a.AssetFa, a.Operator, groupDigits(a.TargetPrice), state)
		}
		sb.WriteString("\n")
	}
	return strings.TrimSpace(sb.String()), nil
}
