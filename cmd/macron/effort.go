package main

import (
	"fmt"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

type effortLevel struct {
	key   string
	icon  string
	name  string
	meter string
	about string
}

var effortLevels = []effortLevel{
	{"", "⚙️", "خودکار", "▱▱▱▱", "پیش‌فرض خود مدل؛ چیزی فرستاده نمی‌شه"},
	{llm.EffortLow, "🟢", "Low", "▰▱▱▱", "سریع و کم‌هزینه: گپ و سؤال‌های کوتاه"},
	{llm.EffortMedium, "🟡", "Medium", "▰▰▱▱", "متعادل: بیشتر کارهای روزمره"},
	{llm.EffortHigh, "🔴", "High", "▰▰▰▱", "عمیق: تحلیل، کد و کارهای چندمرحله‌ای"},
	{llm.EffortUltra, "🚀", "Ultracode", "▰▰▰▰", "نهایت توان مدل برای پروژه‌های بزرگ کد و مسئله‌های سخت؛ هر کار دو برابر قدم و ۱٫۵ برابر وقت می‌گیره"},
}

func effortOf(key string) effortLevel {
	for _, e := range effortLevels {
		if e.key == key {
			return e
		}
	}
	return effortLevels[0]
}

func effortCode(key string) string {
	if key == "" {
		return "auto"
	}
	return key
}

func effortFromCode(code string) (string, bool) {
	if code == "auto" {
		return "", true
	}
	return code, code != "" && llm.ValidEffort(code)
}

func effortName(l lang, key string) string {
	e := effortOf(key)
	return e.icon + " " + l.t(e.name)
}

func effortBadge(key string) string {
	if key == "" {
		return ""
	}
	return " · " + effortOf(key).icon
}

func (b *Bot) viewEffort(l lang, p store.Provider, pick func(code string) string, back telegram.InlineKeyboardButton) panelView {
	var sb strings.Builder
	api := providers.API(p)
	fmt.Fprint(&sb, l.f("⚡ <b>Effort · %s</b>\n<code>%s</code> · %s\n\n", esc(p.Name), esc(shortText(p.Model, 60)), apiShort(api)))
	sb.WriteString(l.t("چقدر مدل قبل از جواب فکر کنه: هرچی بیشتر، جواب کارهای سخت بهتر و دقیق‌تر، ولی توکن و زمان بیشتر.\n"))
	for _, e := range effortLevels {
		mark := "▫️"
		if e.key == p.Effort {
			mark = "✅"
		}
		fmt.Fprintf(&sb, "\n%s %s <b>%s</b> <code>%s</code>\n      %s", mark, e.icon, esc(l.t(e.name)), e.meter, esc(l.t(e.about)))
	}
	param, value, support := llm.EffortWire(api, p.Model, p.Effort)
	sb.WriteString("\n\n")
	switch {
	case support == llm.EffortNo:
		sb.WriteString(l.t("⚠️ این مدل (از روی اسمش) effort قبول نمی‌کنه؛ چیزی فرستاده نمی‌شه و با پیش‌فرض خودش کار می‌کنه."))
	case value == "":
		fmt.Fprint(&sb, l.f("🔧 برای این مدل: پیش‌فرض خودش (<code>%s</code> فرستاده نمی‌شه).", param))
	default:
		fmt.Fprint(&sb, l.f("🔧 برای این مدل: <code>%s = %s</code>", param, value))
		if support == llm.EffortMaybe {
			sb.WriteString(l.t("\n❔ معلوم نیست این مدل قبولش کنه؛ اگه قبول نکنه، خودکار یه سطح پایین‌تر یا بدون اون می‌ره."))
		} else if p.Effort == llm.EffortUltra {
			sb.WriteString(l.t("\nاگه مدل این سطح رو نداشته باشه، خودکار بالاترین سطحی که داره فرستاده می‌شه."))
		}
	}
	var rows [][]telegram.InlineKeyboardButton
	var pair []telegram.InlineKeyboardButton
	for _, e := range effortLevels {
		label, style := e.icon+" "+l.t(e.name), ""
		if e.key == p.Effort {
			label, style = "✅ "+label, telegram.StyleSuccess
		}
		btn := b.button(label, pick(effortCode(e.key)), style)
		if e.key == llm.EffortUltra {
			rows = append(rows, row(btn))
			continue
		}
		if pair = append(pair, btn); len(pair) == 2 {
			rows, pair = append(rows, pair), nil
		}
	}
	if len(pair) > 0 {
		rows = append(rows, pair)
	}
	rows = append(rows, row(back))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}
