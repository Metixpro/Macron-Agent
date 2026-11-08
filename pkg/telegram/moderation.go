package telegram

import (
	"context"
	"time"
)

// Group moderation: banning, muting, promoting, pinning, deleting and
// locking. The bot must be an administrator with the matching right.

// ChatPermissions are what members may do in a group.
type ChatPermissions struct {
	CanSendMessages       bool `json:"can_send_messages"`
	CanSendAudios         bool `json:"can_send_audios"`
	CanSendDocuments      bool `json:"can_send_documents"`
	CanSendPhotos         bool `json:"can_send_photos"`
	CanSendVideos         bool `json:"can_send_videos"`
	CanSendVideoNotes     bool `json:"can_send_video_notes"`
	CanSendVoiceNotes     bool `json:"can_send_voice_notes"`
	CanSendPolls          bool `json:"can_send_polls"`
	CanSendOtherMessages  bool `json:"can_send_other_messages"`
	CanAddWebPagePreviews bool `json:"can_add_web_page_previews"`
	CanChangeInfo         bool `json:"can_change_info"`
	CanInviteUsers        bool `json:"can_invite_users"`
	CanPinMessages        bool `json:"can_pin_messages"`
	CanManageTopics       bool `json:"can_manage_topics"`
}

// MemberPermissions are the usual rights of members: everything but
// changing the group, pinning and topics.
func MemberPermissions() ChatPermissions {
	return ChatPermissions{CanSendMessages: true, CanSendAudios: true, CanSendDocuments: true, CanSendPhotos: true, CanSendVideos: true,
		CanSendVideoNotes: true, CanSendVoiceNotes: true, CanSendPolls: true, CanSendOtherMessages: true, CanAddWebPagePreviews: true,
		CanInviteUsers: true}
}

// AdminRights are the rights given with PromoteChatMember.
type AdminRights struct {
	CanManageChat       bool `json:"can_manage_chat"`
	CanDeleteMessages   bool `json:"can_delete_messages"`
	CanManageVideoChats bool `json:"can_manage_video_chats"`
	CanRestrictMembers  bool `json:"can_restrict_members"`
	CanPromoteMembers   bool `json:"can_promote_members"`
	CanChangeInfo       bool `json:"can_change_info"`
	CanInviteUsers      bool `json:"can_invite_users"`
	CanPinMessages      bool `json:"can_pin_messages"`
	CanManageTopics     bool `json:"can_manage_topics"`
	IsAnonymous         bool `json:"is_anonymous"`
	CanPostStories      bool `json:"can_post_stories"`
	CanEditStories      bool `json:"can_edit_stories"`
	CanDeleteStories    bool `json:"can_delete_stories"`
}

// AllAdminRights are every right of a group admin (what its owner has).
func AllAdminRights() AdminRights {
	return AdminRights{CanManageChat: true, CanDeleteMessages: true, CanManageVideoChats: true, CanRestrictMembers: true,
		CanPromoteMembers: true, CanChangeInfo: true, CanInviteUsers: true, CanPinMessages: true, CanManageTopics: true,
		CanPostStories: true, CanEditStories: true, CanDeleteStories: true, IsAnonymous: true}
}

func untilUnix(until time.Time) int64 {
	if until.IsZero() {
		return 0
	}
	return until.Unix()
}

// BanChatMember bans a user until a time (zero: for ever); revoke also
// deletes all their messages in the group.
func (b *Bot) BanChatMember(ctx context.Context, chatID, userID int64, until time.Time, revoke bool) error {
	return b.call(ctx, "banChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "until_date": untilUnix(until),
		"revoke_messages": revoke}, nil)
}

// UnbanChatMember lifts a ban (the user may join again; a member is not removed).
func (b *Bot) UnbanChatMember(ctx context.Context, chatID, userID int64) error {
	return b.call(ctx, "unbanChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "only_if_banned": true}, nil)
}

// RestrictChatMember sets what a member may do, until a time (zero: for ever).
func (b *Bot) RestrictChatMember(ctx context.Context, chatID, userID int64, perms ChatPermissions, until time.Time) error {
	return b.call(ctx, "restrictChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "permissions": perms,
		"use_independent_chat_permissions": true, "until_date": untilUnix(until)}, nil)
}

// PromoteChatMember sets an admin's rights, all of them at once (with none:
// takes the admin away). Only the rights given are sent; the rest are off.
func (b *Bot) PromoteChatMember(ctx context.Context, chatID, userID int64, rights AdminRights) error {
	payload := map[string]any{"chat_id": chatID, "user_id": userID}
	for k, v := range map[string]bool{"can_manage_chat": rights.CanManageChat, "can_delete_messages": rights.CanDeleteMessages,
		"can_manage_video_chats": rights.CanManageVideoChats, "can_restrict_members": rights.CanRestrictMembers,
		"can_promote_members": rights.CanPromoteMembers, "can_change_info": rights.CanChangeInfo, "can_invite_users": rights.CanInviteUsers,
		"can_pin_messages": rights.CanPinMessages, "can_manage_topics": rights.CanManageTopics, "is_anonymous": rights.IsAnonymous,
		"can_post_stories": rights.CanPostStories, "can_edit_stories": rights.CanEditStories, "can_delete_stories": rights.CanDeleteStories} {
		if v {
			payload[k] = true
		}
	}
	return b.call(ctx, "promoteChatMember", payload, nil)
}

// SetChatAdministratorCustomTitle sets the title shown next to an admin's name.
func (b *Bot) SetChatAdministratorCustomTitle(ctx context.Context, chatID, userID int64, title string) error {
	return b.call(ctx, "setChatAdministratorCustomTitle", map[string]any{"chat_id": chatID, "user_id": userID, "custom_title": title}, nil)
}

// SetChatPermissions sets what all members may do.
func (b *Bot) SetChatPermissions(ctx context.Context, chatID int64, perms ChatPermissions) error {
	return b.call(ctx, "setChatPermissions", map[string]any{"chat_id": chatID, "permissions": perms, "use_independent_chat_permissions": true}, nil)
}

// DeleteMessages deletes up to 100 messages at once (missing ones are skipped).
func (b *Bot) DeleteMessages(ctx context.Context, chatID int64, ids []int64) error {
	return b.call(ctx, "deleteMessages", map[string]any{"chat_id": chatID, "message_ids": ids}, nil)
}

// PinChatMessage pins a message; silent pins without a notification.
func (b *Bot) PinChatMessage(ctx context.Context, chatID, messageID int64, silent bool) error {
	return b.call(ctx, "pinChatMessage", map[string]any{"chat_id": chatID, "message_id": messageID, "disable_notification": silent}, nil)
}

// UnpinChatMessage unpins one message (0: the latest pinned one).
func (b *Bot) UnpinChatMessage(ctx context.Context, chatID, messageID int64) error {
	payload := map[string]any{"chat_id": chatID}
	if messageID > 0 {
		payload["message_id"] = messageID
	}
	return b.call(ctx, "unpinChatMessage", payload, nil)
}

// UnpinAllChatMessages clears the pinned messages.
func (b *Bot) UnpinAllChatMessages(ctx context.Context, chatID int64) error {
	return b.call(ctx, "unpinAllChatMessages", map[string]any{"chat_id": chatID}, nil)
}

// CreateChatInviteLink makes an extra invite link: a name, an expiry (zero:
// none), a member limit (0: none) or join requests the admins approve.
func (b *Bot) CreateChatInviteLink(ctx context.Context, chatID int64, name string, expire time.Time, limit int, requests bool) (string, error) {
	payload := map[string]any{"chat_id": chatID}
	if name != "" {
		payload["name"] = name
	}
	if !expire.IsZero() {
		payload["expire_date"] = expire.Unix()
	}
	if requests {
		payload["creates_join_request"] = true
	} else if limit > 0 {
		payload["member_limit"] = limit
	}
	var link struct {
		InviteLink string `json:"invite_link"`
	}
	err := b.call(ctx, "createChatInviteLink", payload, &link)
	return link.InviteLink, err
}

// SetChatTitle renames a group.
func (b *Bot) SetChatTitle(ctx context.Context, chatID int64, title string) error {
	return b.call(ctx, "setChatTitle", map[string]any{"chat_id": chatID, "title": title}, nil)
}

// SetChatDescription changes a group's description.
func (b *Bot) SetChatDescription(ctx context.Context, chatID int64, description string) error {
	return b.call(ctx, "setChatDescription", map[string]any{"chat_id": chatID, "description": description}, nil)
}
