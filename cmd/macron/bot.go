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

// Bot ties Telegram, the agent, the database and the admin panel together.
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
	envWindow     int // WAZE_CONTEXT_WINDOW (0: unset)
	langs         langs
	emojis        atomic.Pointer[emoji.Set]
	richOff       atomic.Bool  // the Bot API server does not support rich messages
	stopRowOff    atomic.Bool  // nor buttons inside them (the live message's stop button)
	mediaStrikes  atomic.Int32 // rich messages with files that failed in a row
	mediaFailedAt atomic.Int64 // unix time of the last failure

	inputs      sync.Map        // adminID → *adminInput (multi-step admin dialogs)
	runs        sync.Map        // userID → *activeRun
	bgRuns      sync.Map        // background task id → *activeRun (see bgtasks.go)
	resumes     sync.Map        // userID → time of a ▶️ press (see openjob.go)
	drafts      sync.Map        // draftID → draftOwner (the job a draft's stop button stops)
	fileJobs    sync.Map        // userID → a /files upload in progress
	cookieWaits sync.Map        // userID → time.Time until which their next message is a cookies.txt (/cookies)
	groupLogs   sync.Map        // chatID → groupLogState
	approvals   sync.Map        // chatID → approvalState
	panels      sync.Map        // "chat:message" → panelOwner (group panels answer only their owner)
	fb          feedbackState   // reactions, complaints and repeats (feedback.go)
	evalBusy    atomic.Bool     // an exam is running (evals.go)
	taught      teachCache      // the always-rules the admins taught, for every prompt (teach.go)
	usage       usageCache      // what the workspaces take (diskusage.go)
	sys         *sysmon.Sampler // the server's CPU, memory and disk (sysmon_panel.go)
	sysOnce     sync.Once

	cookies *cookiePool      // the downloaders' cookies (admin panel)
	plugins *plugins.Manager // the admins' plugins and MCP servers (plugins_admin.go)

	guestAnswered sync.Map    // guest_query_id → answered (one reply per query)
	guestReplies  sync.Map    // guest_query_id → *guestReply (its «thinking» placeholder)
	guestStubOff  atomic.Bool // the server gives no inline_message_id: no placeholder
	skillMu       sync.Mutex  // learnToolSkill's read and write of a provider's counts
	routes        sync.Map    // user id → routeMemo (their last routed request, modelroute.go)
	triage        triageState // what a request needs, decided before the model works (triage.go)
	// toldGroupSettings: the groups already told that /settings is not their settings
	toldGroupSettings sync.Map
	suspended         sync.Map    // poll ID → suspension: jobs waiting for a poll's answer (polls.go)
	readsAll          atomic.Bool // privacy mode is off: every group message reaches the bot (groupsight.go)
	sights            sync.Map    // chatID → sight (whether the bot sees all of a group)
	sightHints        sync.Map    // chatID → when its admins were last told the bot misses messages

	ffmpegOnce sync.Once
	ffmpeg     bool // ffmpeg runs here (frame previews)

	jobCtx  context.Context // cancelled only when shutdown is forced
	wg      sync.WaitGroup
	started time.Time

	brain *brain.Brain // SOUL.md, USER.md and MEMORY.md (the agent's home folder)
	// The personal edition (personal.go): it answers its owners and nobody else.
	personal  bool
	workFiles time.Duration // how long the files in the workspaces are kept (0: forever)
	strangers sync.Map      // user id → time.Time of the last note in the log
}

func (b *Bot) isOwner(id int64) bool { return b.owner[id] }
func (b *Bot) isAdmin(id int64) bool { return b.owner[id] || b.st.IsAdmin(id) }

// reloadEmoji refreshes the premium emoji set from the database.
// reloadEmoji loads the premium emoji mappings and deletes extra ones: a
// second mapping of the same emoji ("❤" next to "❤️") or of the same premium
// emoji. The oldest one stays.
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

// premium returns the active premium emoji set (nil when disabled).
func (b *Bot) premium() *emoji.Set {
	if !b.st.SettingBool(store.SettingPremiumEmoji, false) {
		return nil
	}
	return b.emojis.Load()
}

func (b *Bot) ctx(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(b.jobCtx, d)
}

// dispatch handles one update on its own goroutine; a panic never takes the bot down.
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
		case u.EditedMessage != nil:
			b.handleEdit(u.EditedMessage)
		case u.MessageReaction != nil:
			b.handleReaction(u.MessageReaction)
		}
	}()
}

// reply sends HTML (with premium emoji) as a reply.
func (b *Bot) reply(m *telegram.Message, html string) *telegram.Message {
	ctx, cancel := b.ctx(30 * time.Second)
	defer cancel()
	return b.sendHTML(ctx, m.Chat.ID, html, telegram.SendOptions{ReplyTo: m.MessageID})
}

func (b *Bot) sendHTML(ctx context.Context, chatID int64, html string, opts telegram.SendOptions) *telegram.Message {
	html = b.bidi(chatID, html)
	if set := b.premium(); set != nil {
		if sent, err := b.api.SendHTML(ctx, chatID, set.HTML(html), opts); err == nil {
			return sent
		}
		// Custom emoji may be refused (owner without Premium): fall back to plain emoji.
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

// button builds an inline button; a leading premium-mapped emoji becomes its icon.
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

// bidi lays a Persian message out right to left (telegram.MarkRTL): in a
// private chat when its user reads Persian, elsewhere (groups, the log
// channel) when most of its letters are Persian.
func (b *Bot) bidi(chatID int64, html string) string {
	if !hasPersian(html) {
		return html
	}
	if chatID > 0 && b.lang(chatID) != fa || chatID < 0 && !telegram.IsMostlyRTL(textutil.StripHTML(html)) {
		return html
	}
	return telegram.MarkRTL(html)
}

// bidiLabel lays a mostly Persian button label out right to left.
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

// parseCommand splits "/cmd@botname args". named: it names this bot
// (/start@ThisBot); ignore: it names another bot.
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

// botCommands are the commands this bot knows. In a group, another command
// without @ThisBot is for another bot there (/warn, /ban…): it is ignored,
// not answered with "unknown command".
var botCommands = map[string]bool{}

func init() {
	for _, c := range strings.Fields("/admin /cancel /clear /cookie /cookies /deep /fast /files /groupsettings /help /lang /language /learn /me /memory " +
		"/mode /new /nolog /panel /recap /reminders /reset /schedules /settings /start /stop /tasks /teach /zaban") {
		botCommands[c] = true
	}
}

// wakeRe matches the name the bot is called by at the start of a group
// message: "مکرون …", "ماکرون، …", "Macron …", "Macron Ai …". "مکرونی" does not match.
var wakeRe = regexp.MustCompile(`^(?i)\s*(?:م(?:ا)?[کك]رون|macron)(?:\s*(?:ai|ای[\s\x{200c}]?آی))?(?:$|[\s\p{P}]+)`)

// textOnlyName is the prompt when someone only calls the bot by its name;
// textCalledOnReply when they do it on a reply to a message (anyone's).
const (
	textOnlyName      = "(فقط اسمم رو صدا زد)"
	textCalledOnReply = "(روی یه پیام ریپلای کرد و صدام زد: یعنی درباره همون پیام کمکش کنم؛ بسته به پیام، جوابش رو بدم، توضیحش بدم، ترجمه‌اش کنم، خلاصه‌اش کنم یا نظرم رو بگم)"
)

// calledOnly is the prompt when a message only calls the bot (its name or
// @mention, nothing else): about the message it replies to, if any.
func (b *Bot) calledOnly(m *telegram.Message) string {
	if m.Replied() != nil {
		return b.langOf(m.From).t(textCalledOnReply)
	}
	return b.langOf(m.From).t(textOnlyName)
}

// addressed reports whether a group message is meant for the bot (it starts
// with "مکرون", mentions the bot or replies to it) and returns the text without
// the name or the mention.
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
			rest = b.calledOnly(m) // "@bot" alone on someone's message: that message is the request
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
	if m.From == nil || m.From.IsBot || m.Chat.Type == "channel" {
		return
	}
	// A group waits for an admin's approval before the bot works there; groups
	// that turned on catch-up summaries keep their messages for a while.
	if !m.Chat.Private() {
		actx, acancel := b.ctx(5 * time.Second)
		active := b.groupActive(actx, m.Chat.ID)
		acancel()
		if !active {
			return
		}
		if !m.AlbumDone {
			b.logGroupMessage(m) // an album's items were logged one by one already
		}
		if m.FromChannel() {
			return // a channel's post is read for catch-up summaries, never answered as a person's
		}
	}
	// The items of an album are handled together, as one message (album.go).
	if m.MediaGroupID != "" && !m.AlbumDone {
		b.albums.add(m, &b.wg, func(items []*telegram.Message) { b.handleMessage(mergeAlbum(items)) })
		return
	}
	// In groups only messages meant for the bot count; members who never
	// talked to it are not users.
	cmd, args, ignore, named := b.parseCommand(m.Text)
	if ignore || (cmd != "" && !m.Chat.Private() && !named && !botCommands[cmd]) {
		return
	}
	text := m.Body()
	if !m.Chat.Private() && cmd == "" {
		ok, stripped := b.addressed(m)
		if !ok && !b.repliesToOwnJob(m) {
			return
		}
		text = stripped
	}
	if !m.Chat.Private() {
		go b.checkSight(m) // a group where the bot misses «مکرون …» is told how to fix it
	}

	ctx, cancel := b.ctx(20 * time.Second)
	defer cancel()
	if !m.Chat.Private() {
		b.touchChat(m.Chat)
	}

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
		// Dialog steps may test a provider connection (up to 45 s).
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
	case "/recap":
		if m.Chat.Private() {
			b.reply(m, l.t("این دستور توی گروه‌ها کار می‌کنه: خلاصه چیزهایی که از گروه از دست دادی."))
			return
		}
		if b.groupLogFor(ctx, m.Chat.ID).hours == 0 {
			b.reply(m, l.t(textGroupLogOff))
			return
		}
		text = strings.TrimSpace(l.t("چی از دستم رفت؟") + " " + args)
		opt.about = "The user ran /recap: summarize what they missed in this group with " + toolGroupHistory + " (since their last message unless they asked otherwise)."
	case "/groupsettings":
		b.openGroupSettings(ctx, m)
		return
	case "/nolog":
		b.toggleNoLog(ctx, m)
		return
	case "/start":
		b.reply(m, l.f(personalWelcome, esc(m.From.FirstName), greeting(l, b.st.Now().In(b.loc))))
		return
	case "/help":
		b.reply(m, l.t(personalHelp))
		return
	case "/language", "/lang", "/zaban":
		b.sendLanguage(m, l)
		return
	case "/me":
		b.sendMe(ctx, m, user)
		return
	case "/new", "/reset", "/clear":
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
	case "/mode", "/fast", "/deep":
		switch cmd {
		case "/fast", "/deep":
			if err := b.setMode(ctx, uid, cmd == "/fast"); err != nil {
				b.reply(m, l.t(textError))
				return
			}
		}
		b.sendMode(ctx, m)
		return
	case "/files":
		b.openFilePanel(ctx, m)
		return
	case "/cookies", "/cookie":
		b.openCookiesPanel(ctx, m)
		return
	case "/reminders", "/schedules":
		b.sendSchedules(ctx, m, uid)
		return
	case "/cancel", "/stop":
		if r, ok := b.runs.Load(uid); ok {
			r.(*activeRun).stop()
			return
		}
		b.reply(m, l.t(textNothingToCancel))
		return
	case "/settings", "/panel", "/admin": // /admin: the old name, kept for scripts, not in the menu
		if !m.Chat.Private() {
			// A group's settings are /groupsettings: said once per group, then quiet.
			if _, told := b.toldGroupSettings.LoadOrStore(m.Chat.ID, true); !told {
				b.reply(m, l.t("⚙️ تنظیمات گروه با /groupsettings باز می‌شه؛ /settings پنل مدیریت ربات توی پیویه."))
			}
			return
		}
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
	// A message sent while the user's job runs joins that job (or stops it).
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

// handleMembership tracks users who block or unblock the bot, and the
// groups and channels it is added to or removed from.
func (b *Bot) handleMembership(u *telegram.ChatMemberUpdated) {
	if !u.Chat.Private() {
		b.handleGroupMembership(u)
		return
	}
	ctx, cancel := b.ctx(10 * time.Second)
	defer cancel()
	blocked := u.NewChatMember.Status == "kicked"
	if err := b.st.SetBlockedBot(ctx, u.From.ID, blocked); err != nil && !errors.Is(err, store.ErrNotFound) {
		log.Printf("membership update: %v", err)
	}
}

// handleStop cancels a run when the user presses the draft's stop button.
// Drafts only exist in private chats, so when the draft id is unknown (or
// arrived in a shape we could not parse) the chat id is the user id.
// handleStop stops the job whose draft the user stopped. A draft that shows
// no job now (an old one, another job's) never stops the job running now:
// only the job it was made for. A draft_id that did not decode (0) stands
// for the draft this private chat shows now, if it has a stop button.
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
	case strings.HasPrefix(q.Data, "ga:"):
		b.handleApprovalCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "me:"):
		b.handleMeCallback(ctx, q, answer)
	case strings.HasPrefix(q.Data, "gl:"):
		b.handleGroupSettingsCallback(ctx, q, answer)
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
