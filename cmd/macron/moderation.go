package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// group_admin: the agent moderates a group in plain words ("mute him for an
// hour", "ban everyone who posted that link", "lock the group until
// morning"). It is offered only to the bot's admins and the group's own
// admins, and every action also needs the matching admin right, of the
// person asking (unless they are a bot admin or the group's creator) and of
// the bot. Admins, the group's creator, bot admins and the bot itself are
// never targets.

const (
	toolGroupAdmin = "group_admin"
	warnLimit      = 3
	warnMute       = 24 * time.Hour
	maxModTargets  = 25
	maxPurge       = 500
	prefWarnPrefix = "w:"
	prefLockPerms  = "lockperms"
)

// modRight is the admin right an action needs ("" = any admin).
var modRight = map[string]string{
	"ban": "restrict", "unban": "restrict", "kick": "restrict", "mute": "restrict", "unmute": "restrict", "restrict": "restrict",
	"warn": "restrict", "unwarn": "restrict", "delete": "delete", "purge": "delete", "pin": "pin", "unpin": "pin", "lock": "info",
	"unlock": "info", "set_title": "info", "set_description": "info", "promote": "promote", "demote": "promote", "title": "promote",
	"set_rights": "promote", "invite_link": "invite", "warnings": "", "info": "",
}

var modActions = []string{"ban", "unban", "kick", "mute", "unmute", "restrict", "warn", "unwarn", "warnings", "info", "delete", "purge",
	"pin", "unpin", "lock", "unlock", "promote", "demote", "title", "set_rights", "invite_link", "set_title", "set_description"}

// targetActions act on members.
var targetActions = map[string]bool{"ban": true, "unban": true, "kick": true, "mute": true, "unmute": true, "restrict": true, "warn": true,
	"unwarn": true, "warnings": true, "info": true, "promote": true, "demote": true, "title": true, "set_rights": true}

// punishing actions never touch admins.
var punishing = map[string]bool{"ban": true, "kick": true, "mute": true, "warn": true, "restrict": true}

// adminChanges change who is an admin and what they may do.
var adminChanges = map[string]bool{"promote": true, "demote": true, "title": true, "set_rights": true}

func hasRight(cm *telegram.ChatMember, right string) bool {
	if cm == nil {
		return false
	}
	if cm.Status == "creator" {
		return true
	}
	if cm.Status != "administrator" {
		return false
	}
	switch right {
	case "":
		return true
	case "restrict":
		return cm.CanRestrictMembers
	case "delete":
		return cm.CanDeleteMessages
	case "pin":
		return cm.CanPinMessages
	case "info":
		return cm.CanChangeInfo
	case "promote":
		return cm.CanPromoteMembers
	case "invite":
		return cm.CanInviteUsers
	}
	return false
}

var rightNames = map[string]string{"restrict": "ban users", "delete": "delete messages", "pin": "pin messages", "info": "change group info",
	"promote": "add new admins", "invite": "invite users via link"}

// moderator is who may use group_admin in this chat: a bot admin or an admin of the group.
func (b *Bot) moderator(ctx context.Context, m *telegram.Message) bool {
	if m.Chat.Private() || m.GuestQueryID != "" || m.From == nil {
		return false
	}
	return b.isGroupAdmin(ctx, m.Chat.ID, m.From.ID)
}

func (b *Bot) groupAdminTool(m *telegram.Message) agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolGroupAdmin,
			Description: "Moderate this group, only when its admin asks (the person asking is one). The target is the replied message's " +
				"author unless users names @username or a numeric id. promote: rights = a preset (moderator, full, helper, cleaner) or rights " +
				"(delete_messages, pin_messages, restrict_members, invite_users, change_info, manage_topics, manage_video_chats, " +
				"promote_members, anonymous, post_stories), changes like '+pin', '-delete', '+ban'; text = admin title (at most 16 " +
				"characters, no emoji). demote: takes every admin right. title: sets the admin title (a member is made a moderator " +
				"first). set_rights: '+right'/'-right' changes an admin's rights, keeping the others. restrict: permissions like '-media', " +
				"'-voice', '-photos', '-videos', '-stickers', '-links', '+messages', with an optional duration. info: status, title, " +
				"admin rights or what a restricted member may send, and warnings. warn (" + strconv.Itoa(warnLimit) + " warnings mute " +
				"for 24h), unwarn (the last one; text 'all': every one), warnings. ban (delete_messages, duration), unban, kick, mute " +
				"(duration), unmute, delete, purge (count), pin, unpin, lock, unlock, invite_link, set_title, set_description. Nobody " +
				"gets a right the asker or the bot doesn't have.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"action":          map[string]any{"type": "string", "enum": modActions},
				"users":           map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "'reply' (the replied message's author, the default), @username or a numeric id"},
				"rights":          map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "promote / set_rights: a preset, right names, or '+right' / '-right'"},
				"permissions":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "restrict: '-media', '-voice', '-photos', '-videos', '-stickers', '-links', '+messages', 'all'…"},
				"duration":        map[string]any{"type": "string", "description": "e.g. '30m', '2h', '1d', '1w'; none: for ever (invite_link: expiry)"},
				"reason":          map[string]any{"type": "string"},
				"count":           map[string]any{"type": "integer", "description": "purge: the last N messages; invite_link: member limit"},
				"text":            map[string]any{"type": "string", "description": "promote/title: the admin title; set_title/set_description: the text; invite_link: its name; unpin/unwarn: 'all'"},
				"delete_messages": map[string]any{"type": "boolean", "description": "ban: delete their messages too"},
			}, "required": []string{"action"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			return agent.ToolOutput{Text: b.moderate(ctx, m, args)}, nil
		},
	}
}

func (b *Bot) moderate(ctx context.Context, m *telegram.Message, args map[string]any) string {
	action, _ := args["action"].(string)
	right, known := modRight[action]
	if !known {
		return "error: unknown action; use one of " + strings.Join(modActions, ", ")
	}
	chatID := m.Chat.ID
	var asker *telegram.ChatMember
	if !b.isAdmin(m.From.ID) {
		cm, err := b.api.GetChatMember(ctx, chatID, m.From.ID)
		if err != nil {
			return "error: could not check the asker's rights: " + err.Error()
		}
		if !hasRight(cm, right) {
			if cm.Status != "creator" && cm.Status != "administrator" {
				return "refused: only the group's admins (or the bot's admins) can do this"
			}
			return fmt.Sprintf("refused: %s needs the admin right to %s, which the asker does not have", action, rightNames[right])
		}
		asker = cm
	}
	var me *telegram.ChatMember
	if right != "" {
		var err error
		me, err = b.api.GetChatMember(ctx, chatID, b.me.ID)
		if err != nil || !hasRight(me, right) {
			return fmt.Sprintf("cannot: I need to be an admin of this group with the right to %s (ask the group's owner to give it to me)", rightNames[right])
		}
	}
	text := strings.TrimSpace(fmt.Sprint(orEmpty(args["text"])))
	reason := strings.TrimSpace(fmt.Sprint(orEmpty(args["reason"])))
	count := 0
	if n, ok := args["count"].(float64); ok {
		count = int(n)
	}
	switch action {
	case "delete":
		if m.RepliedInChat() == nil {
			return "error: reply to the message to delete"
		}
		if err := b.api.DeleteMessage(ctx, chatID, m.RepliedInChat().MessageID); err != nil {
			return "failed: " + modError(err)
		}
		return "✅ deleted the replied message"
	case "purge":
		return b.purge(ctx, m, args, count)
	case "pin":
		if m.RepliedInChat() == nil {
			return "error: reply to the message to pin"
		}
		if err := b.api.PinChatMessage(ctx, chatID, m.RepliedInChat().MessageID, false); err != nil {
			return "failed: " + modError(err)
		}
		return "✅ pinned"
	case "unpin":
		var err error
		switch {
		case strings.EqualFold(text, "all"):
			err = b.api.UnpinAllChatMessages(ctx, chatID)
		case m.RepliedInChat() != nil:
			err = b.api.UnpinChatMessage(ctx, chatID, m.RepliedInChat().MessageID)
		default:
			err = b.api.UnpinChatMessage(ctx, chatID, 0)
		}
		if err != nil {
			return "failed: " + modError(err)
		}
		return "✅ unpinned"
	case "lock", "unlock":
		return b.lockGroup(ctx, chatID, action == "lock")
	case "set_title":
		if text == "" || len([]rune(text)) > 128 {
			return "error: text is the new title (1-128 characters)"
		}
		if err := b.api.SetChatTitle(ctx, chatID, text); err != nil {
			return "failed: " + modError(err)
		}
		return "✅ the group is now called " + text
	case "set_description":
		if len([]rune(text)) > 255 {
			return "error: the description takes at most 255 characters"
		}
		if err := b.api.SetChatDescription(ctx, chatID, text); err != nil {
			return "failed: " + modError(err)
		}
		return "✅ description updated"
	case "invite_link":
		var expire time.Time
		if d, err := parseModDuration(fmt.Sprint(orEmpty(args["duration"]))); err != nil {
			return "error: " + err.Error()
		} else if d > 0 {
			expire = time.Now().Add(d)
		}
		link, err := b.api.CreateChatInviteLink(ctx, chatID, textOr(text, "Macron"), expire, min(max(count, 0), 99999), false)
		if err != nil {
			return "failed: " + modError(err)
		}
		note := ""
		if !expire.IsZero() {
			note += ", expires " + expire.In(b.loc).Format("2006-01-02 15:04")
		}
		if count > 0 {
			note += fmt.Sprintf(", for %d people", count)
		}
		return "✅ invite link" + note + ": " + link
	}

	// The rest act on members.
	targets, problems := b.modTargets(ctx, m, args)
	if len(targets) == 0 {
		if len(problems) == 0 {
			problems = []string{"no one to act on: reply to their message, or give @username or a numeric id in users"}
		}
		return strings.Join(problems, "\n")
	}
	var d time.Duration
	if action == "ban" || action == "mute" || action == "restrict" {
		var err error
		if d, err = parseModDuration(fmt.Sprint(orEmpty(args["duration"]))); err != nil {
			return "error: " + err.Error()
		}
	}
	lines := problems
	for _, u := range targets {
		lines = append(lines, b.moderateOne(ctx, m, asker, me, action, u, d, reason, text, args))
	}
	return strings.Join(lines, "\n")
}

func textOr(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// modTarget is a member to act on.
type modTarget struct {
	id   int64
	name string
}

func (t modTarget) label() string {
	if t.name == "" {
		return strconv.FormatInt(t.id, 10)
	}
	return fmt.Sprintf("%s (%d)", t.name, t.id)
}

var modIDRe = regexp.MustCompile(`^-?\d{1,15}$`)

// modTargets resolves users: the replied message's author, @usernames (people
// the bot has seen), numeric ids and members mentioned by name in the message.
func (b *Bot) modTargets(ctx context.Context, m *telegram.Message, args map[string]any) ([]modTarget, []string) {
	var raw []string
	if list, ok := args["users"].([]any); ok {
		for _, v := range list {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
				raw = append(raw, strings.TrimSpace(s))
			}
		}
	}
	if len(raw) == 0 {
		raw = []string{"reply"}
	}
	mentions := map[string]telegram.User{} // text mentions of people without a username
	for _, e := range m.BodyEntities() {
		if e.Type == "text_mention" && e.User != nil {
			mentions[strings.ToLower(utf16Slice(m.Body(), e.Offset, e.Length))] = *e.User
		}
	}
	var out []modTarget
	var problems []string
	seen := map[int64]bool{}
	add := func(t modTarget) {
		if !seen[t.id] && len(out) < maxModTargets {
			seen[t.id] = true
			out = append(out, t)
		}
	}
	for _, r := range raw {
		key := strings.ToLower(r)
		switch {
		case key == "reply" || key == "replied":
			if m.RepliedInChat() == nil || m.RepliedInChat().From == nil {
				problems = append(problems, "⚠️ no replied message: reply to the person's message, or name them with @username or their id")
				continue
			}
			from := m.RepliedInChat().From
			if m.RepliedInChat().SenderChat != nil && from.ID == 1087968824 { // an anonymous admin
				problems = append(problems, "⚠️ the replied message was sent anonymously by an admin")
				continue
			}
			add(modTarget{from.ID, from.FullName()})
		case modIDRe.MatchString(r):
			id, _ := strconv.ParseInt(r, 10, 64)
			add(modTarget{id: id})
		case strings.HasPrefix(r, "@"):
			if t, ok := b.userByUsername(ctx, m.Chat.ID, strings.TrimPrefix(r, "@")); ok {
				add(t)
			} else {
				problems = append(problems, "⚠️ I don't know "+r+" yet (they haven't written where I could see); reply to one of their messages instead")
			}
		default:
			if u, ok := mentions[key]; ok {
				add(modTarget{u.ID, u.FullName()})
			} else {
				problems = append(problems, "⚠️ "+r+" is not a user I can find: use 'reply', @username or a numeric id")
			}
		}
	}
	return out, problems
}

func (b *Bot) userByUsername(ctx context.Context, chatID int64, username string) (modTarget, bool) {
	if users, err := b.st.FindUsers(ctx, "@"+username, 1); err == nil && len(users) == 1 {
		u := users[0]
		return modTarget{u.ID, strings.TrimSpace(u.FirstName + " " + u.LastName)}, true
	}
	if id, name, err := b.st.GroupMemberByUsername(ctx, chatID, username); err == nil && id != 0 {
		return modTarget{id, name}, true
	}
	return modTarget{}, false
}

// utf16Slice cuts text by the UTF-16 offsets Telegram uses.
func utf16Slice(s string, offset, length int) string {
	u := utf16.Encode([]rune(s))
	if offset < 0 || offset+length > len(u) {
		return ""
	}
	return string(utf16.Decode(u[offset : offset+length]))
}

func (b *Bot) moderateOne(ctx context.Context, m *telegram.Message, asker, me *telegram.ChatMember, action string, t modTarget, d time.Duration,
	reason, text string, args map[string]any) string {
	chatID := m.Chat.ID
	switch {
	case t.id == b.me.ID:
		return "⚠️ " + t.label() + ": that is me"
	case t.id == m.From.ID && punishing[action]:
		return "⚠️ " + t.label() + ": not on yourself"
	case b.isAdmin(t.id) && punishing[action]:
		return "⚠️ " + t.label() + ": a bot admin, protected"
	case b.isAdmin(t.id) && action != "info" && action != "warnings" && !b.isAdmin(m.From.ID): // only a bot admin changes another's role
		return "⚠️ " + t.label() + ": a bot admin, protected"
	}
	member, err := b.api.GetChatMember(ctx, chatID, t.id)
	if err == nil && t.name == "" {
		t.name = member.User.FullName()
	}
	isStaff := err == nil && (member.Status == "creator" || member.Status == "administrator")
	if punishing[action] && isStaff {
		return "⚠️ " + t.label() + ": an admin of this group; demote them first"
	}
	if adminChanges[action] {
		if err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return b.changeAdmin(ctx, chatID, asker, me, action, t, member, strList(args["rights"]), text)
	}
	until := func() time.Time {
		if d <= 0 {
			return time.Time{}
		}
		return time.Now().Add(d)
	}
	why := ""
	if reason != "" {
		why = " — " + reason
	}
	switch action {
	case "ban":
		del, _ := args["delete_messages"].(bool)
		if err := b.api.BanChatMember(ctx, chatID, t.id, until(), del); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return "✅ banned " + t.label() + " " + forText(d) + why
	case "unban":
		if err := b.api.UnbanChatMember(ctx, chatID, t.id); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return "✅ unbanned " + t.label() + " (they can join again)"
	case "kick":
		if err := b.api.BanChatMember(ctx, chatID, t.id, time.Now().Add(time.Minute), false); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		_ = b.api.UnbanChatMember(ctx, chatID, t.id)
		return "✅ removed " + t.label() + " (they can join again)" + why
	case "mute":
		if err := b.api.RestrictChatMember(ctx, chatID, t.id, telegram.ChatPermissions{}, until()); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return "✅ muted " + t.label() + " " + forText(d) + why
	case "unmute":
		if err := b.api.RestrictChatMember(ctx, chatID, t.id, b.groupPermissions(ctx, chatID), time.Time{}); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return "✅ " + t.label() + " can write again"
	case "warn":
		n, reasons := b.warnings(ctx, chatID, t.id)
		n++
		if reason != "" {
			reasons = append(reasons, reason)
		}
		if n >= warnLimit {
			_ = b.setWarnings(ctx, chatID, t.id, 0, nil)
			if err := b.api.RestrictChatMember(ctx, chatID, t.id, telegram.ChatPermissions{}, time.Now().Add(warnMute)); err != nil {
				return fmt.Sprintf("⚠️ %s reached %d warnings, but muting failed: %s", t.label(), n, modError(err))
			}
			return fmt.Sprintf("🔇 %s reached %d warnings and is muted for a day; the count starts over%s", t.label(), n, why)
		}
		if err := b.setWarnings(ctx, chatID, t.id, n, reasons); err != nil {
			return "❌ " + t.label() + ": the warning was not saved"
		}
		return fmt.Sprintf("⚠️ warned %s (%d of %d)%s", t.label(), n, warnLimit, why)
	case "unwarn":
		n, reasons := b.warnings(ctx, chatID, t.id)
		if n == 0 {
			return t.label() + ": no warnings to take back"
		}
		if strings.EqualFold(text, "all") || n == 1 {
			_ = b.setWarnings(ctx, chatID, t.id, 0, nil)
			return "✅ every warning of " + t.label() + " cleared"
		}
		if len(reasons) > 0 {
			reasons = reasons[:len(reasons)-1]
		}
		if err := b.setWarnings(ctx, chatID, t.id, n-1, reasons); err != nil {
			return "❌ " + t.label() + ": not saved"
		}
		return fmt.Sprintf("✅ took back a warning of %s (%d of %d left)", t.label(), n-1, warnLimit)
	case "warnings":
		n, reasons := b.warnings(ctx, chatID, t.id)
		if n == 0 {
			return t.label() + ": no warnings"
		}
		return fmt.Sprintf("%s: %d of %d warnings (%s)", t.label(), n, warnLimit, strings.Join(reasons, "; "))
	case "info":
		if err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return b.memberInfo(ctx, chatID, t, member)
	case "restrict":
		items := strList(args["permissions"])
		if len(items) == 0 {
			return "error: permissions says what to take away or give back, e.g. ['-media', '-links'] or ['+messages']"
		}
		base := b.groupPermissions(ctx, chatID)
		if err == nil && member.Status == "restricted" {
			base = member.Permissions()
		}
		perms, perr := parsePerms(items, base)
		if perr != nil {
			return "error: " + perr.Error()
		}
		if err := b.api.RestrictChatMember(ctx, chatID, t.id, perms, until()); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		allowed, blocked := permsText(perms)
		out := "✅ " + t.label() + " " + forText(d) + ": blocked " + listOr(blocked, "nothing") + "; allowed " + listOr(allowed, "nothing") + why
		if !perms.CanAddWebPagePreviews && base.CanAddWebPagePreviews {
			out += " (links: Telegram can only turn off link previews; it has no switch for the links themselves)"
		}
		return out
	}
	return "error: unknown action"
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func forText(d time.Duration) string {
	if d <= 0 {
		return "for good"
	}
	switch {
	case d%(7*24*time.Hour) == 0:
		return fmt.Sprintf("for %d week(s)", d/(7*24*time.Hour))
	case d%(24*time.Hour) == 0:
		return fmt.Sprintf("for %d day(s)", d/(24*time.Hour))
	case d%time.Hour == 0:
		return fmt.Sprintf("for %d hour(s)", d/time.Hour)
	}
	return fmt.Sprintf("for %d minute(s)", d/time.Minute)
}

var modDurationRe = regexp.MustCompile(`^(\d+)\s*(s|sec|m|min|minutes?|h|hours?|d|days?|w|weeks?|ثانیه|دقیقه|ساعت|روز|هفته|ماه|mo|months?)$`)

// parseModDuration reads '30m', '2h', '1d', '1w', «۲ ساعت»; "" or 'forever' is 0.
// Telegram treats less than 30 seconds and more than 366 days as for ever.
func parseModDuration(s string) (time.Duration, error) {
	s = strings.ToLower(strings.TrimSpace(persianDigits(s)))
	switch s {
	case "", "<nil>", "forever", "permanent", "همیشه", "دائم", "برای همیشه":
		return 0, nil
	}
	m := modDurationRe.FindStringSubmatch(s)
	if m == nil {
		return 0, errors.New("duration must look like '30m', '2h', '1d' or '1w'")
	}
	n, _ := strconv.Atoi(m[1])
	unit := time.Minute
	switch u := m[2]; {
	case u == "s" || u == "sec" || u == "ثانیه":
		unit = time.Second
	case strings.HasPrefix(u, "h") || u == "ساعت":
		unit = time.Hour
	case strings.HasPrefix(u, "d") || u == "روز":
		unit = 24 * time.Hour
	case strings.HasPrefix(u, "w") || u == "هفته":
		unit = 7 * 24 * time.Hour
	case strings.HasPrefix(u, "mo") || u == "ماه":
		unit = 30 * 24 * time.Hour
	}
	d := time.Duration(n) * unit
	if d < 30*time.Second || d > 366*24*time.Hour {
		return 0, nil
	}
	return d, nil
}

func persianDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

// modError turns Telegram's refusals into plain words.
func modError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "not enough rights"):
		return "I don't have the admin right for that here"
	case strings.Contains(msg, "user is an administrator") || strings.Contains(msg, "can't remove chat owner") ||
		strings.Contains(msg, "CHAT_ADMIN_REQUIRED"):
		return "they are an admin (or the owner) of the group"
	case strings.Contains(msg, "USER_NOT_PARTICIPANT") || strings.Contains(msg, "user not found") || strings.Contains(msg, "PARTICIPANT_ID_INVALID"):
		return "not a member of this group"
	case strings.Contains(msg, "message to delete not found") || strings.Contains(msg, "message can't be deleted"):
		return "the message is gone or too old to delete (more than 48 hours)"
	}
	return msg
}

// ---------------------------------------------------------------- warnings

func (b *Bot) warnings(ctx context.Context, chatID, userID int64) (int, []string) {
	v, err := b.st.ChatPref(ctx, chatID, prefWarnPrefix+strconv.FormatInt(userID, 10))
	if err != nil || v == "" {
		return 0, nil
	}
	head, rest, _ := strings.Cut(v, "|")
	n, _ := strconv.Atoi(head)
	var reasons []string
	if rest != "" {
		reasons = strings.Split(rest, "\x1f")
	}
	return n, reasons
}

func (b *Bot) setWarnings(ctx context.Context, chatID, userID int64, n int, reasons []string) error {
	name := prefWarnPrefix + strconv.FormatInt(userID, 10)
	if n <= 0 {
		return b.st.SetChatPref(ctx, chatID, name, "")
	}
	for len(reasons) > 0 && len(strings.Join(reasons, "\x1f")) > 200 {
		reasons = reasons[1:]
	}
	return b.st.SetChatPref(ctx, chatID, name, strconv.Itoa(n)+"|"+strings.Join(reasons, "\x1f"))
}

// ---------------------------------------------------------------- locking

var permOrder = []func(*telegram.ChatPermissions) *bool{
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendMessages },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendAudios },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendDocuments },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendPhotos },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendVideos },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendVideoNotes },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendVoiceNotes },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendPolls },
	func(p *telegram.ChatPermissions) *bool { return &p.CanSendOtherMessages },
	func(p *telegram.ChatPermissions) *bool { return &p.CanAddWebPagePreviews },
	func(p *telegram.ChatPermissions) *bool { return &p.CanChangeInfo },
	func(p *telegram.ChatPermissions) *bool { return &p.CanInviteUsers },
	func(p *telegram.ChatPermissions) *bool { return &p.CanPinMessages },
	func(p *telegram.ChatPermissions) *bool { return &p.CanManageTopics },
}

func encodePerms(p telegram.ChatPermissions) string {
	var sb strings.Builder
	for _, f := range permOrder {
		if *f(&p) {
			sb.WriteByte('1')
		} else {
			sb.WriteByte('0')
		}
	}
	return sb.String()
}

func decodePerms(s string) (telegram.ChatPermissions, bool) {
	var p telegram.ChatPermissions
	if len(s) != len(permOrder) {
		return p, false
	}
	for i, f := range permOrder {
		*f(&p) = s[i] == '1'
	}
	return p, true
}

// groupPermissions are the group's usual member rights: the ones saved when
// it was locked, else its current ones, else the usual set.
func (b *Bot) groupPermissions(ctx context.Context, chatID int64) telegram.ChatPermissions {
	if v, err := b.st.ChatPref(ctx, chatID, prefLockPerms); err == nil {
		if p, ok := decodePerms(v); ok {
			return p
		}
	}
	if info, err := b.api.GetChat(ctx, chatID); err == nil && info.Permissions != nil && info.Permissions.CanSendMessages {
		return *info.Permissions
	}
	return telegram.MemberPermissions()
}

// lockGroup stops everyone but admins from writing, remembering what members
// could do; unlocking gives that back.
func (b *Bot) lockGroup(ctx context.Context, chatID int64, lock bool) string {
	if lock {
		if info, err := b.api.GetChat(ctx, chatID); err == nil && info.Permissions != nil && info.Permissions.CanSendMessages {
			_ = b.st.SetChatPref(ctx, chatID, prefLockPerms, encodePerms(*info.Permissions))
		}
		if err := b.api.SetChatPermissions(ctx, chatID, telegram.ChatPermissions{}); err != nil {
			return "failed: " + modError(err)
		}
		return "🔒 the group is locked: only admins can write until it is unlocked"
	}
	perms := b.groupPermissions(ctx, chatID)
	if err := b.api.SetChatPermissions(ctx, chatID, perms); err != nil {
		return "failed: " + modError(err)
	}
	_ = b.st.SetChatPref(ctx, chatID, prefLockPerms, "")
	return "🔓 the group is open again"
}

// ---------------------------------------------------------------- purge

// purge deletes many messages: those of the users given (from the group's
// log), or everything from the replied message on, or the last count.
func (b *Bot) purge(ctx context.Context, m *telegram.Message, args map[string]any, count int) string {
	chatID := m.Chat.ID
	var ids []int64
	if _, named := args["users"]; named {
		targets, problems := b.modTargets(ctx, m, args)
		for _, t := range targets {
			if b.isAdmin(t.id) || t.id == b.me.ID {
				continue
			}
			got, err := b.st.GroupMessageIDsBy(ctx, chatID, t.id, maxPurge)
			if err == nil {
				ids = append(ids, got...)
			}
		}
		if len(ids) == 0 {
			return strings.Join(append(problems, "nothing to delete: their messages are only known while the group's catch-up log is on "+
				"(/settings); otherwise reply to the first message to remove and purge from there"), "\n")
		}
	} else {
		from := int64(0)
		switch {
		case m.RepliedInChat() != nil:
			from = m.RepliedInChat().MessageID
		case count > 0:
			from = m.MessageID - int64(min(count, maxPurge))
		default:
			return "error: reply to the first message to remove (everything after it goes), or give count"
		}
		for id := max(from, m.MessageID-maxPurge); id < m.MessageID; id++ {
			ids = append(ids, id)
		}
	}
	if len(ids) > maxPurge {
		ids = ids[:maxPurge]
	}
	deleted := 0
	for i := 0; i < len(ids); i += 100 {
		chunk := ids[i:min(i+100, len(ids))]
		if err := b.api.DeleteMessages(ctx, chatID, chunk); err != nil {
			if deleted == 0 {
				return "failed: " + modError(err)
			}
			break
		}
		deleted += len(chunk)
	}
	return fmt.Sprintf("🧹 deleted up to %d messages (Telegram keeps those older than 48 hours)", deleted)
}

// ---------------------------------------------------------------- admins

// changeAdmin promotes, demotes, titles or changes the rights of a member.
// Rights are capped by the asker's own and the bot's own (capRights); an
// admin with rights the asker lacks is changed only by the group's owner or
// a bot admin; Telegram lets the bot change only admins it promoted.
func (b *Bot) changeAdmin(ctx context.Context, chatID int64, asker, me *telegram.ChatMember, action string, t modTarget,
	member *telegram.ChatMember, items []string, text string) string {
	if member.Status == "creator" {
		return "⚠️ " + t.label() + ": the group's owner; their rights can't be changed"
	}
	if member.Status == "left" || member.Status == "kicked" {
		return "⚠️ " + t.label() + ": not a member of this group"
	}
	isAdmin := member.Status == "administrator"
	current := member.Rights()
	if isAdmin && !member.CanBeEdited {
		return "⚠️ " + t.label() + ": promoted by someone else, and Telegram lets me change only the admins I promoted (the group's owner can, in the group's settings)"
	}
	if isAdmin && asker != nil && asker.Status != "creator" {
		if more := beyond(current, asker); len(more) > 0 {
			return "⚠️ " + t.label() + ": they have rights you don't (" + strings.Join(more, ", ") + "); only the group's owner or a bot admin can change them"
		}
	}
	if action == "demote" {
		if !isAdmin {
			return t.label() + ": not an admin"
		}
		if err := b.api.PromoteChatMember(ctx, chatID, t.id, telegram.AdminRights{}); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		return "✅ " + t.label() + " is no longer an admin"
	}

	title := strings.TrimSpace(text)
	if action == "promote" && strings.EqualFold(title, "full") && len(items) == 0 { // the older way to ask for every right
		items, title = []string{"full"}, ""
	}
	var want telegram.AdminRights
	var dropped []string
	switch {
	case action == "set_rights" && len(items) == 0:
		return "error: rights says what to change, e.g. ['+pin', '-delete']"
	case action == "title" && isAdmin, action == "promote" && isAdmin && len(items) == 0:
		want = current // already an admin: the rights stay as they are
	default:
		if len(items) == 0 {
			items = []string{"moderator"}
		}
		var err error
		if want, err = parseRights(items, current); err != nil {
			return "error: " + err.Error()
		}
		want, dropped = capRights(want, asker, me)
	}
	want.CanManageChat = true
	var done []string
	if !isAdmin || want != current {
		if err := b.api.PromoteChatMember(ctx, chatID, t.id, want); err != nil {
			return "❌ " + t.label() + ": " + modError(err)
		}
		if isAdmin {
			done = append(done, "rights now: "+rightsText(want))
		} else {
			done = append(done, "is now an admin with: "+rightsText(want))
		}
	} else if action != "title" && title == "" {
		done = append(done, "already has exactly these rights: "+rightsText(want))
	}
	if title != "" {
		title = truncateRunes(title, 16)
		if err := b.api.SetChatAdministratorCustomTitle(ctx, chatID, t.id, title); err != nil {
			done = append(done, "the title was not set: "+modError(err))
		} else {
			done = append(done, "titled «"+title+"»")
		}
	} else if action == "title" {
		return "error: text is the admin title (at most 16 characters, no emoji)"
	}
	out := "✅ " + t.label() + " " + strings.Join(done, "; ")
	if len(dropped) > 0 {
		out += " — left out: " + strings.Join(dropped, ", ")
	}
	return out
}

// memberInfo says what a member is in the group: status, title, admin
// rights or what they may still send, and warnings.
func (b *Bot) memberInfo(ctx context.Context, chatID int64, t modTarget, member *telegram.ChatMember) string {
	n, _ := b.warnings(ctx, chatID, t.id)
	parts := []string{t.label() + ": " + member.Status}
	if member.User.Username != "" {
		parts = append(parts, "@"+member.User.Username)
	}
	if member.CustomTitle != "" {
		parts = append(parts, "title «"+member.CustomTitle+"»")
	}
	switch member.Status {
	case "creator":
		parts = append(parts, "the group's owner (every right)")
	case "administrator":
		parts = append(parts, "rights: "+rightsText(member.Rights()))
		if !member.CanBeEdited {
			parts = append(parts, "promoted by someone else (I can't change their rights)")
		}
	case "restricted":
		allowed, blocked := permsText(member.Permissions())
		parts = append(parts, "blocked: "+listOr(blocked, "nothing"), "allowed: "+listOr(allowed, "nothing"))
		if !member.IsMember {
			parts = append(parts, "not in the group now")
		}
	case "member":
		parts = append(parts, "the group's usual permissions")
	case "kicked":
		parts = append(parts, "banned")
	}
	if member.UntilDate > 0 {
		parts = append(parts, "until "+time.Unix(member.UntilDate, 0).In(b.loc).Format("2006-01-02 15:04"))
	}
	if b.isAdmin(t.id) {
		parts = append(parts, "a bot admin")
	}
	return strings.Join(parts, ", ") + fmt.Sprintf(", warnings %d/%d", n, warnLimit)
}

// strList reads a list argument (a lone string counts as one item).
func strList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, it := range x {
			if s, ok := it.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case string:
		for _, s := range strings.FieldsFunc(x, func(r rune) bool { return r == ',' || r == '،' || r == ' ' }) {
			out = append(out, s)
		}
	}
	return out
}

func listOr(xs []string, none string) string {
	if len(xs) == 0 {
		return none
	}
	return strings.Join(xs, ", ")
}
