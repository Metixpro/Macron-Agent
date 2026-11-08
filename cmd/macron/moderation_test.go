package main

import (
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const modChat = -100500

func modMessage(from int64, text string, replyTo *telegram.User) *telegram.Message {
	m := &telegram.Message{MessageID: 900, From: &telegram.User{ID: from, FirstName: "Admin"}, Chat: telegram.Chat{ID: modChat, Type: "supergroup"}, Text: text}
	if replyTo != nil {
		m.ReplyToMessage = &telegram.Message{MessageID: 880, From: replyTo, Chat: m.Chat, Text: "buy followers http://spam"}
	}
	return m
}

func TestGroupModeration(t *testing.T) {
	h := newHarness(t)
	b := h.bot
	spammer := &telegram.User{ID: 555, FirstName: "Spam", LastName: "Bot"}
	h.tg.members["-100500:42"] = "administrator" // the bot
	h.tg.members["-100500:77"] = "administrator" // the asker
	h.tg.members["-100500:78"] = "member"
	h.tg.members["-100500:555"] = "member"
	h.tg.members["-100500:556"] = "administrator"

	if b.moderator(t.Context(), modMessage(78, "", nil)) || !b.moderator(t.Context(), modMessage(77, "", nil)) || !b.moderator(t.Context(), modMessage(ownerID, "", nil)) {
		t.Fatal("offered to the group's admins and the bot's admins only")
	}
	if b.moderator(t.Context(), &telegram.Message{From: &telegram.User{ID: 77}, Chat: telegram.Chat{ID: 77, Type: "private"}}) {
		t.Fatal("not in private chats")
	}
	if out := b.moderate(t.Context(), modMessage(78, "", spammer), map[string]any{"action": "ban"}); !strings.HasPrefix(out, "refused") {
		t.Errorf("a member cannot: %s", out)
	}

	h.tg.reset()
	out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "ban", "duration": "۲ ساعت", "reason": "spam", "delete_messages": true})
	if !strings.Contains(out, "✅ banned Spam Bot (555) for 2 hour(s) — spam") {
		t.Fatalf("ban: %s", out)
	}
	call := h.tg.of("banChatMember")[0].body
	until := int64(call["until_date"].(float64))
	if call["user_id"].(float64) != 555 || call["revoke_messages"] != true || until < time.Now().Add(119*time.Minute).Unix() || until > time.Now().Add(121*time.Minute).Unix() {
		t.Errorf("the ban call: %v", call)
	}

	out = b.moderate(t.Context(), modMessage(77, "", nil), map[string]any{"action": "mute", "users": []any{"556", "77", "42", "555", "@nobody_here"}})
	for _, want := range []string{"(556): an admin of this group", "77: not on yourself", "42: that is me", "✅ muted", "I don't know @nobody_here"} {
		if !strings.Contains(out, want) {
			t.Errorf("mute misses %q:\n%s", want, out)
		}
	}
	if out := b.moderate(t.Context(), modMessage(77, "", nil), map[string]any{"action": "ban", "users": []any{"1000"}}); !strings.Contains(out, "a bot admin, protected") {
		t.Errorf("bot admins are protected: %s", out)
	}

	// Three warnings mute for a day and start over.
	h.tg.reset()
	for i := 1; i <= 3; i++ {
		out = b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "warn", "reason": "link"})
	}
	if !strings.Contains(out, "reached 3 warnings and is muted for a day") || len(h.tg.of("restrictChatMember")) != 1 {
		t.Errorf("third warning: %s", out)
	}
	if out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "warnings"}); !strings.Contains(out, "no warnings") {
		t.Errorf("the count starts over: %s", out)
	}
	b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "warn", "reason": "flood"})
	if out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "info"}); !strings.Contains(out, "member") || !strings.Contains(out, "warnings 1/3") {
		t.Errorf("info: %s", out)
	}

	// Purge from the replied message on; lock and unlock; an invite link.
	h.tg.reset()
	if out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "purge"}); !strings.Contains(out, "deleted up to 20 messages") {
		t.Errorf("purge: %s", out)
	}
	ids := h.tg.of("deleteMessages")[0].body["message_ids"].([]any)
	if len(ids) != 20 || ids[0].(float64) != 880 || ids[19].(float64) != 899 {
		t.Errorf("from the replied message to now: %v", ids)
	}
	if out := b.moderate(t.Context(), modMessage(77, "", nil), map[string]any{"action": "lock"}); !strings.Contains(out, "locked") {
		t.Errorf("lock: %s", out)
	}
	if perms := h.tg.of("setChatPermissions")[0].body["permissions"].(map[string]any); perms["can_send_messages"] != false {
		t.Errorf("locked: %v", perms)
	}
	b.moderate(t.Context(), modMessage(77, "", nil), map[string]any{"action": "unlock"})
	if perms := h.tg.of("setChatPermissions")[1].body["permissions"].(map[string]any); perms["can_send_messages"] != true || perms["can_change_info"] != false {
		t.Errorf("unlocked with the usual member rights: %v", perms)
	}
	if out := b.moderate(t.Context(), modMessage(77, "", nil), map[string]any{"action": "invite_link", "duration": "1d", "count": 10.0}); !strings.Contains(out, "https://t.me/+abc123") || !strings.Contains(out, "for 10 people") {
		t.Errorf("invite link: %s", out)
	}
	// Every right, as far as the asker has them (the rest is left out).
	if out := b.moderate(t.Context(), modMessage(77, "", &telegram.User{ID: 78, FirstName: "Sara"}), map[string]any{"action": "promote", "text": "full"}); !strings.Contains(out, "is now an admin with: change_info") || !strings.Contains(out, "manage_topics (you don't have it yourself)") {
		t.Errorf("full, capped by the asker's rights: %s", out)
	}

	// Without the right, the bot says what it needs.
	h.tg.members["-100500:42"] = "administrator-limited"
	if out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "mute"}); !strings.Contains(out, "with the right to ban users") {
		t.Errorf("the bot's own rights: %s", out)
	}
	h.tg.members["-100500:77"] = "administrator-limited"
	h.tg.members["-100500:42"] = "administrator"
	if out := b.moderate(t.Context(), modMessage(77, "", spammer), map[string]any{"action": "ban"}); !strings.Contains(out, "needs the admin right to ban users") {
		t.Errorf("the asker's own rights: %s", out)
	}
}

func TestModDuration(t *testing.T) {
	for in, want := range map[string]time.Duration{"30m": 30 * time.Minute, "2h": 2 * time.Hour, "1d": 24 * time.Hour, "1w": 7 * 24 * time.Hour,
		"۳ روز": 72 * time.Hour, "forever": 0, "": 0, "10s": 0, "400d": 0} {
		if got, err := parseModDuration(in); err != nil || got != want {
			t.Errorf("%q: %v %v", in, got, err)
		}
	}
	if _, err := parseModDuration("soon"); err == nil {
		t.Error("nonsense is an error")
	}
}
