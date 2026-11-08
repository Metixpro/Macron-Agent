package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/sched"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// The bot speaks Persian and English. Texts are written in Persian in the
// code and looked up by that wording in the English catalog (i18n_en.go), so
// the code still reads naturally; a test keeps the catalog complete. Each
// user gets their /language choice, else the language of their Telegram app.

type lang uint8

const (
	fa lang = iota
	en
)

const prefLang = "lang"

// t translates a text.
func (l lang) t(s string) string {
	if l == en {
		if e, ok := english[s]; ok {
			return e
		}
	}
	return s
}

// f translates a format and fills it.
func (l lang) f(format string, a ...any) string { return fmt.Sprintf(l.t(format), a...) }

// num writes a number with the language's digits.
func (l lang) num(n int) string {
	if l == en {
		return strconv.Itoa(n)
	}
	return faNum(n)
}

func (l lang) code() string {
	if l == en {
		return "en"
	}
	return "fa"
}

// hasPersian reports whether s has Persian (Arabic-script) letters.
func hasPersian(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Arabic) && unicode.IsLetter(r) {
			return true
		}
	}
	return false
}

// langFromCode maps Telegram's language_code: Persian (and Dari, Pashto)
// apps and unknown ones read Persian, every other one English.
func langFromCode(code string) lang {
	switch strings.ToLower(strings.SplitN(code, "-", 2)[0]) {
	case "", "fa", "ps", "prs":
		return fa
	}
	return en
}

func parseLang(s string) (lang, bool) {
	switch s {
	case "fa":
		return fa, true
	case "en":
		return en, true
	}
	return fa, false
}

// langs caches each user's language.
type langs struct {
	mu     sync.Mutex
	chosen map[int64]lang // set with /language
	app    map[int64]lang // from the Telegram app
	loaded map[int64]bool
}

func (s *langs) lock() {
	s.mu.Lock()
	if s.app == nil {
		s.chosen, s.app, s.loaded = map[int64]lang{}, map[int64]lang{}, map[int64]bool{}
	}
}

// langOf is the language of the user behind an update.
func (b *Bot) langOf(u *telegram.User) lang {
	if u == nil {
		return fa
	}
	if u.LanguageCode != "" { // missing on users the bot builds itself (scheduled tasks)
		b.langs.lock()
		b.langs.app[u.ID] = langFromCode(u.LanguageCode)
		b.langs.mu.Unlock()
	}
	return b.lang(u.ID)
}

// lang is a user's language by id (owners in the log, reminders, panels).
func (b *Bot) lang(id int64) lang {
	b.langs.lock()
	loaded := b.langs.loaded[id]
	b.langs.mu.Unlock()
	if !loaded {
		b.loadLang(id)
	}
	b.langs.lock()
	defer b.langs.mu.Unlock()
	if l, ok := b.langs.chosen[id]; ok {
		return l
	}
	return b.langs.app[id]
}

func (b *Bot) loadLang(id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pref, err := b.st.UserPref(ctx, id, prefLang)
	if err != nil {
		return
	}
	u, _ := b.st.User(ctx, id)
	b.langs.lock()
	defer b.langs.mu.Unlock()
	b.langs.loaded[id] = true
	if l, ok := parseLang(pref); ok {
		b.langs.chosen[id] = l
	}
	if _, ok := b.langs.app[id]; !ok && u != nil {
		b.langs.app[id] = langFromCode(u.LanguageCode)
	}
}

// setLang stores a user's choice.
func (b *Bot) setLang(ctx context.Context, id int64, l lang) error {
	if err := b.st.SetUserPref(ctx, id, prefLang, l.code()); err != nil {
		return err
	}
	b.langs.lock()
	b.langs.chosen[id], b.langs.loaded[id] = l, true
	b.langs.mu.Unlock()
	return nil
}

// logLang is the language of the owners' log: the first owner's.
func (b *Bot) logLang() lang {
	var first int64
	for id := range b.owner {
		if first == 0 || id < first {
			first = id
		}
	}
	if first == 0 {
		return fa
	}
	return b.lang(first)
}

// chatLang is the language of a chat: the user's in private, the group's otherwise.
func (b *Bot) chatLang(ctx context.Context, chatID int64) lang {
	if chatID > 0 {
		return b.lang(chatID)
	}
	return b.groupLang(ctx, chatID)
}

// groupLang is the language of a group: that of whoever added the bot.
func (b *Bot) groupLang(ctx context.Context, chatID int64) lang {
	if c, err := b.st.Chat(ctx, chatID); err == nil && c.AddedBy != 0 {
		return b.lang(c.AddedBy)
	}
	return fa
}

// describeRule says when a schedule repeats, in the user's language.
func describeRule(l lang, r sched.Rule) string {
	if l == en {
		return r.DescribeEnglish()
	}
	return r.Describe()
}

// sendLanguage offers the two languages.
func (b *Bot) sendLanguage(m *telegram.Message, l lang) {
	ctx, cancel := b.ctx(15 * time.Second)
	defer cancel()
	b.sendPanel(ctx, m, l.t("🌐 به چه زبونی باهات حرف بزنم؟"), keyboard(row(b.button("🇮🇷 فارسی", "lang:fa", ""), b.button("🇬🇧 English", "lang:en", ""))))
}

func (b *Bot) handleLanguageCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l, ok := parseLang(strings.TrimPrefix(q.Data, "lang:"))
	if !ok {
		answer("", false)
		return
	}
	if err := b.setLang(ctx, q.From.ID, l); err != nil {
		answer(l.t("ذخیره نشد؛ دوباره امتحان کن."), true)
		return
	}
	answer("✅", false)
	_ = b.editHTML(ctx, q.Message.Chat.ID, q.Message.MessageID, l.t("✅ از این به بعد فارسی حرف می‌زنیم."), nil)
}
