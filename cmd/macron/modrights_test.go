package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func adminJSON(id int64, editable bool, rights ...string) string {
	extra := ""
	for _, r := range rights {
		extra += fmt.Sprintf(`,"%s":true`, r)
	}
	return fmt.Sprintf(`{"status":"administrator","user":{"id":%d,"first_name":"x"},"can_be_edited":%v,"can_manage_chat":true%s}`, id, editable, extra)
}

func lastBody(h *harness, method string) map[string]any {
	calls := h.tg.of(method)
	if len(calls) == 0 {
		h.t.Fatalf("no %s call", method)
	}
	return calls[len(calls)-1].body
}

func TestGroupAdminRights(t *testing.T) {
	h := newHarness(t)
	b := h.bot
	all := []string{"can_change_info", "can_delete_messages", "can_restrict_members", "can_invite_users", "can_pin_messages",
		"can_manage_topics", "can_manage_video_chats", "can_promote_members", "can_post_stories", "can_edit_stories", "can_delete_stories"}
	h.tg.members["-100500:42"] = adminJSON(42, false, all...)
	h.tg.members["-100500:77"] = adminJSON(77, false, "can_delete_messages", "can_restrict_members", "can_pin_messages", "can_invite_users",
		"can_promote_members", "can_manage_video_chats")
	h.tg.members["-100500:1"] = `{"status":"creator","user":{"id":1,"first_name":"Owner"}}`
	h.tg.members["-100500:78"] = "member"
	h.tg.members["-100500:83"] = "member"
	h.tg.members["-100500:79"] = adminJSON(79, true, "can_delete_messages", "can_pin_messages")
	h.tg.members["-100500:80"] = adminJSON(80, true, "can_change_info", "can_delete_messages")
	h.tg.members["-100500:81"] = adminJSON(81, false, "can_pin_messages")
	h.tg.members["-100500:82"] = `{"status":"restricted","user":{"id":82,"first_name":"R"},"is_member":true,"can_send_messages":true,` +
		`"can_send_audios":true,"can_send_documents":true,"can_send_photos":false,"can_send_videos":true,"can_send_video_notes":true,` +
		`"can_send_voice_notes":true,"can_send_polls":true,"can_send_other_messages":true,"can_add_web_page_previews":true,"until_date":0}`
	h.tg.members["-100500:555"] = "member"
	do := func(args map[string]any) string { return b.moderate(t.Context(), modMessage(77, "", nil), args) }

	out := do(map[string]any{"action": "promote", "users": []any{"78"}, "rights": []any{"moderator", "+info", "-delete"}, "text": "ضعیف ۲"})
	body := lastBody(h, "promoteChatMember")
	if body["can_restrict_members"] != true || body["can_pin_messages"] != true || body["can_delete_messages"] != nil || body["can_change_info"] != nil {
		t.Fatalf("promote: %v\n%s", body, out)
	}
	if !strings.Contains(out, "change_info (you don't have it yourself)") || !strings.Contains(out, "titled «ضعیف ۲»") {
		t.Fatalf("promote report: %s", out)
	}
	if lastBody(h, "setChatAdministratorCustomTitle")["custom_title"] != "ضعیف ۲" {
		t.Fatal("no title")
	}

	out = do(map[string]any{"action": "set_rights", "users": []any{"79"}, "rights": []any{"+ban", "-pin"}})
	body = lastBody(h, "promoteChatMember")
	if body["user_id"] != float64(79) || body["can_delete_messages"] != true || body["can_restrict_members"] != true || body["can_pin_messages"] != nil {
		t.Fatalf("set_rights: %v\n%s", body, out)
	}

	for user, want := range map[string]string{"80": "they have rights you don't (change_info)", "81": "promoted by someone else",
		"1": "the group's owner"} {
		h.tg.reset()
		if out := do(map[string]any{"action": "demote", "users": []any{user}}); !strings.Contains(out, want) || len(h.tg.of("promoteChatMember")) > 0 {
			t.Errorf("demote %s: %s", user, out)
		}
	}

	h.tg.reset()
	out = b.moderate(t.Context(), modMessage(ownerID, "", nil), map[string]any{"action": "promote", "users": []any{"78"}, "rights": []any{"full"}})
	if body = lastBody(h, "promoteChatMember"); body["can_change_info"] != true || body["can_promote_members"] != true || body["is_anonymous"] != nil ||
		strings.Contains(out, "left out") {
		t.Fatalf("full by a bot admin: %v\n%s", body, out)
	}

	h.tg.reset()
	out = do(map[string]any{"action": "title", "users": []any{"83"}, "text": "پشتیبان گروه"})
	if lastBody(h, "promoteChatMember")["can_restrict_members"] != true || !strings.Contains(out, "titled «پشتیبان گروه»") {
		t.Fatalf("title: %s", out)
	}
	if out := do(map[string]any{"action": "promote", "users": []any{"78"}, "rights": []any{"+fly"}}); !strings.Contains(out, "unknown right") {
		t.Errorf("an unknown right: %s", out)
	}

	h.tg.reset()
	out = do(map[string]any{"action": "restrict", "users": []any{"555"}, "permissions": []any{"-media", "-links"}, "duration": "2h"})
	body = lastBody(h, "restrictChatMember")
	perms := body["permissions"].(map[string]any)
	if perms["can_send_photos"] != false || perms["can_send_voice_notes"] != false || perms["can_add_web_page_previews"] != false ||
		perms["can_send_messages"] != true || perms["can_send_other_messages"] != true {
		t.Fatalf("restrict: %v", perms)
	}
	if until := int64(body["until_date"].(float64)); until < time.Now().Add(119*time.Minute).Unix() || !strings.Contains(out, "link previews") {
		t.Fatalf("restrict: %v\n%s", body, out)
	}
	do(map[string]any{"action": "restrict", "users": []any{"82"}, "permissions": []any{"+photos", "-stickers"}})
	if perms := lastBody(h, "restrictChatMember")["permissions"].(map[string]any); perms["can_send_photos"] != true || perms["can_send_other_messages"] != false {
		t.Fatalf("restrict from what they had: %v", perms)
	}
	if out := do(map[string]any{"action": "restrict", "users": []any{"79"}, "permissions": []any{"-media"}}); !strings.Contains(out, "an admin of this group") {
		t.Errorf("admins are not restricted: %s", out)
	}

	for user, want := range map[string]string{"79": "rights: delete_messages, pin_messages", "82": "blocked: photos", "81": "promoted by someone else",
		"1": "the group's owner"} {
		if out := do(map[string]any{"action": "info", "users": []any{user}}); !strings.Contains(out, want) {
			t.Errorf("info %s: %s", user, out)
		}
	}

	for i := 0; i < 2; i++ {
		do(map[string]any{"action": "warn", "users": []any{"555"}, "reason": fmt.Sprint("r", i)})
	}
	if out := do(map[string]any{"action": "unwarn", "users": []any{"555"}}); !strings.Contains(out, "1 of 3 left") {
		t.Errorf("unwarn: %s", out)
	}
	if n, reasons := b.warnings(t.Context(), modChat, 555); n != 1 || len(reasons) != 1 || reasons[0] != "r0" {
		t.Errorf("after unwarn: %d %v", n, reasons)
	}
	do(map[string]any{"action": "warn", "users": []any{"555"}})
	if out := do(map[string]any{"action": "unwarn", "users": []any{"555"}, "text": "all"}); !strings.Contains(out, "every warning") {
		t.Errorf("unwarn all: %s", out)
	}
}

func TestParseRightsAndPermissions(t *testing.T) {
	r, err := parseRights([]string{"+pin", "-delete"}, telegram.AdminRights{CanDeleteMessages: true, CanInviteUsers: true})
	if err != nil || !r.CanPinMessages || r.CanDeleteMessages || !r.CanInviteUsers || !r.CanManageChat {
		t.Fatalf("changes keep the rest: %+v %v", r, err)
	}
	r, _ = parseRights([]string{"cleaner", "+stories"}, telegram.AdminRights{CanChangeInfo: true})
	if r.CanChangeInfo || !r.CanDeleteMessages || !r.CanPostStories || !r.CanDeleteStories {
		t.Fatalf("a preset starts over: %+v", r)
	}
	if r, _ = parseRights([]string{"Can Pin Messages", "full", "-promote"}, telegram.AdminRights{}); !r.CanPinMessages || r.CanPromoteMembers || r.IsAnonymous {
		t.Fatalf("full without promote: %+v", r)
	}
	p, err := parsePerms([]string{"media", "+voice"}, telegram.MemberPermissions())
	if err != nil || p.CanSendPhotos || p.CanSendDocuments || !p.CanSendVoiceNotes || !p.CanSendMessages {
		t.Fatalf("perms: %+v %v", p, err)
	}
	if p, _ = parsePerms([]string{"-all", "+messages"}, telegram.MemberPermissions()); !p.CanSendMessages || p.CanSendPhotos || p.CanInviteUsers {
		t.Fatalf("all: %+v", p)
	}
	if _, err := parsePerms([]string{"-teleport"}, telegram.MemberPermissions()); err == nil {
		t.Fatal("an unknown permission is an error")
	}
}

func TestBotAdminsKeepTheirRole(t *testing.T) {
	h := newHarness(t)
	h.tg.members["-100500:42"] = adminJSON(42, false, "can_delete_messages", "can_restrict_members", "can_pin_messages", "can_promote_members")
	h.tg.members["-100500:77"] = adminJSON(77, false, "can_delete_messages", "can_restrict_members", "can_pin_messages", "can_promote_members")
	h.tg.members["-100500:1000"] = adminJSON(1000, true, "can_delete_messages", "can_restrict_members", "can_pin_messages")
	for _, args := range []map[string]any{
		{"action": "promote", "users": []any{"1000"}, "rights": []any{"cleaner"}},
		{"action": "set_rights", "users": []any{"1000"}, "rights": []any{"-ban"}},
		{"action": "title", "users": []any{"1000"}, "text": "x"},
		{"action": "demote", "users": []any{"1000"}},
	} {
		if out := h.bot.moderate(t.Context(), modMessage(77, "", nil), args); !strings.Contains(out, "a bot admin, protected") {
			t.Errorf("%v: %s", args["action"], out)
		}
	}
	if n := len(h.tg.of("promoteChatMember")); n != 0 {
		t.Fatalf("%d promote calls on a bot admin", n)
	}
	if out := h.bot.moderate(t.Context(), modMessage(ownerID, "", nil), map[string]any{"action": "title", "users": []any{"1000"}, "text": "پشتیبان"}); !strings.Contains(out, "titled") {
		t.Errorf("a bot admin may title another: %s", out)
	}
}
