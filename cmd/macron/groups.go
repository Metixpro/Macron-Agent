package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// ---------------------------------------------------------------- the bot in groups

// handleGroupMembership tracks the groups and channels the bot is added to or
// removed from, tells the owners (or the log chat) and greets a new group.
func (b *Bot) handleGroupMembership(u *telegram.ChatMemberUpdated) {
	ctx, cancel := b.ctx(30 * time.Second)
	defer cancel()
	old, cur := u.OldChatMember, u.NewChatMember
	was, now := old.Joined(), cur.Joined()
	chat := u.Chat
	by := u.From

	switch {
	case !was && now:
		members, _ := b.api.GetChatMemberCount(ctx, chat.ID)
		info, _ := b.api.GetChat(ctx, chat.ID)
		if err := b.st.SaveChatJoin(ctx, store.BotChat{ChatID: chat.ID, Type: chat.Type, Title: chat.Title, Username: chat.Username,
			Status: cur.Status, Members: members, AddedBy: by.ID, AddedByName: by.FullName()}); err != nil {
			log.Printf("group %d: %v", chat.ID, err)
		}
		log.Printf("added to %s %d (%q) by %d", chat.Type, chat.ID, chat.Title, by.ID)
		l, gl := b.logLang(), b.langOf(&by)
		notice := groupNotice(l, l.f("➕ <b>ربات به یه %s اضافه شد</b>", chatKind(l, chat.Type)), chat, info, members, &by, cur.Status, b.loc)
		if b.st.SettingBool(store.SettingGroupApproval, true) && !b.isAdmin(by.ID) {
			b.askApproval(ctx, chat, notice, gl)
			return
		}
		_ = b.st.SetGroupApproval(ctx, chat.ID, store.ApprovalApproved)
		b.approvals.Delete(chat.ID)
		b.notifyLog(ctx, notice)
		b.noteSight(chat.ID, cur.Status)
		if chat.Type != "channel" {
			hello := gl.t(textGroupHello)
			if !b.seesAll(ctx, chat.ID) { // in privacy mode «مکرون …» never reaches the bot: say so from the start
				hello += "\n\n" + b.sightHint(gl)
				b.sightHints.Store(chat.ID, time.Now())
			}
			b.sendHTML(ctx, chat.ID, hello, telegram.SendOptions{Silent: true})
		}
	case was && !now:
		if err := b.st.SaveChatLeave(ctx, chat.ID, cur.Status, chat.Title); err != nil {
			log.Printf("group %d: %v", chat.ID, err)
		}
		// Nothing of a group is kept once the bot is gone.
		if err := b.st.ForgetChat(ctx, chat.ID); err != nil {
			log.Printf("group %d: forgetting its messages: %v", chat.ID, err)
		}
		b.forgetGroupLogCache(chat.ID)
		log.Printf("removed from %s %d (%q) by %d", chat.Type, chat.ID, chat.Title, by.ID)
		l := b.logLang()
		title := l.f("➖ <b>ربات از یه %s حذف شد</b>", chatKind(l, chat.Type))
		if by.ID == b.me.ID {
			title = l.f("🚪 <b>ربات از یه %s خارج شد</b>", chatKind(l, chat.Type))
		}
		b.notifyLog(ctx, groupNotice(l, title, chat, nil, 0, &by, cur.Status, b.loc))
	case was && now && old.Status != cur.Status:
		b.noteSight(chat.ID, cur.Status)
		_ = b.st.SaveChatJoin(ctx, store.BotChat{ChatID: chat.ID, Type: chat.Type, Title: chat.Title, Username: chat.Username, Status: cur.Status})
		l := b.logLang()
		title := l.f("⭐️ <b>ربات توی یه %s ادمین شد</b>", chatKind(l, chat.Type))
		if cur.Status != "administrator" {
			title = l.f("🔻 <b>ادمینی ربات توی یه %s گرفته شد</b>", chatKind(l, chat.Type))
		}
		b.notifyLog(ctx, groupNotice(l, title, chat, nil, 0, &by, cur.Status, b.loc))
	}
}

func chatKind(l lang, t string) string {
	if t == "channel" {
		return l.t("کانال")
	}
	return l.t("گروه")
}

func groupNotice(l lang, title string, chat telegram.Chat, info *telegram.ChatFullInfo, members int, by *telegram.User, status string, loc *time.Location) string {
	var sb strings.Builder
	sb.WriteString(title + "\n\n")
	fmt.Fprintf(&sb, "👥 %s\n🆔 <code>%d</code>\n", esc(chatTitle(l, chat.Title)), chat.ID)
	switch {
	case chat.Username != "":
		fmt.Fprintf(&sb, "🔗 @%s\n", esc(chat.Username))
	case info != nil && info.InviteLink != "":
		fmt.Fprintf(&sb, "🔗 %s\n", esc(info.InviteLink))
	}
	if members > 0 {
		fmt.Fprint(&sb, l.f("👨‍👩‍👧 اعضا: <b>%d</b>\n", members))
	}
	if by != nil && by.ID != 0 {
		who := esc(by.FullName())
		if by.Username != "" {
			who += " (@" + esc(by.Username) + ")"
		}
		fmt.Fprint(&sb, l.f("👤 توسط: %s <code>%d</code>\n", who, by.ID))
	}
	fmt.Fprint(&sb, l.f("🎚 وضعیت ربات: %s\n🕒 %s", statusName(l, status), time.Now().In(loc).Format("2006-01-02 15:04")))
	return sb.String()
}

func chatTitle(l lang, t string) string {
	if strings.TrimSpace(t) == "" {
		return l.t("(بی‌نام)")
	}
	return t
}

func statusName(l lang, s string) string {
	switch s {
	case "administrator":
		return l.t("ادمین")
	case "member":
		return l.t("عضو")
	case "restricted":
		return l.t("محدود")
	case "left":
		return l.t("خارج شده")
	case "kicked":
		return l.t("اخراج شده")
	}
	return s
}

// notifyLog sends a notice to the log chat, or to every owner when none is
// set (or the log chat refuses it).
func (b *Bot) notifyLog(ctx context.Context, html string) { b.notifyLogKB(ctx, html, nil) }

// notifyLogKB is notifyLog with buttons.
func (b *Bot) notifyLogKB(ctx context.Context, html string, kb *telegram.InlineKeyboardMarkup) {
	if v, ok := b.st.Setting(store.SettingLogChat); ok && v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			if _, err := b.api.SendHTML(ctx, id, b.bidi(id, html), telegram.SendOptions{Markup: kb}); err == nil {
				return
			} else {
				log.Printf("log chat %d: %v (notifying the owners instead)", id, err)
			}
		}
	}
	for id := range b.owner {
		b.sendHTML(ctx, id, html, telegram.SendOptions{Markup: kb})
	}
}

// touchChat keeps the record of a group the bot is used in.
func (b *Bot) touchChat(c telegram.Chat) {
	ctx, cancel := b.ctx(5 * time.Second)
	defer cancel()
	if err := b.st.TouchChat(ctx, c.ID, c.Type, c.Title, c.Username); err != nil {
		log.Printf("group %d: %v", c.ID, err)
	}
}

// ---------------------------------------------------------------- admin panel: groups

func (b *Bot) viewGroups(ctx context.Context, l lang) panelView {
	active, left, members, err := b.st.ChatCounts(ctx)
	if err != nil {
		return panelView{html: l.t("خطا: ") + esc(err.Error()), kb: keyboard(row(b.back(l, "a:home")))}
	}
	list, _ := b.st.Chats(ctx, 15)
	logTo := l.t("مالک‌ها (پیوی)")
	if v, ok := b.st.Setting(store.SettingLogChat); ok && v != "" {
		logTo = "<code>" + esc(v) + "</code>"
	}
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("👥 <b>گروه‌ها و کانال‌ها</b>\n\n✅ فعال: <b>%d</b> (%d عضو)\n🚪 خارج‌شده: %d\n📍 لاگ ورود/خروج: %s\n", active, members, left, logTo))
	fmt.Fprint(&sb, l.f("✅ تأیید دستی گروه‌های جدید: %s\n", onOff(l, b.st.SettingBool(store.SettingGroupApproval, true))))
	var rows [][]telegram.InlineKeyboardButton
	if len(list) == 0 {
		sb.WriteString(l.t("\nهنوز ربات به گروهی اضافه نشده."))
	}
	for _, c := range list {
		icon := "🟢"
		if !c.Active() {
			icon = "⚪️"
		}
		label := icon + " " + chatTitle(l, c.Title)
		if c.Members > 0 {
			label += fmt.Sprintf(" · %d", c.Members)
		}
		rows = append(rows, row(b.button(label, fmt.Sprintf("a:gr:c:%d", c.ChatID), "")))
	}
	rows = append(rows,
		row(b.button(l.t("✅ تأیید گروه‌های جدید"), "a:st:gapprove:gr", "")),
		row(b.button(l.t("📍 تنظیم کانال لاگ"), "a:gr:log", ""), b.button(l.t("♻️ لاگ به پیوی"), "a:gr:logdel", "")),
		row(b.button(l.t("🔄 بروزرسانی"), "a:gr", ""), b.back(l, "a:home")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) viewGroup(ctx context.Context, l lang, id int64) panelView {
	c, err := b.st.Chat(ctx, id)
	if err != nil {
		return panelView{html: l.t("این گروه پیدا نشد."), kb: keyboard(row(b.back(l, "a:gr")))}
	}
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("👥 <b>%s</b>\n\n🆔 <code>%d</code> · %s\n🎚 وضعیت: %s\n", esc(chatTitle(l, c.Title)), c.ChatID, chatKind(l, c.Type), statusName(l, c.Status)))
	if c.Username != "" {
		fmt.Fprintf(&sb, "🔗 @%s\n", esc(c.Username))
	}
	if c.Members > 0 {
		fmt.Fprint(&sb, l.f("👨‍👩‍👧 اعضا: %d\n", c.Members))
	}
	if c.AddedBy != 0 {
		fmt.Fprint(&sb, l.f("👤 اضافه‌کننده: %s <code>%d</code>\n", esc(c.AddedByName), c.AddedBy))
	}
	if c.JoinedAt != nil {
		fmt.Fprint(&sb, l.f("📅 ورود: %s\n", c.JoinedAt.In(b.loc).Format("2006-01-02 15:04")))
	}
	if c.LeftAt != nil {
		fmt.Fprint(&sb, l.f("🚪 خروج: %s\n", c.LeftAt.In(b.loc).Format("2006-01-02 15:04")))
	}
	fmt.Fprint(&sb, l.f("🕒 آخرین فعالیت: %s", ago(l, &c.UpdatedAt)))
	state, _, _ := b.st.GroupApproval(ctx, id)
	if c.Active() {
		fmt.Fprint(&sb, l.f("\n✅ تأیید: %s", approvalName(l, state)))
	}
	rows := [][]telegram.InlineKeyboardButton{}
	if c.Active() && state == store.ApprovalPending {
		rows = append(rows, row(b.button(l.t("✅ فعال بشه"), fmt.Sprintf("a:gr:ok:%d", id), telegram.StyleSuccess),
			b.button(l.t("❌ رد و خروج"), fmt.Sprintf("a:gr:no:%d", id), telegram.StyleDanger)))
	}
	if c.Active() {
		rows = append(rows, row(b.button(l.t("🔄 بروزرسانی اعضا"), fmt.Sprintf("a:gr:sync:%d", id), ""),
			b.button(l.t("🚪 خروج از گروه"), fmt.Sprintf("a:gr:leave:%d", id), telegram.StyleDanger)))
	}
	rows = append(rows, row(b.back(l, "a:gr")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) handleGroupPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	l := b.langOf(&q.From)
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	id, _ := strconv.ParseInt(arg(3), 10, 64)
	switch arg(2) {
	case "c":
		answer("", false)
		show(b.viewGroup(ctx, l, id))
		return
	case "ok", "no":
		verdict, ok := b.decideGroup(ctx, l, id, arg(2) == "ok", q.From.FullName())
		answer(verdict, !ok)
		show(b.viewGroup(ctx, l, id))
		return
	case "sync":
		if n, err := b.api.GetChatMemberCount(ctx, id); err == nil {
			c, _ := b.st.Chat(ctx, id)
			if c != nil {
				c.Members = n
				_ = b.st.SaveChatJoin(ctx, *c)
			}
			answer(l.f("%d عضو", n), false)
		} else {
			answer(l.t("نشد: ")+err.Error(), true)
		}
		show(b.viewGroup(ctx, l, id))
		return
	case "leave":
		if err := b.api.LeaveChat(ctx, id); err != nil && !telegram.IsUnreachable(err) {
			answer(l.t("نشد: ")+err.Error(), true)
			return
		}
		_ = b.st.SaveChatLeave(ctx, id, "left", "")
		answer(l.t("از گروه خارج شدم"), false)
		show(b.viewGroup(ctx, l, id))
		return
	case "log":
		answer("", false)
		ask(&adminInput{kind: "gr_log"}, l.t("📍 کانال یا گروه لاگ رو مشخص کن:\n• یه پیام از اون کانال فوروارد کن، یا\n• یوزرنیمش (<code>@channel</code>) یا آیدی عددی‌اش رو بفرست.\n\nربات باید اونجا عضو باشه و اجازه ارسال پیام داشته باشه."))
		return
	case "logdel":
		_ = b.st.DeleteSetting(ctx, store.SettingLogChat)
		answer(l.t("از این به بعد لاگ‌ها به پیوی مالک‌ها فرستاده می‌شن"), false)
	default:
		answer("", false)
	}
	show(b.viewGroups(ctx, l))
}

// setLogChat handles the "gr_log" dialog step.
func (b *Bot) setLogChat(ctx context.Context, l lang, m *telegram.Message) (panelView, error) {
	var target any
	text := strings.TrimSpace(m.Text)
	switch id := m.Origin().ChatID(); {
	case id != 0:
		target = id
	case strings.HasPrefix(text, "@") && len(text) > 1:
		target = text
	default:
		id, err := strconv.ParseInt(text, 10, 64)
		if err != nil || id == 0 {
			return panelView{}, errors.New(l.t("یه پیام از کانال فوروارد کن، یا @یوزرنیم یا آیدی عددی‌اش رو بفرست"))
		}
		target = id
	}
	info, err := b.api.GetChat(ctx, target)
	if err != nil {
		return panelView{}, errors.New(l.t("پیداش نکردم؛ ربات باید عضو اونجا باشه"))
	}
	if _, err := b.api.SendHTML(ctx, info.ID, b.bidi(info.ID, l.t("✅ از این به بعد خبر ورود و خروج ربات به گروه‌ها اینجا فرستاده می‌شه.")), telegram.SendOptions{}); err != nil {
		return panelView{}, errors.New(l.t("نتونستم اونجا پیام بفرستم؛ ربات رو ادمین کن (با اجازه ارسال پیام)"))
	}
	if err := b.st.SetSetting(ctx, store.SettingLogChat, strconv.FormatInt(info.ID, 10)); err != nil {
		return panelView{}, err
	}
	return b.viewGroups(ctx, l), nil
}
