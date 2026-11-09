package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"regexp"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/sysmon"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

type Bot struct {
	api   *telegram.Bot
	app   *app.App
	st    *store.Store
	prov  *providers.Manager
	mem   *memory.Manager
	me    telegram.User
	loc   *time.Location
	owner map[int64]bool

	albums        albumCollector
	gate          *guard.Gate
	flood         *guard.RateLimiter
	runTimeout    time.Duration
	envWindow     int
	langs         langs
	emojis        atomic.Pointer[emoji.Set]
	richOff       atomic.Bool
	stopRowOff    atomic.Bool
	mediaStrikes  atomic.Int32
	mediaFailedAt atomic.Int64

	inputs      sync.Map
	runs        sync.Map
	bgRuns      sync.Map
	resumes     sync.Map
	drafts      sync.Map
	fileJobs    sync.Map
	cookieWaits sync.Map
	panels      sync.Map
	fb          feedbackState
	evalBusy    atomic.Bool
	taught      teachCache
	usage       usageCache
	sys         *sysmon.Sampler
	sysOnce     sync.Once

	cookies *cookiePool
	plugins *plugins.Manager

	guestAnswered sync.Map
	guestReplies  sync.Map
	guestStubOff  atomic.Bool
	skillMu       sync.Mutex
	routes        sync.Map
	triage        triageState
	hookSeen      sync.Map
	updating      atomic.Bool
	moaStrictBy   sync.Map
	gateAsks      sync.Map
	suspended     sync.Map

	ffmpegOnce sync.Once
	ffmpeg     bool

	jobCtx  context.Context
	wg      sync.WaitGroup
	started time.Time

	brain *brain.Brain

	personal  bool
	workFiles time.Duration
	strangers sync.Map
}

func (b *Bot) isOwner(id int64) bool { return b.owner[id] }
func (b *Bot) isAdmin(id int64) bool { return b.owner[id] || b.st.IsAdmin(id) }

func (b *Bot) reloadEmoji(ctx context.Context) {
	rows, err := b.st.PremiumEmojiInOrder(ctx)
	if err != nil {
		log.Printf("premium emoji: %v", err)
		return
	}
	pairs := make([]emoji.Pair, len(rows))
	for i, r := range rows {
		pairs[i] = emoji.Pair{Emoji: r[0], ID: r[1]}
	}
	kept, extra := emoji.Dedupe(pairs)
	removed := 0
	for _, p := range extra {
		if err := b.st.DeletePremiumEmoji(ctx, p.Emoji); err == nil {
			removed++
		}
	}
	if removed > 0 {
		log.Printf("premium emoji: removed %d extra mappings", removed)
	}
	m := make(map[string]string, len(kept))
	for _, p := range kept {
		m[p.Emoji] = p.ID
	}
	b.emojis.Store(emoji.New(m))
}

func (b *Bot) premium() *emoji.Set {
	if !b.st.SettingBool(store.SettingPremiumEmoji, false) {
		return nil
	}
	return b.emojis.Load()
}

func (b *Bot) ctx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(b.jobCtx, d)
}

func (b *Bot) dispatch(u telegram.Update) {
	if b.personal && !b.allowedUpdate(u) {
		return
	}
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer func() {
			if p := recover(); p != nil {
				log.Printf("panic while handling update %d: %v\n%s", u.UpdateID, p, debug.Stack())
				if m := u.Message; m != nil {
					ctx, cancel := b.ctx(10 * time.Second)
					defer cancel()
					_, _ = b.api.SendHTML(ctx, m.Chat.ID, b.bidi(m.Chat.ID, b.langOf(m.From).t(textError)), telegram.SendOptions{ReplyTo: m.MessageID})
				}
			}
		}()
		switch {
		case u.Message != nil:
			b.handleMessage(u.Message)
		case u.CallbackQuery != nil:
			b.handleCallback(u.CallbackQuery)
		case u.MyChatMember != nil:
			b.handleMembership(u.MyChatMember)
		case u.StoppedMessageGeneration != nil:
			b.handleStop(u.StoppedMessageGeneration)
		case u.PollAnswer != nil:
			b.handlePollAnswer(u.PollAnswer)
		case u.GuestMessage != nil:
			b.handleGuest(u.GuestMessage)
		case u.MessageReaction != nil:
			b.handleReaction(u.MessageReaction)
		}
	}()
}

func (b *Bot) reply(m *telegram.Message, html string) *telegram.Message {
	ctx, cancel := b.ctx(30 * time.Second)
	defer cancel()
	return b.sendHTML(ctx, m.Chat.ID, html, telegram.SendOptions{ReplyTo: m.MessageID})
}

func (b *Bot) sendHTML(ctx context.Context, chatID int64, html string, opts telegram.SendOptions) *telegram.Message {
	if b.personal && chatID < 0 {
		return nil
	}
	html = b.bidi(chatID, html)
	if set := b.premium(); set != nil {
		if sent, err := b.api.SendHTML(ctx, chatID, set.HTML(html), opts); err == nil {
			return sent
		}

	}
	sent, err := b.api.SendHTML(ctx, chatID, html, opts)
	if err != nil && !telegram.IsUnreachable(err) {
		log.Printf("[chat %d] send failed: %v", chatID, err)
	}
	return sent
}

func (b *Bot) editHTML(ctx context.Context, chatID, msgID int64, html string, kb *telegram.InlineKeyboardMarkup) error {
	html = b.bidi(chatID, html)
	if set := b.premium(); set != nil {
		if err := b.api.EditMessageText(ctx, chatID, msgID, set.HTML(html), kb); err == nil {
			return nil
		}
	}
	return b.api.EditMessageText(ctx, chatID, msgID, html, kb)
}

func (b *Bot) button(label, data, style string) telegram.InlineKeyboardButton {
	btn := telegram.InlineKeyboardButton{Text: label, CallbackData: data, Style: style}
	if set := b.premium(); set != nil {
		btn.Text, btn.IconCustomEmojiID = set.Button(label)
	}
	btn.Text = bidiLabel(btn.Text)
	return btn
}

func (b *Bot) urlButton(label, url string) telegram.InlineKeyboardButton {
	btn := telegram.InlineKeyboardButton{Text: label, URL: url, Style: telegram.StylePrimary}
	if set := b.premium(); set != nil {
		btn.Text, btn.IconCustomEmojiID = set.Button(label)
	}
	btn.Text = bidiLabel(btn.Text)
	return btn
}

func (b *Bot) bidi(chatID int64, html string) string {
	if !hasPersian(html) {
		return html
	}
	if chatID > 0 && b.lang(chatID) != fa || chatID < 0 && !telegram.IsMostlyRTL(textutil.StripHTML(html)) {
		return html
	}
	return telegram.MarkRTL(html)
}

func bidiLabel(s string) string {
	if telegram.IsMostlyRTL(s) {
		return telegram.MarkRTL(s)
	}
	return s
}

func keyboard(rows ...[]telegram.InlineKeyboardButton) *telegram.InlineKeyboardMarkup {
	var out [][]telegram.InlineKeyboardButton
	for _, r := range rows {
		if len(r) > 0 {
			out = append(out, r)
		}
	}
	return &telegram.InlineKeyboardMarkup{InlineKeyboard: out}
}

func row(btns ...telegram.InlineKeyboardButton) []telegram.InlineKeyboardButton { return btns }

func (b *Bot) parseCommand(text string) (cmd, args string, ignore, named bool) {
	if !strings.HasPrefix(text, "/") {
		return "", "", false, false
	}
	first, rest, _ := strings.Cut(text, " ")
	name, target, hasTarget := strings.Cut(first, "@")
	if hasTarget && !strings.EqualFold(target, b.me.Username) {
		return "", "", true, false
	}
	return strings.ToLower(name), strings.TrimSpace(rest), false, hasTarget
}

var wakeRe = regexp.MustCompile(`^(?i)\s*(?:م(?:ا)?[کك]رون|macron)(?:\s*(?:ai|ای[\s\x{200c}]?آی))?(?:$|[\s\p{P}]+)`)

const (
	textOnlyName      = "(فقط اسمم رو صدا زد)"
	textCalledOnReply = "(روی یه پیام ریپلای کرد و صدام زد: یعنی درباره همون پیام کمکش کنم؛ بسته به پیام، جوابش رو بدم، توضیحش بدم، ترجمه‌اش کنم، خلاصه‌اش کنم یا نظرم رو بگم)"
)

func (b *Bot) calledOnly(m *telegram.Message) string {
	if m.Replied() != nil {
		return b.langOf(m.From).t(textCalledOnReply)
	}
	return b.langOf(m.From).t(textOnlyName)
}

func (b *Bot) addressed(m *telegram.Message) (bool, string) {
	text := m.Body()
	if loc := wakeRe.FindStringIndex(text); loc != nil {
		rest := strings.TrimSpace(text[loc[1]:])
		if rest == "" && !m.HasMedia() {
			rest = b.calledOnly(m)
		}
		return true, rest
	}
	if r := m.RepliedInChat(); r != nil && r.From != nil && r.From.ID == b.me.ID {
		return true, text
	}
	if b.me.Username != "" && strings.Contains(strings.ToLower(text), "@"+strings.ToLower(b.me.Username)) {
		rest := stripMention(text, b.me.Username)
		if rest == "" && !m.HasMedia() {
			rest = b.calledOnly(m)
		}
		return true, rest
	}
	return false, text
}

func userInfo(u *telegram.User) store.UserInfo {
	return store.UserInfo{ID: u.ID, Username: u.Username, FirstName: u.FirstName, LastName: u.LastName,
		LanguageCode: u.LanguageCode, IsPremium: u.IsPremium}
}

func (b *Bot) handleMessage(m *telegram.Message) {
	if m.From == nil || m.From.IsBot || m.Chat.Type == "channel" || !m.Chat.Private() {
		return
	}

	if m.MediaGroupID != "" && !m.AlbumDone {
		b.albums.add(m, &b.wg, func(items []*telegram.Message) { b.handleMessage(mergeAlbum(items)) })
		return
	}

	cmd, args, ignore, _ := b.parseCommand(m.Text)
	if ignore {
		return
	}
	text := m.Body()
	ctx, cancel := b.ctx(20 * time.Second)
	defer cancel()

	l := b.langOf(m.From)
	user, _, err := b.st.TouchUser(ctx, userInfo(m.From))
	if err != nil {
		log.Printf("touch user %d: %v", m.From.ID, err)
		b.reply(m, l.t(textError))
		return
	}
	uid := m.From.ID
	admin := b.isAdmin(uid)

	if admin && m.Chat.Private() {

		actx, acancel := b.ctx(90 * time.Second)
		handled := b.handleAdminInput(actx, m)
		acancel()
		if handled {
			return
		}
	}

	if cmd == "/id" {
		b.reply(m, l.f("🆔 شناسه تو: <code>%d</code>\nشناسه این گفتگو: <code>%d</code>", uid, m.Chat.ID))
		return
	}

	if b.cookieInput(ctx, m, cmd) {
		return
	}

	opt := promptOptions{}
	switch cmd {
	case "":
	case "/start":
		b.reply(m, l.f(personalWelcome, esc(m.From.FirstName), greeting(l, b.st.Now().In(b.loc))))
		return
	case "/queue":
		text = strings.TrimSpace(args)
		if text == "" && !m.HasMedia() {
			b.reply(m, l.t("متن کار بعدی را بعد از /queue بنویس."))
			return
		}
		if b.interjectMode(user, m, text, true) {
			return
		}
	case "/help":
		b.reply(m, l.t(personalHelp))
		return
	case "/language":
		b.sendLanguage(m, l)
		return
	case "/me":
		b.sendMe(ctx, m, user)
		return
	case "/new":
		if err := b.mem.Reset(ctx, uid); err != nil {
			b.reply(m, l.t(textError))
			return
		}
		b.reply(m, l.t(textMemoryReset))
		return
	case "/memory":
		b.openMemoryPanel(ctx, m)
		return
	case "/tasks":
		b.sendTasks(ctx, m, uid)
		return
	case "/mode":
		b.sendMode(ctx, m)
		return
	case "/files":
		b.openFilePanel(ctx, m)
		return
	case "/cookies":
		b.openCookiesPanel(ctx, m)
		return
	case "/reminders":
		b.sendSchedules(ctx, m, uid)
		return
	case "/cancel":
		if r, ok := b.runs.Load(uid); ok {
			r.(*activeRun).stop()
			return
		}
		b.reply(m, l.t(textNothingToCancel))
		return
	case "/settings":
		if admin {
			b.openPanel(ctx, m)
			return
		}
		b.reply(m, l.t(textUnknownCommand))
		return
	case "/teach":
		if admin {
			b.teachCommand(ctx, m, l, args)
			return
		}
		b.reply(m, l.t(textUnknownCommand))
		return
	case "/approve":
		if admin && b.approveLatest(uid) {
			return
		}
		b.reply(m, l.t("[!] دستوری منتظر تأیید نیست."))
		return
	case "/yolo":
		if b.isOwner(uid) && m.Chat.Private() {
			b.yoloCommand(m, l, args)
			return
		}
		b.reply(m, l.t(textUnknownCommand))
		return
	case "/board":
		if !b.isOwner(uid) || !m.Chat.Private() {
			b.reply(m, l.t(textUnknownCommand))
			return
		}
		b.boardCommand(ctx, m, l)
		return
	case "/dashboard":
		if !b.isOwner(uid) || !m.Chat.Private() {
			b.reply(m, l.t(textUnknownCommand))
			return
		}
		b.dashboardCommand(ctx, m, l)
		return
	case "/search":
		if !b.isOwner(uid) || !m.Chat.Private() {
			b.reply(m, l.t(textUnknownCommand))
			return
		}
		b.searchCommand(ctx, m, l, args)
		return
	case "/update":
		if !b.isOwner(uid) || !m.Chat.Private() {
			b.reply(m, l.t(textUnknownCommand))
			return
		}
		b.showNew(ctx, m.Chat.ID, b.updateView(ctx, l, "a:home"))
		return
	case "/moa":
		if !admin || !m.Chat.Private() {
			b.reply(m, l.t(textUnknownCommand))
			return
		}
		if !b.moaCommand(m, l, args) {
			return
		}
		text, opt.moa = strings.TrimSpace(args), moaDefaultName
	case "/learn":
		if admin && m.Chat.Private() {
			b.learnCommand(ctx, m, l, args)
			return
		}
		b.reply(m, l.t(textUnknownCommand))
		return
	default:
		b.reply(m, l.t(textUnknownCommand))
		return
	}

	if cmd == "" && b.interject(user, m, text) {
		return
	}
	b.handlePrompt(user, m, text, opt)
}

func humanInt(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fK", float64(n)/1e3)
	}
	return strconv.Itoa(n)
}

func sessionFor(userID int64) string { return "tg-" + strconv.FormatInt(userID, 10) }

func (b *Bot) handleMembership(u *telegram.ChatMemberUpdated) {
	if !u.Chat.Private() {
		return
	}
	ctx, cancel := b.ctx(10 * time.Second)
	defer cancel()
	blocked := u.NewChatMember.Status == "kicked"
	if err := b.st.SetBlockedBot(ctx, u.From.ID, blocked); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("membership update: %v", err)
	}
}

func (b *Bot) handleStop(s *telegram.MessageGenerationStopped) {
	var owner draftOwner
	found := false
	if s.DraftID != 0 {
		if v, ok := b.drafts.Load(int64(s.DraftID)); ok {
			owner, found = v.(draftOwner), true
		}
	} else if s.Chat.Private() {
		b.drafts.Range(func(_, v any) bool {
			if d := v.(draftOwner); d.chatID == s.Chat.ID {
				owner, found = d, true
				return false
			}
			return true
		})
	}
	if !found || owner.chatID != s.Chat.ID {
		log.Printf("[chat %d] stop for draft %d, which shows no job now: ignored", s.Chat.ID, s.DraftID)
		return
	}
	if v, ok := b.runs.Load(owner.userID); ok {
		if r := v.(*activeRun); r.key == owner.key && r.chatID == s.Chat.ID {
			r.stop()
		}
	}
}

func (b *Bot) handleCallback(q *telegram.CallbackQuery) {
	if q.Message != nil && !q.Message.Chat.Private() {
		return
	}
	ctx, cancel := b.ctx(60 * time.Second)
	defer cancel()
	answer := func(text string, alert bool) { _ = b.api.AnswerCallbackQuery(ctx, q.ID, text, alert) }

	if q.Message == nil {
		answer(b.langOf(&q.From).t("این دکمه دیگه کار نمی‌کنه."), false)
		return
	}
	if b.foreignPanel(q) {
		answer(b.langOf(&q.From).t(textNotYourPanel), true)
		return
	}
	switch {
	case strings.HasPrefix(q.Data, "lang:"):
		b.handleLanguageCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "run:stop:"):
		b.handleRunCallback(q, answer)
	case strings.HasPrefix(q.Data, "run:go:"):
		b.handleContinueCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "bg:"):
		b.handleBgCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "mode:"):
		b.handleModeCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "sc:"):
		b.handleScheduleCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "ga:"), strings.HasPrefix(q.Data, "gl:"):
		answer("", false)
	case strings.HasPrefix(q.Data, "cg:"):
		b.handleGateCallback(q, answer)
	case strings.HasPrefix(q.Data, "up:go:"):
		b.handleUpdateCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "me:"):
		b.handleMeCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "m:"):
		b.handleMemoryCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "f:"):
		b.handleFilesCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "ck:"):
		b.handleCookiesCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "a:"):
		if !b.isAdmin(q.From.ID) {
			answer("⛔", true)
			return
		}
		b.handlePanel(ctx, q, answer)
	default:
		answer("", false)
	}
}
