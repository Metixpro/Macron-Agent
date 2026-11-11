package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolGenerateBriefing = "generate_briefing"

// GenerateMorningBriefing compiles a comprehensive daily morning briefing.
func (r *Registry) GenerateMorningBriefing(ctx context.Context, sessionID string) (string, error) {
	now := time.Now()
	jalaliDate := textutil.JalaliDateFull(now)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("🌅 **گزارش صبحگاهی روزانه (Morning Briefing)**\n📅 **%s**\n\n", jalaliDate))

	// 1. Market rates
	quotes, _ := r.fetchMarketQuotes(ctx)
	sb.WriteString("💰 **خلاصه بازار ارز و طلا:**\n")
	if usd, ok := quotes["usd"]; ok && usd > 0 {
		sb.WriteString(fmt.Sprintf("- دلار آمریکا: **%.0f تومان**\n", usd))
	}
	if gold, ok := quotes["gold18"]; ok && gold > 0 {
		sb.WriteString(fmt.Sprintf("- طلای ۱۸ عیار: **%.0f تومان** (هر گرم)\n", gold))
	}
	if emami, ok := quotes["emami"]; ok && emami > 0 {
		sb.WriteString(fmt.Sprintf("- سکه امامی: **%.0f تومان**\n", emami))
	}
	if btc, ok := quotes["btc"]; ok && btc > 0 {
		sb.WriteString(fmt.Sprintf("- بیت‌کوین: **$%.0f**\n", btc))
	}
	sb.WriteString("\n")

	// 2. TSETMC stock overview
	tse, err := r.QueryTSETMC(ctx, "شاخص")
	if err == nil && tse != nil {
		sb.WriteString(fmt.Sprintf("📈 **بورس تهران:** شاخص کل **%.0f** (%.1f%% تغییر)\n\n",
			tse.Index, tse.IndexPercent))
	}

	// 3. Active price alerts or watches
	alerts, _ := r.ListPriceAlerts(sessionID)
	if len(alerts) > 0 {
		sb.WriteString(fmt.Sprintf("🔔 **هشدارهای قیمت فعال:** %d مورد در حال پایش\n\n", len(alerts)))
	}

	sb.WriteString("✨ روز کاری پربرکتی داشته باشید!")
	return sb.String(), nil
}
