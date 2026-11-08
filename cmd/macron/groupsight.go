package main

import (
	"context"
	"log"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// What the bot sees of a group. In privacy mode (the default in BotFather),
// a bot that is not an admin gets only the messages that @mention it, reply
// to its own messages, or are commands: «مکرون …» — on someone else's
// message above all — never reaches it, and nothing on the bot's side can
// change that. So when a message does reach it in such a group, its admins
// are told (once every few days) how to fix it, and so are the bot's owners.

const (
	sightTTL      = 15 * time.Minute
	sightHintGap  = 72 * time.Hour
	settingSightW = "privacy_warned_at"
)

type sight struct {
	all bool
	at  time.Time
}

// seesAll reports whether the bot gets every message of a group: privacy
// mode off, or an admin there.
func (b *Bot) seesAll(ctx context.Context, chatID int64) bool {
	if b.readsAll.Load() {
		return true
	}
	if v, ok := b.sights.Load(chatID); ok && time.Since(v.(sight).at) < sightTTL {
		return v.(sight).all
	}
	all := b.canReadGroup(ctx, chatID)
	b.sights.Store(chatID, sight{all: all, at: time.Now()})
	return all
}

// noteSight hears that the bot's status in a group changed.
func (b *Bot) noteSight(chatID int64, status string) {
	b.sights.Store(chatID, sight{all: b.readsAll.Load() || status == "administrator" || status == "creator", at: time.Now()})
}

// checkSight tells a group's admins, once every few days, that the bot
// misses its messages, when it does.
func (b *Bot) checkSight(m *telegram.Message) {
	if m.Chat.Private() || m.Chat.Type == "channel" || b.readsAll.Load() {
		return
	}
	ctx, cancel := b.ctx(15 * time.Second)
	defer cancel()
	if b.seesAll(ctx, m.Chat.ID) {
		return
	}
	if v, ok := b.sightHints.Load(m.Chat.ID); ok && time.Since(v.(time.Time)) < sightHintGap {
		return
	}
	b.sightHints.Store(m.Chat.ID, time.Now())
	b.sendHTML(ctx, m.Chat.ID, b.sightHint(b.langOf(m.From)), telegram.SendOptions{Silent: true})
	b.warnPrivacy(ctx)
}

// sightHint is the note for a group's admins.
func (b *Bot) sightHint(l lang) string {
	return l.f("💡 <b>یه نکته برای ادمین‌های گروه</b>\nالان فقط پیام‌هایی به دستم می‌رسه که منو منشن کنن (@%s) یا ریپلای به پیام خودم باشن؛ برای همین وقتی بدون منشن صدام می‌زنید، مثلاً روی پیام یکی دیگه می‌نویسید «مکرون نظرت چیه؟»، تلگرام اصلاً اون پیام رو به من نمی‌ده.\n\nاگه منو <b>ادمین گروه</b> کنید (هیچ دسترسی خاصی لازم نیست)، هر جا صدام کنید جواب می‌دم.", b.me.Username)
}

// warnPrivacy tells the owners, at most once a week, that privacy mode is on.
func (b *Bot) warnPrivacy(ctx context.Context) {
	if b.readsAll.Load() {
		return
	}
	if v, ok := b.st.Setting(settingSightW); ok {
		if t, err := time.Parse(time.RFC3339, v); err == nil && time.Since(t) < 7*24*time.Hour {
			return
		}
	}
	_ = b.st.SetSetting(ctx, settingSightW, time.Now().UTC().Format(time.RFC3339))
	log.Printf("telegram: privacy mode is on: in groups where the bot is not an admin it sees only @mentions, replies to it and commands")
	b.notifyLog(ctx, b.logLang().t("⚠️ <b>حالت Privacy ربات روشنه</b>\nتوی گروه‌هایی که ربات ادمین نیست، تلگرام فقط پیام‌هایی رو به ربات می‌ده که منشنش کنن یا ریپلای به خودش باشن؛ «مکرون …» (مخصوصاً روی پیام بقیه) اصلاً به ربات نمی‌رسه.\n\nبرای درست شدنش یکی از این دو کار:\n• توی @BotFather با /setprivacy حالت Privacy رو <b>Disable</b> کن، بعد ربات رو از گروه‌ها حذف و دوباره اضافه کن (تلگرام بدون این کار تغییر رو اعمال نمی‌کنه).\n• یا ربات رو توی هر گروه <b>ادمین</b> کن."))
}

// refreshMe reads again whether privacy mode is on (the owner may change it
// in BotFather while the bot runs).
func (b *Bot) refreshMe(ctx context.Context) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			gctx, cancel := context.WithTimeout(ctx, 20*time.Second)
			if me, err := b.api.GetMe(gctx); err == nil && me.CanReadAllGroupMessages != b.readsAll.Load() {
				b.readsAll.Store(me.CanReadAllGroupMessages)
				b.sights.Clear()
			}
			cancel()
		}
	}
}
