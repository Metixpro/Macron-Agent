package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func (b *Bot) executionMode() string {
	if b.app != nil && b.app.Machine != nil {
		return b.app.Machine.ExecutionMode()
	}
	if b.st != nil {
		if v, _ := b.st.Setting(settingExecutionMode); v != "" {
			return v
		}
	}
	return "best-effort"
}

func (b *Bot) sandboxed() bool {
	return b.app != nil && b.app.Machine != nil && b.app.Machine.Sandboxed()
}

func (b *Bot) setExecutionMode(ctx context.Context, mode string) (string, error) {
	if b.app == nil || b.app.Machine == nil {
		return "", errors.New("execution machine is not initialized")
	}
	note, err := b.app.Machine.SetExecutionMode(mode)
	if err != nil {
		return "", err
	}
	active := b.app.Machine.ExecutionMode()
	if b.app.Config != nil {
		b.app.Config.ExecutionMode = active
	}
	if b.st != nil {
		sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := b.st.SetSetting(sctx, settingExecutionMode, active); err != nil {
			return note, err
		}
	}
	return note, nil
}

func (b *Bot) executionView(l lang) (string, *telegram.InlineKeyboardMarkup) {
	mode := b.executionMode()
	state := "unsandboxed"
	if b.sandboxed() {
		state = "sandbox active"
	}

	var text string
	if l == fa {
		stateFa := "بدون سندباکس"
		if b.sandboxed() {
			stateFa = "سندباکس فعال 🔒"
		}
		text = fmt.Sprintf(
			"<b>حالت اجرای دستورات (Execution Mode)</b>\n\n"+
				"وضعیت فعلی: <code>%s</code> (%s)\n\n"+
				"• <code>sandbox</code>: ایزوله‌سازی سخت‌گیرانه با bwrap (در صورت نبودن، Fail-Closed)\n"+
				"• <code>best-effort</code>: استفاده از سندباکس در صورت وجود، وگرنه اجرای محلی\n"+
				"• <code>local</code>: دور زدن سندباکس و اجرای مستقیم روی سیستم میزبان",
			esc(mode), stateFa,
		)
	} else {
		text = fmt.Sprintf(
			"<b>Execution Mode</b>\n\n"+
				"Current: <code>%s</code> (%s)\n\n"+
				"• <code>sandbox</code>: strict bubblewrap isolation (fail-closed)\n"+
				"• <code>best-effort</code>: use sandbox when available, otherwise run locally\n"+
				"• <code>local</code>: bypass sandbox and run directly on the host",
			esc(mode), state,
		)
	}

	btn := func(id, title string) telegram.InlineKeyboardButton {
		if mode == id {
			title = "✓ " + title
		}
		return b.button(title, "exec:set:"+id, "")
	}

	kb := keyboard(
		row(
			btn("sandbox", "Sandbox"),
			btn("best-effort", "Best-effort"),
			btn("local", "Local"),
		),
	)
	return text, kb
}

func (b *Bot) executionCommand(ctx context.Context, m *telegram.Message, l lang, args string) {
	arg := strings.ToLower(strings.TrimSpace(args))
	if arg == "" || arg == "status" {
		text, kb := b.executionView(l)
		b.sendPanel(ctx, m, text, kb)
		return
	}

	note, err := b.setExecutionMode(ctx, arg)
	if err != nil {
		if l == fa {
			b.reply(m, fmt.Sprintf("[!] تغییر حالت اجرا متوقف شد (Fail-Closed):\n<code>%s</code>", esc(shortText(err.Error(), 300))))
		} else {
			b.reply(m, fmt.Sprintf("[!] Execution mode switch aborted (fail-closed):\n<code>%s</code>", esc(shortText(err.Error(), 300))))
		}
		return
	}

	active := b.executionMode()
	if active == "local" {
		if l == fa {
			b.reply(m, "[!] <b>هشدار:</b> حالت اجرا روی <code>local</code> قرار گرفت و سندباکس خاموش است. برای بازگشت از <code>/execution sandbox</code> استفاده کن.")
		} else {
			b.reply(m, "[!] <b>Warning:</b> execution mode is now <code>local</code> (sandbox bypassed). Use <code>/execution sandbox</code> to re-enable strict isolation.")
		}
		return
	}

	state := "unsandboxed"
	if b.sandboxed() {
		state = "sandbox active"
	}
	msg := fmt.Sprintf("[+] Execution mode: <code>%s</code> (%s)", esc(active), state)
	if l == fa {
		stateFa := "بدون سندباکس"
		if b.sandboxed() {
			stateFa = "سندباکس فعال 🔒"
		}
		msg = fmt.Sprintf("[+] حالت اجرا روی <code>%s</code> قرار گرفت (%s).", esc(active), stateFa)
	}
	if note != "" {
		msg += "\n<code>" + esc(shortText(note, 200)) + "</code>"
	}
	b.reply(m, msg)
}

func (b *Bot) handleExecutionCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	if !b.isOwner(q.From.ID) {
		answer("⛔", true)
		return
	}
	mode, ok := strings.CutPrefix(q.Data, "exec:set:")
	if !ok {
		answer("", false)
		return
	}
	l := b.langOf(&q.From)
	if _, err := b.setExecutionMode(ctx, mode); err != nil {
		answer(shortText(err.Error(), 180), true)
		return
	}
	text, kb := b.executionView(l)
	_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, text, kb)

	active := b.executionMode()
	if active == "local" {
		if l == fa {
			answer("⚠️ سندباکس خاموش شد (حالت local)", true)
		} else {
			answer("⚠️ Sandbox bypassed (local mode)", true)
		}
		return
	}
	if l == fa {
		answer("حالت اجرا: "+active, false)
	} else {
		answer("Execution mode: "+active, false)
	}
}
