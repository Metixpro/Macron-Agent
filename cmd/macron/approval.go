package main

import (
	"context"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	approvalWait     = 24 * time.Hour
	approvalCacheTTL = time.Minute
	approvalSweep    = 10 * time.Minute
)

type approvalState struct {
	state string
	at    time.Time
}

func (b *Bot) groupActive(ctx context.Context, chatID int64) bool {
	if v, ok := b.approvals.Load(chatID); ok {
		if s := v.(approvalState); time.Since(s.at) < approvalCacheTTL {
			return s.state == store.ApprovalApproved
		}
	}
	state, _, err := b.st.GroupApproval(ctx, chatID)
	if err != nil {
		log.Printf("[chat %d] approval: %v", chatID, err)
		return true
	}
	b.approvals.Store(chatID, approvalState{state: state, at: time.Now()})
	return state == store.ApprovalApproved
}

func approvalKeyboard(l lang, b *Bot, chatID int64) *telegram.InlineKeyboardMarkup {
	id := strconv.FormatInt(chatID, 10)
	return keyboard(row(b.button(l.t("✅ فعال بشه"), "ga:"+id+":ok", telegram.StyleSuccess), b.button(l.t("❌ رد و خروج"), "ga:"+id+":no", telegram.StyleDanger)))
}

func (b *Bot) askApproval(ctx context.Context, chat telegram.Chat, notice string, group lang) {
	l := b.logLang()
	if err := b.st.SetGroupApproval(ctx, chat.ID, store.ApprovalPending); err != nil {
		log.Printf("[chat %d] approval: %v", chat.ID, err)
	}
	b.approvals.Delete(chat.ID)
	notice += l.f("\n\n⏳ <b>منتظر تأیید توئه.</b> تا تأیید نکنی، ربات اینجا جواب نمی‌ده؛ اگه رد کنی یا تا %s ساعت تصمیم نگیری، از گروه خارج می‌شه.",
		l.num(int(approvalWait.Hours())))
	b.notifyLogKB(ctx, notice, approvalKeyboard(l, b, chat.ID))
	if chat.Type != "channel" {
		b.sendHTML(ctx, chat.ID, group.t(textGroupPending), telegram.SendOptions{Silent: true})
	}
}

func (b *Bot) handleApprovalCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 {
		answer("", false)
		return
	}
	if !b.isAdmin(q.From.ID) {
		answer(l.t("فقط ادمین‌های ربات می‌تونن تصمیم بگیرن."), true)
		return
	}
	chatID, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		answer("", false)
		return
	}
	verdict, ok := b.decideGroup(ctx, l, chatID, parts[2] == "ok", q.From.FullName())
	answer(verdict, !ok)

	if q.Message != nil {
		_ = b.api.EditMarkup(ctx, q.Message.Chat.ID, q.Message.MessageID, nil)
		if ok && q.Message.Text != "" {
			_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, esc(q.Message.Text)+"\n\n"+verdict, nil)
		}
	}
}

func (b *Bot) decideGroup(ctx context.Context, l lang, chatID int64, approve bool, by string) (string, bool) {
	state := store.ApprovalRejected
	if approve {
		state = store.ApprovalApproved
	}
	changed, err := b.st.DecideGroup(ctx, chatID, state)
	b.approvals.Delete(chatID)
	switch {
	case err != nil:
		return l.f("خطا: %s", err.Error()), false
	case !changed:
		current, _, _ := b.st.GroupApproval(ctx, chatID)
		return l.f("قبلاً تصمیمش گرفته شده (%s).", approvalName(l, current)), false
	}
	group := b.groupLang(ctx, chatID)
	if approve {
		b.sendHTML(ctx, chatID, group.t(textGroupHello), telegram.SendOptions{Silent: true})
		log.Printf("group %d approved by %s", chatID, by)
		return l.f("✅ فعال شد (%s)", by), true
	}
	b.sendHTML(ctx, chatID, group.t(textGroupRejected), telegram.SendOptions{Silent: true})
	b.leaveGroup(ctx, chatID)
	log.Printf("group %d rejected by %s", chatID, by)
	return l.f("❌ رد شد و ربات خارج شد (%s)", by), true
}

func (b *Bot) leaveGroup(ctx context.Context, chatID int64) {
	if err := b.api.LeaveChat(ctx, chatID); err != nil && !telegram.IsUnreachable(err) {
		log.Printf("[chat %d] leaving: %v", chatID, err)
	}
	_ = b.st.SaveChatLeave(ctx, chatID, "left", "")
	_ = b.st.ForgetChat(ctx, chatID)
	_ = b.st.SetGroupApproval(ctx, chatID, store.ApprovalRejected)
	b.forgetGroupLogCache(chatID)
	b.approvals.Delete(chatID)
}

func approvalName(l lang, state string) string {
	switch state {
	case store.ApprovalPending:
		return l.t("منتظر تأیید")
	case store.ApprovalRejected:
		return l.t("رد شده")
	}
	return l.t("فعال")
}
