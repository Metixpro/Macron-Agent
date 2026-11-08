package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The personal edition answers its owners and nobody else. Every update is
// looked at here first: one from a stranger is dropped without a word (a bot
// that answers "not for you" tells a stranger that it exists, and who runs
// it), and it leaves any group it is added to, including by its owner.

// senderOf is the user an update comes from (false: it has none, e.g. a
// press of the stop button of a draft, which only a job of the owner has).
func senderOf(u telegram.Update) (int64, bool) {
	switch {
	case u.Message != nil:
		if u.Message.From != nil {
			return u.Message.From.ID, true
		}
		return 0, true // sent by a channel or an anonymous admin: nobody we know
	case u.CallbackQuery != nil:
		return u.CallbackQuery.From.ID, true
	case u.MyChatMember != nil:
		return u.MyChatMember.From.ID, true
	case u.PollAnswer != nil:
		if u.PollAnswer.User != nil {
			return u.PollAnswer.User.ID, true
		}
		return 0, true
	case u.GuestMessage != nil:
		if u.GuestMessage.From != nil {
			return u.GuestMessage.From.ID, true
		}
		return 0, true
	case u.EditedMessage != nil:
		if u.EditedMessage.From != nil {
			return u.EditedMessage.From.ID, true
		}
		return 0, true
	case u.PreCheckoutQuery != nil:
		return u.PreCheckoutQuery.From.ID, true
	case u.MessageReaction != nil:
		if u.MessageReaction.User != nil {
			return u.MessageReaction.User.ID, true
		}
		return 0, true
	}
	return 0, false
}

// allowedUpdate says whether an update may be handled.
func (b *Bot) allowedUpdate(u telegram.Update) bool {
	if mc := u.MyChatMember; mc != nil && !mc.Chat.Private() {
		switch mc.NewChatMember.Status {
		case "member", "administrator":
			b.wg.Add(1)
			go func() {
				defer b.wg.Done()
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				if err := b.api.LeaveChat(ctx, mc.Chat.ID); err != nil {
					log.Printf("could not leave non-private chat %d: %v", mc.Chat.ID, err)
				}
			}()
		}
		return false
	}
	var chat *telegram.Chat
	switch {
	case u.Message != nil:
		chat = &u.Message.Chat
	case u.EditedMessage != nil:
		chat = &u.EditedMessage.Chat
	case u.GuestMessage != nil:
		chat = &u.GuestMessage.Chat
	case u.CallbackQuery != nil && u.CallbackQuery.Message != nil:
		chat = &u.CallbackQuery.Message.Chat
	case u.MessageReaction != nil:
		chat = &u.MessageReaction.Chat
	case u.StoppedMessageGeneration != nil:
		chat = &u.StoppedMessageGeneration.Chat
	}
	if chat != nil && !chat.Private() {
		return false
	}
	id, known := senderOf(u)
	if !known || b.owner[id] {
		return true
	}
	b.noteStranger(id)
	return false
}

// noteStranger logs a stranger now and then, so an owner who typed their id
// wrong finds out why the bot is silent.
func (b *Bot) noteStranger(id int64) {
	if last, ok := b.strangers.Load(id); ok && time.Since(last.(time.Time)) < time.Hour {
		return
	}
	b.strangers.Store(id, time.Now())
	log.Printf("ignored a message from user %d: not an owner (the owners are set in MACRON_OWNERS)", id)
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ---------------------------------------------------------------- the personal edition's wording

const personalWelcome = `سلام %s، %s

من <b>مکرون</b> هستم؛ دستیار شخصی تو که روی سرور خودت زندگی می‌کنه 🏠
هر کاری از یه آدم پشت کامپیوتر برمیاد انجام می‌دم: سرچ، فایل، کد، سرور، دانلود، عکس و آهنگ… دسترسی کامل به همین ماشین دارم، پس می‌تونم برنامه نصب کنم، سرویس بالا بیارم و فایل‌های سرور رو ویرایش کنم.

<b>هر چی یاد می‌گیرم مال خودته</b>
• شخصیتم یه فایله (SOUL.md)، چیزهایی که درباره‌ات می‌دونم یکی دیگه (USER.md) و یادداشت‌هام از کار و سرور هم (MEMORY.md). هر جا ویرایششون کنی از پیام بعدی اعمال می‌شن.
• کارهایی که بلدم «مهارت»‌ان؛ بعد از کارهای سخت درس می‌گیرم و دفعه بعد بهتر انجامشون می‌دم.
• همه گفتگوهامون برای همیشه بایگانی می‌شه؛ بپرس «اون سروری که پارسال گفتیم چی بود؟»

<b>دستورها</b>
/new — گفتگوی تازه
/memory — حافظه و فایل‌هام
/files — فایل‌هام
/reminders — یادآورها و کارهای زمان‌بندی‌شده
/tasks — کارهای پس‌زمینه
/settings — پنل: مدل‌ها، جستجو، یادگیری و تنظیمات
/language — زبان (فارسی / English)
/help — راهنما`

const personalHelp = `<b>چطوری باهام کار کنی؟</b>

همون‌طوری که با یه همکار حرف می‌زنی بنویس. خودم می‌فهمم چی لازمه: سرچ، فایل، کد، دستور روی سرور، دانلود، عکس، نمودار، PDF…

• <b>سرور:</b> «سرویس nginx رو ریستارت کن»، «چرا دیسک پر شده؟»، «یه ربات تلگرام پایتونی بنویس و بالا بیارش». دسترسی کامل دارم و کارهام رو قدم‌به‌قدم نشونت می‌دم.
• <b>فایل و عکس</b> بفرستی روشون کار می‌کنم. روی هر پیام <b>ریپلای</b> کنی می‌فهمم منظورت کدومه.
• <b>یادآور و کار زمان‌بندی‌شده:</b> «هر روز ساعت ۸ وضعیت سرور رو بفرست». لیست و لغو با /reminders
• <b>کار پس‌زمینه:</b> کارهای طولانی رو می‌فرستم پس‌زمینه و نتیجه‌اش جدا میاد (/tasks).
• <b>حافظه:</b> «یادت باشه …» توی USER.md یا MEMORY.md ذخیره می‌شه. با /memory می‌بینی و پاک می‌کنی، یا خود فایل‌ها رو توی پوشه خونه‌ام ویرایش کن.
• <b>گذشته:</b> هر گفتگویی که داشتیم قابل جستجوئه؛ بگو «یادته درباره … چی گفتیم؟»
• <b>وصل کردن:</b> سرور (SSH)، گیت‌هاب، سرور MCP، پلاگین یا مهارت جدید رو توی چت بگو؛ «این سرور رو وصل کن: آدرس، کاربر، رمز یا کلید». خودم وصل می‌کنم و نشونت می‌دم.
• <b>یادگیری:</b> از کارهایی که اول خراب می‌شن و بعد درست، درس می‌گیرم و توی مهارت‌هام می‌نویسم؛ مهارت‌ها فایل‌هایی توی پوشه <code>skills</code> خونه‌امن و خودت هم می‌تونی ببینی و ویرایششون کنی.

/new — گفتگوی تازه
/memory — حافظه و فایل‌ها
/mode — حالت جواب (⚡ سریع / 🧠 عمیق)
/files — فایل‌هام
/reminders — یادآورها
/tasks — کارهای پس‌زمینه
/settings — پنل
/language — زبان (فارسی / English)
/cancel — توقف کار فعلی`

const personalHomeFmt = `

🤖 پروایدر فعال: %s
⏰ زمان‌بندی‌های فعال: %d یادآور · %d کار
💬 گفتگوهای بایگانی‌شده: %s پیام
📁 پوشه خونه: <code>%s</code>
⏱ آنلاین از %s پیش`

// personalCommands is the command menu of the personal edition.
func personalCommands(l lang) []telegram.BotCommand {
	return []telegram.BotCommand{
		{Command: "start", Description: l.t("شروع و معرفی")},
		{Command: "new", Description: l.t("گفتگوی تازه")},
		{Command: "memory", Description: l.t("حافظه و فایل‌هام (SOUL.md، USER.md، MEMORY.md)")},
		{Command: "mode", Description: l.t("حالت جواب: ⚡ سریع / 🧠 عمیق")},
		{Command: "files", Description: l.t("پنل فایل‌ها (دسته‌بندی، ارسال، zip)")},
		{Command: "reminders", Description: l.t("یادآورها و کارهای زمان‌بندی‌شده")},
		{Command: "tasks", Description: l.t("کارهای پس‌زمینه (بیلد، رندر، دانلود طولانی)")},
		{Command: "language", Description: l.t("زبان: فارسی / English")},
		{Command: "cancel", Description: l.t("توقف کار فعلی")},
		{Command: "help", Description: l.t("راهنما")},
	}
}

// viewHome is the admin panel's first screen: one owner, so nothing about
// users, sales or groups.
func (b *Bot) viewHome(ctx context.Context, admin int64) panelView {
	l := b.lang(admin)
	reminders, tasks, _ := b.st.ScheduleCounts(ctx)
	active := l.t("هیچ‌کدوم ⚠️")
	if p, ok := b.prov.Active(); ok {
		active = fmt.Sprintf("%s · <code>%s</code>", esc(p.Name), esc(p.Model))
	}
	kept, _ := b.st.ArchiveCount(ctx, admin)
	html := l.t(`🛠 <b>پنل مدیریت مکرون</b> · نسخه `) + app.Version + l.f(personalHomeFmt,
		active, reminders, tasks, humanInt(kept), esc(config.Home()), uptime(l, time.Since(b.started)))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("📊 آمار و سرور"), "a:stats", telegram.StylePrimary), b.button(l.t("🤖 پروایدرها"), "a:pv", "")),
		row(b.button(l.t("🦾 ایجنت و ابزارها"), "a:ai", ""), b.button(b.learnLabel(ctx, l), "a:learn", "")),
		row(b.button(l.t("⚙️ تنظیمات"), "a:st", ""), b.button(l.t("🔄 بروزرسانی"), "a:upd", "")),
		row(b.button(l.t("♻️ تازه‌سازی"), "a:home", ""), b.button(l.t("✖️ بستن"), "a:close", telegram.StyleDanger)),
	}
	return panelView{html: html, kb: keyboard(rows...)}
}

// firstOwner is the owner whose language the log messages are in.
func (b *Bot) firstOwner() int64 {
	var first int64
	for id := range b.owner {
		if first == 0 || id < first {
			first = id
		}
	}
	return first
}
