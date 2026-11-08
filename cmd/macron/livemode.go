package main

import (
	"context"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
)

// How a private chat sees the bot at work. Each user picks one in /mode;
// the admins set the default (⚙️ settings).
//
//   - liveMsg: a live message that shows the work and then becomes the
//     answer, with Telegram's "typing…" dots above it. The input stays free:
//     what the user sends meanwhile joins the job, and the stop button sits
//     inside the message, never where the send button is.
//   - liveStop: Telegram's draft bubble, whose stop button takes the send
//     button's place while the bot works (on Android the input is locked
//     then: nothing can be added to the job).
//   - liveDots: the draft bubble with Telegram's own "generating" dots and no
//     stop button (stop with a message or /stop where the input allows it).
const (
	liveMsg  = "msg"
	liveStop = "stop"
	liveDots = "dots"
)

// liveModes in the order they are offered.
var liveModes = []string{liveMsg, liveStop, liveDots}

func validLive(v string) bool { return v == liveMsg || v == liveStop || v == liveDots }

// liveDefault is the mode of users who chose none.
func (b *Bot) liveDefault() string {
	if v, _ := b.st.Setting(store.SettingLiveMode); validLive(v) {
		return v
	}
	return liveMsg
}

// liveModeOf is a user's mode.
func (b *Bot) liveModeOf(ctx context.Context, uid int64) string {
	if v, err := b.st.UserPref(ctx, uid, store.PrefLive); err == nil && validLive(v) {
		return v
	}
	return b.liveDefault()
}

// liveModeNow reads a user's mode with a time of its own (a live view starts
// whatever the request's context).
func (b *Bot) liveModeNow(uid int64) string {
	ctx, cancel := b.ctx(3 * time.Second)
	defer cancel()
	return b.liveModeOf(ctx, uid)
}

func liveName(l lang, v string) string {
	switch v {
	case liveStop:
		return l.t("⏹ پیش‌نویس با دکمه توقف")
	case liveDots:
		return l.t("⋯ پیش‌نویس سه‌نقطه")
	}
	return l.t("💬 پیام زنده")
}

// liveHelp says what each mode looks like.
func liveHelp(l lang) string {
	return l.t("💬 <b>پیام زنده</b>: یه پیام توی چت کار رو نشون می‌ده و آخرش خودش جواب می‌شه؛ بالای چت «در حال نوشتن…» با سه‌نقطه میاد. ورودی آزاده: وسط کار هر چی بفرستی به همون کار اضافه می‌شه؛ دکمه توقف داخل خود پیامه.\n") +
		l.t("⏹ <b>پیش‌نویس با دکمه توقف</b>: حباب پیش‌نویس تلگرام؛ دکمه توقف جای دکمه ارسال میاد (روی اندروید تا کار تموم نشه نمی‌شه پیامی فرستاد).\n") +
		l.t("⋯ <b>پیش‌نویس سه‌نقطه</b>: حباب پیش‌نویس با سه‌نقطه خود تلگرام، بدون دکمه توقف.")
}
