package main

import (
	"context"
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func (b *Bot) statusView(ctx context.Context, uid int64, l lang) panelView {
	var sb strings.Builder
	if l == fa {
		fmt.Fprintf(&sb, "📊 <b>وضعیت %s %s</b>\n", esc(app.Name), esc(app.Version))
		fmt.Fprintf(&sb, "توسعه توسط <b>%s</b> (<a href=\"%s\">%s</a>) · لایسنس MIT\n\n", esc(app.Creator), esc(app.CreatorURL), esc(app.CreatorHandle))
	} else {
		fmt.Fprintf(&sb, "📊 <b>%s %s Status</b>\n", esc(app.Name), esc(app.Version))
		fmt.Fprintf(&sb, "Built by <b>%s</b> (<a href=\"%s\">%s</a>) · MIT License\n\n", esc(app.Creator), esc(app.CreatorURL), esc(app.CreatorHandle))
	}

	if b.prov != nil {
		if p, ok := b.prov.Active(); ok {
			api := providers.API(p)
			win := p.ContextWindow
			winLabel := ""
			if win > 0 {
				winLabel = agent.HumanTokens(win)
			} else {
				winLabel = agent.HumanTokens(agent.ContextWindow(p.Model)) + " (auto)"
			}
			tempLabel := "auto"
			if p.Temperature != nil {
				tempLabel = strconv.FormatFloat(*p.Temperature, 'f', -1, 64)
			}
			eff := p.Effort
			if eff == "" {
				eff = "auto"
			}
			stream := onOff(l, !p.NoStream)
			vision := onOff(l, p.Vision)
			if l == fa {
				fmt.Fprintf(&sb, "🤖 <b>مدل فعال:</b> <code>%s</code> (%s)\n", esc(p.Model), esc(p.Name))
				fmt.Fprintf(&sb, "🔌 API: <code>%s</code> · 🌊 استریم: %s · 🖼 تصویر: %s\n", esc(api), stream, vision)
				fmt.Fprintf(&sb, "⚡ Effort: <code>%s</code> · 🪟 پنجره: <code>%s</code> · 🌡 دما: <code>%s</code>\n", esc(eff), esc(winLabel), esc(tempLabel))
			} else {
				fmt.Fprintf(&sb, "🤖 <b>Active model:</b> <code>%s</code> (%s)\n", esc(p.Model), esc(p.Name))
				fmt.Fprintf(&sb, "🔌 API: <code>%s</code> · 🌊 Stream: %s · 🖼 Vision: %s\n", esc(api), stream, vision)
				fmt.Fprintf(&sb, "⚡ Effort: <code>%s</code> · 🪟 Window: <code>%s</code> · 🌡 Temp: <code>%s</code>\n", esc(eff), esc(winLabel), esc(tempLabel))
			}
		} else {
			if l == fa {
				sb.WriteString("🤖 <b>مدل فعال:</b> تنظیم نشده ⚠️\n")
			} else {
				sb.WriteString("🤖 <b>Active model:</b> not configured ⚠️\n")
			}
		}
	}

	execMode := b.executionMode()
	sandboxState := "unsandboxed"
	if b.sandboxed() {
		sandboxState = "sandbox active 🔒"
	}
	if l == fa {
		sandboxState = "بدون سندباکس"
		if b.sandboxed() {
			sandboxState = "سندباکس فعال 🔒"
		}
	}

	gate := b.gateMode()
	yolo := false
	if b.app != nil && b.app.Registry != nil && b.app.Registry.Gate != nil {
		yolo, _ = b.app.Registry.Gate.Yolo()
	}
	if l == fa {
		yoloText := ""
		if yolo {
			yoloText = " · ⚡ YOLO روشن"
		}
		fmt.Fprintf(&sb, "🛡 <b>حالت اجرا:</b> <code>%s</code> (%s)\n", esc(execMode), sandboxState)
		fmt.Fprintf(&sb, "🔐 <b>تأیید دستورات:</b> <code>%s</code>%s\n", esc(gate), yoloText)
	} else {
		yoloText := ""
		if yolo {
			yoloText = " · ⚡ YOLO on"
		}
		fmt.Fprintf(&sb, "🛡 <b>Execution mode:</b> <code>%s</code> (%s)\n", esc(execMode), sandboxState)
		fmt.Fprintf(&sb, "🔐 <b>Approvals:</b> <code>%s</code>%s\n", esc(gate), yoloText)
	}

	if b.mem != nil {
		if mem, err := b.mem.Load(ctx, uid); err == nil && mem != nil {
			pct := 0.0
			if mem.Budget > 0 {
				pct = float64(mem.Tokens) * 100 / float64(mem.Budget)
			}
			if l == fa {
				fmt.Fprintf(&sb, "🧠 <b>حافظه گفتگو:</b> %d پیام · %s / %s توکن (%.1f٪)\n", mem.Messages, humanInt(mem.Tokens), humanInt(mem.Budget), pct)
			} else {
				fmt.Fprintf(&sb, "🧠 <b>Context:</b> %d msgs · %s / %s tokens (%.1f%%)\n", mem.Messages, humanInt(mem.Tokens), humanInt(mem.Budget), pct)
			}
		}
	}

	skillCount := 0
	if b.app != nil && b.app.Skills != nil {
		skillCount = len(b.app.Skills.Snapshot())
	}
	if l == fa {
		fmt.Fprintf(&sb, "🧩 <b>مهارت‌ها:</b> %d · 🖥 <code>%s/%s</code>\n", skillCount, runtime.GOOS, runtime.GOARCH)
		if b.isOwner(uid) {
			fmt.Fprintf(&sb, "📁 <b>مسیر:</b> <code>%s</code>", esc(config.Home()))
		}
	} else {
		fmt.Fprintf(&sb, "🧩 <b>Skills:</b> %d · 🖥 <code>%s/%s</code>\n", skillCount, runtime.GOOS, runtime.GOARCH)
		if b.isOwner(uid) {
			fmt.Fprintf(&sb, "📁 <b>Home:</b> <code>%s</code>", esc(config.Home()))
		}
	}

	kb := keyboard(row(
		b.button(l.t("🔄 بروزرسانی"), "st:refresh", ""),
		b.button(l.t("✖️ بستن"), "st:close", telegram.StyleDanger),
	))
	return panelView{html: sb.String(), kb: kb}
}

func (b *Bot) statusCommand(ctx context.Context, m *telegram.Message, l lang) {
	v := b.statusView(ctx, m.From.ID, l)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func (b *Bot) handleStatusCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	answer("", false)
	if strings.TrimPrefix(q.Data, "st:") == "close" {
		_ = b.api.DeleteMessage(ctx, q.Message.Chat.ID, q.Message.MessageID)
		return
	}
	l := b.langOf(&q.From)
	b.showPanel(ctx, q.Message, b.statusView(ctx, q.From.ID, l))
}

func (b *Bot) aboutCommand(m *telegram.Message, l lang) {
	if l == fa {
		b.reply(m, fmt.Sprintf(
			"✨ <b>%s %s</b>\n\n"+
				"دستیار شخصی هوش مصنوعی برای دسکتاپ، ترمینال و تلگرام.\n\n"+
				"👥 <b>تیم سازنده:</b> تیم %s\n"+
				"📣 <b>کانال تلگرام:</b> <a href=\"%s\">%s</a>\n"+
				"📜 <b>لایسنس:</b> MIT License — متعلق به تیم SpectorDev\n"+
				"💻 <b>مخزن کد:</b> https://github.com/Metixpro/Macron-Agent",
			esc(app.Name), esc(app.Version), esc(app.Creator), esc(app.CreatorURL), esc(app.CreatorHandle),
		))
		return
	}
	b.reply(m, fmt.Sprintf(
		"✨ <b>%s %s</b>\n\n"+
			"Personal AI agent across Desktop, Terminal, and Telegram.\n\n"+
			"👥 <b>Developed by:</b> %s Team\n"+
			"📣 <b>Telegram Channel:</b> <a href=\"%s\">%s</a>\n"+
			"📜 <b>License:</b> %s\n"+
			"💻 <b>Repository:</b> https://github.com/Metixpro/Macron-Agent",
		esc(app.Name), esc(app.Version), esc(app.Creator), esc(app.CreatorURL), esc(app.CreatorHandle), esc(app.License),
	))
}

func (b *Bot) compactCommand(ctx context.Context, m *telegram.Message, l lang) {
	uid := m.From.ID
	before, err := b.mem.Load(ctx, uid)
	if err != nil {
		b.reply(m, l.t(textError))
		return
	}
	if before == nil || before.Messages == 0 {
		if l == fa {
			b.reply(m, "هنوز پیامی برای خلاصه‌سازی وجود ندارد.")
		} else {
			b.reply(m, "Nothing to compact yet.")
		}
		return
	}
	done, err := b.mem.Compress(ctx, uid)
	if err != nil {
		b.reply(m, l.t(textError))
		return
	}
	if !done {
		if l == fa {
			b.reply(m, "گفتگو هنوز کوتاه است و نیازی به خلاصه‌سازی ندارد.")
		} else {
			b.reply(m, "The conversation is short already.")
		}
		return
	}
	after, _ := b.mem.Load(ctx, uid)
	afterTok := 0
	if after != nil {
		afterTok = after.Tokens
	}
	if l == fa {
		b.reply(m, fmt.Sprintf("🗜 حافظه گفتگو فشرده شد: <code>%s</code> ← <code>%s</code> توکن (خلاصه کارهای قبلی حفظ شد).", humanInt(before.Tokens), humanInt(afterTok)))
	} else {
		b.reply(m, fmt.Sprintf("🗜 Compacted: <code>%s</code> → <code>%s</code> tokens (a summary keeps what was done).", humanInt(before.Tokens), humanInt(afterTok)))
	}
}

func (b *Bot) connectionsCommand(ctx context.Context, m *telegram.Message, arg string, l lang) {
	if !b.isOwner(m.From.ID) {
		if l == fa {
			b.reply(m, "این دستور فقط برای مالک ربات است.")
		} else {
			b.reply(m, "This command is only available to the bot owner.")
		}
		return
	}
	fields := strings.Fields(strings.TrimSpace(arg))
	if len(fields) >= 2 && strings.EqualFold(fields[0], "test") {
		service := fields[1]
		resource := ""
		if len(fields) >= 3 {
			resource = fields[2]
		}
		if b.app == nil || b.app.Registry == nil {
			b.reply(m, l.t(textError))
			return
		}
		tctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		defer cancel()
		out, err := b.app.Registry.TestConnection(tctx, service, resource)
		if err != nil {
			if l == fa {
				b.reply(m, fmt.Sprintf("❌ تست <code>%s</code> ناموفق بود:\n<code>%s</code>", esc(service), esc(err.Error())))
			} else {
				b.reply(m, fmt.Sprintf("❌ Test failed for <code>%s</code>:\n<code>%s</code>", esc(service), esc(err.Error())))
			}
			return
		}
		if l == fa {
			b.reply(m, fmt.Sprintf("✅ <b>%s</b>\n<code>%s</code>", esc(service), esc(out)))
		} else {
			b.reply(m, fmt.Sprintf("✅ <b>%s</b>\n<code>%s</code>", esc(service), esc(out)))
		}
		return
	}

	var sb strings.Builder
	if l == fa {
		sb.WriteString("🔌 <b>وضعیت اتصال سرویس‌های خارجی</b>\n\n")
	} else {
		sb.WriteString("🔌 <b>External Service Connections</b>\n\n")
	}
	for _, spec := range tools.ConnectionSpecs() {
		mark := "⚪"
		state := "not set up"
		if l == fa {
			state = "تنظیم نشده"
		}
		if tools.ConnectionReady(spec) {
			mark = "🟢"
			state = "configured"
			if l == fa {
				state = "تنظیم شده"
			}
		}
		fmt.Fprintf(&sb, "%s <b>%s</b> (<code>%s</code>) — %s\n", mark, esc(spec.Name), esc(spec.ID), esc(state))
	}
	if l == fa {
		sb.WriteString("\nبرای تست خواندنی هر سرویس:\n<code>/connections test github</code>")
	} else {
		sb.WriteString("\nRun a read-only probe:\n<code>/connections test github</code>")
	}
	b.reply(m, sb.String())
}
