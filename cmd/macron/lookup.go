package main

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const toolLookup = "telegram_lookup"

var (
	lookupLinkRe = regexp.MustCompile(`(?i)^(?:https?://)?(?:www\.)?(?:t\.me|telegram\.me|telegram\.dog)/(?:s/)?(c/\d+|[a-z0-9_]{4,32})`)
	lookupNameRe = regexp.MustCompile(`^@?[A-Za-z][A-Za-z0-9_]{3,31}$`)
)

type lookupTarget struct {
	id       int64
	username string
	user     *telegram.User
	via      string
	chat     *telegram.ChatFullInfo
}

func (b *Bot) lookupTool(u *store.User, m *telegram.Message) agent.LocalTool {
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolLookup,
			Description: "Stats of a Telegram account, bot, group or channel: name, bio, premium, photos, data center, account age, " +
				"members, standing in this group.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"target": map[string]any{"type": "string", "description": "Id, @username, t.me link, 'reply', 'forward' or 'me' (default: replied/forwarded author, else me)"},
			}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			target, _ := args["target"].(string)
			if v, ok := args["target"].(float64); ok {
				target = strconv.FormatInt(int64(v), 10)
			}
			return agent.ToolOutput{Text: b.lookup(ctx, u, m, target)}, nil
		},
	}
}

func resolveTarget(m *telegram.Message, target string, self int64) (lookupTarget, error) {
	t := strings.TrimSpace(target)
	low := strings.ToLower(t)
	fromUser := func(u *telegram.User, via string) lookupTarget {
		return lookupTarget{id: u.ID, username: u.Username, user: u, via: via}
	}
	fromOrigin := func(o *telegram.MessageOrigin) (lookupTarget, bool) {
		switch {
		case o == nil:
		case o.SenderUser != nil:
			return fromUser(o.SenderUser, "the forwarded message's author"), true
		case o.Chat != nil:
			return lookupTarget{id: o.Chat.ID, username: o.Chat.Username, via: "the chat the message was forwarded from"}, true
		case o.SenderChat != nil:
			return lookupTarget{id: o.SenderChat.ID, username: o.SenderChat.Username, via: "the chat the message was forwarded from"}, true
		}
		return lookupTarget{}, false
	}
	replied := func() (lookupTarget, bool) {
		r := m.Replied()
		switch {
		case r == nil:
		case r.From != nil && r.From.ID != 0:
			return fromUser(r.From, "the replied message's author"), true
		case r.SenderChat != nil:
			return lookupTarget{id: r.SenderChat.ID, username: r.SenderChat.Username, via: "the replied message's sender chat"}, true
		}
		return lookupTarget{}, false
	}
	switch low {
	case "", "reply", "replied":
		if lt, ok := replied(); ok && (low != "" || lt.id != self) {
			return lt, nil
		}
		if low != "" {
			return lookupTarget{}, errors.New("the message replies to nothing; give an id, @username or link, or ask them to reply to the person's message")
		}
		if lt, ok := fromOrigin(m.Origin()); ok {
			return lt, nil
		}
		if r := m.Replied(); r != nil {
			if lt, ok := fromOrigin(r.Origin()); ok {
				return lt, nil
			}
		}
		return fromUser(m.From, "the user themself"), nil
	case "forward", "forwarded":
		if lt, ok := fromOrigin(m.Origin()); ok {
			return lt, nil
		}
		if r := m.Replied(); r != nil {
			if lt, ok := fromOrigin(r.Origin()); ok {
				return lt, nil
			}
			if r.Origin() != nil {
				return lookupTarget{}, errors.New("that message was forwarded from a hidden account: Telegram gives no id for it")
			}
		}
		return lookupTarget{}, errors.New("no forwarded message here; forward it to me, or reply to it")
	case "me", "myself", "self":
		return fromUser(m.From, "the user themself"), nil
	}
	if sub := lookupLinkRe.FindStringSubmatch(t); sub != nil {
		if strings.HasPrefix(sub[1], "c/") {
			n, _ := strconv.ParseInt(strings.TrimPrefix(sub[1], "c/"), 10, 64)
			id, _ := strconv.ParseInt("-100"+strconv.FormatInt(n, 10), 10, 64)
			return lookupTarget{id: id, via: "the link"}, nil
		}
		return lookupTarget{username: sub[1], via: "the link"}, nil
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(t, "id:")), 10, 64); err == nil && n != 0 {
		return lookupTarget{id: n}, nil
	}
	if lookupNameRe.MatchString(t) {
		return lookupTarget{username: strings.TrimPrefix(t, "@")}, nil
	}
	return lookupTarget{}, fmt.Errorf("%q is not an id, @username or t.me link", textutil.TruncateRunes(t, 60, "…"))
}

func (b *Bot) lookup(ctx context.Context, asker *store.User, m *telegram.Message, target string) string {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	lt, err := resolveTarget(m, target, b.me.ID)
	if err != nil {
		return "error: " + err.Error()
	}
	var sb strings.Builder
	line := func(format string, a ...any) { fmt.Fprintf(&sb, format+"\n", a...) }

	if lt.id == 0 && lt.username != "" {
		if info, err := b.api.GetChat(ctx, "@"+lt.username); err == nil {
			lt.id, lt.chat = info.ID, info
		} else if found, _ := b.st.FindUsers(ctx, "@"+lt.username, 1); len(found) > 0 {
			lt.id = found[0].ID
		} else if !m.Chat.Private() && m.GuestQueryID == "" {
			if id, _, err := b.st.GroupMemberByUsername(ctx, m.Chat.ID, lt.username); err == nil && id != 0 {
				lt.id = id
			}
		}
		if lt.id == 0 {
			return fmt.Sprintf("not found: @%s is not a public group, channel or bot Telegram knows, and no one by that name has talked to "+
				"me or written in this group. Telegram lets a bot look a person up only then: ask for their numeric id (they see it with "+
				"/id), or to reply to one of their messages.", lt.username)
		}
	}
	if lt.via != "" {
		line("target: %s", lt.via)
	}
	if lt.id < 0 {
		b.lookupChat(ctx, &sb, lt.id, lt.chat)
	} else {
		b.lookupUser(ctx, &sb, asker, m, lt)
	}
	return strings.TrimSpace(sb.String())
}

func (b *Bot) lookupChat(ctx context.Context, sb *strings.Builder, id int64, known *telegram.ChatFullInfo) {
	line := func(format string, a ...any) { fmt.Fprintf(sb, format+"\n", a...) }
	var (
		info    = known
		infoErr error
		members = -1
		wg      sync.WaitGroup
	)
	wg.Add(1)
	if info == nil {
		wg.Add(1)
		go func() { defer wg.Done(); info, infoErr = b.api.GetChat(ctx, id) }()
	}
	go func() {
		defer wg.Done()
		if n, err := b.api.GetChatMemberCount(ctx, id); err == nil {
			members = n
		}
	}()
	wg.Wait()
	line("id: %d", id)
	if infoErr != nil || info == nil {
		line("Telegram does not show this chat to the bot (it is private and the bot is not in it, or it does not exist)")
		return
	}
	line("type: %s", info.Type)
	if info.Title != "" {
		line("title: %s", info.Title)
	}
	if info.Username != "" {
		line("username: @%s (public: t.me/%s)", info.Username, info.Username)
	} else {
		line("username: none (private)")
	}
	if members >= 0 {
		line("members: %d", members)
	}
	if d := strings.TrimSpace(info.Description); d != "" {
		line("description: %s", textutil.TruncateRunes(textutil.OneLine(d), 400, "…"))
	}
	if info.Photo != nil {
		if dc := telegram.FileDC(info.Photo.BigFileID); dc > 0 {
			line("data center: DC%d (%s)", dc, telegram.DCLocation(dc))
		}
	}
	if c, err := b.st.Chat(ctx, id); err == nil && c.Active() {
		line("this bot is in it: yes (since %s)", c.JoinedAt.In(b.loc).Format("2006-01-02"))
	}
}

func (b *Bot) lookupUser(ctx context.Context, sb *strings.Builder, asker *store.User, m *telegram.Message, lt lookupTarget) {
	line := func(format string, a ...any) { fmt.Fprintf(sb, format+"\n", a...) }
	inGroup := !m.Chat.Private() && m.GuestQueryID == ""
	var (
		info    *telegram.ChatFullInfo
		photos  *telegram.UserProfilePhotos
		member  *telegram.ChatMember
		infoErr error
		wg      sync.WaitGroup
	)
	wg.Add(2)
	go func() { defer wg.Done(); info, infoErr = b.api.GetChat(ctx, lt.id) }()
	go func() { defer wg.Done(); photos, _ = b.api.GetUserProfilePhotos(ctx, lt.id, 1) }()
	if inGroup {
		wg.Add(1)
		go func() { defer wg.Done(); member, _ = b.api.GetChatMember(ctx, m.Chat.ID, lt.id) }()
	}
	known, _ := b.st.User(ctx, lt.id)
	wg.Wait()

	user := lt.user
	if user == nil && member != nil {
		user = &member.User
	}
	kind := "user"
	if (user != nil && user.IsBot) || (info != nil && strings.HasSuffix(strings.ToLower(info.Username), "bot") && user == nil) {
		kind = "bot"
	}
	line("id: %d", lt.id)
	line("type: %s", kind)
	name, username := "", ""
	switch {
	case info != nil:
		name, username = strings.TrimSpace(info.FirstName+" "+info.LastName), info.Username
	case user != nil:
		name, username = user.FullName(), user.Username
	case known != nil:
		name, username = known.DisplayName(), known.Username
	}
	if name != "" {
		line("name: %s", name)
	}
	if username != "" {
		line("username: @%s", username)
	} else {
		line("username: none")
	}
	if info != nil && strings.TrimSpace(info.Bio) != "" {
		line("bio: %s", textutil.TruncateRunes(textutil.OneLine(info.Bio), 300, "…"))
	}
	if info != nil && info.PersonalChat != nil {
		pc := info.PersonalChat
		line("personal channel: %s%s", pc.Title, map[bool]string{true: " (@" + pc.Username + ")"}[pc.Username != ""])
	}
	if user != nil {
		line("telegram premium: %s", yesOrNo(user.IsPremium))
		if user.LanguageCode != "" {
			line("app language: %s", user.LanguageCode)
		}
	} else if known != nil {
		line("telegram premium: %s (when last seen)", yesOrNo(known.IsPremium))
	}
	if photos != nil {
		line("profile photos: %d", photos.TotalCount)
		if len(photos.Photos) > 0 && len(photos.Photos[0]) > 0 {
			sizes := photos.Photos[0]
			if dc := telegram.FileDC(sizes[len(sizes)-1].FileID); dc > 0 {
				line("data center: DC%d (%s), where the account lives", dc, telegram.DCLocation(dc))
			}
		}
	}
	if age, ok := telegram.EstimateAccountAge(lt.id); ok && kind == "user" {
		when := age.When.Format("2006-01")
		switch {
		case age.Before:
			line("account made: before %s (one of the oldest ids)", when)
		case age.After:
			line("account made: after %s (newer than the newest id with a known date; no closer estimate)", when)
		default:
			line("account made: about %s (estimated from the id: it can be off by months)", when)
		}
	}
	if info == nil && infoErr != nil && user == nil && known == nil {
		line("note: Telegram shows a person to a bot only after they talked to it or share a group with it; " +
			"this account has done neither, so only what its id tells is known")
	}

	if inGroup {
		if member != nil {
			line("in this group: %s", memberStatus(member.Status))
		}
		if s := b.groupLogFor(ctx, m.Chat.ID); s.hours > 0 && !s.optedOut[lt.id] {
			if ids, err := b.st.GroupMessageIDsBy(ctx, m.Chat.ID, lt.id, 1000); err == nil {
				line("messages here in the last %d hours: %d", s.hours, len(ids))
			}
			if last, err := b.st.LastGroupMessageBy(ctx, m.Chat.ID, lt.id, 0); err == nil && last != nil {
				line("last message here: %s ago", agoText(time.Since(last.CreatedAt)))
			}
		}
	}

	if known != nil && (b.isAdmin(asker.ID) || asker.ID == lt.id) {
		line("— this bot's records (shown because the asker is %s) —", map[bool]string{true: "the person themself", false: "an admin of the bot"}[asker.ID == lt.id])
		line("first talked to the bot: %s", known.CreatedAt.In(b.loc).Format("2006-01-02"))
		line("last seen: %s ago", agoText(time.Since(known.LastSeenAt)))
		line("messages to the bot: %d", known.MessagesTotal)
		if used, err := b.st.TokensToday(ctx, known.ID); err == nil {
			line("tokens used today: %d", used)
		}
		if known.BlockedBot {
			line("blocked the bot: yes")
		}
	} else if known != nil {
		line("note: what the bot itself knows of this person (their use of it) is private: only they and the bot's admins see it")
	}
}

func memberStatus(s string) string {
	switch s {
	case "creator":
		return "owner"
	case "administrator":
		return "admin"
	case "member":
		return "member"
	case "restricted":
		return "restricted"
	case "left":
		return "not a member (left or never joined)"
	case "kicked":
		return "banned from the group"
	}
	return s
}

func yesOrNo(v bool) string {
	if v {
		return "yes"
	}
	return "no"
}

func agoText(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	case d >= 2*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	case d >= 2*time.Minute:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	}
	return "moments"
}
