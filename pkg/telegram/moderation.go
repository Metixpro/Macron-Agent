package telegram

import (
	"context"
	"time"
)

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

func MemberPermissions() ChatPermissions {
	return ChatPermissions{CanSendMessages: true, CanSendAudios: true, CanSendDocuments: true, CanSendPhotos: true, CanSendVideos: true,
		CanSendVideoNotes: true, CanSendVoiceNotes: true, CanSendPolls: true, CanSendOtherMessages: true, CanAddWebPagePreviews: true,
		CanInviteUsers: true}
}

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

func (b *Bot) BanChatMember(ctx context.Context, chatID, userID int64, until time.Time, revoke bool) error {
	return b.call(ctx, "banChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "until_date": untilUnix(until),
		"revoke_messages": revoke}, nil)
}

func (b *Bot) UnbanChatMember(ctx context.Context, chatID, userID int64) error {
	return b.call(ctx, "unbanChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "only_if_banned": true}, nil)
}

func (b *Bot) RestrictChatMember(ctx context.Context, chatID, userID int64, perms ChatPermissions, until time.Time) error {
	return b.call(ctx, "restrictChatMember", map[string]any{"chat_id": chatID, "user_id": userID, "permissions": perms,
		"use_independent_chat_permissions": true, "until_date": untilUnix(until)}, nil)
}

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

func (b *Bot) SetChatAdministratorCustomTitle(ctx context.Context, chatID, userID int64, title string) error {
	return b.call(ctx, "setChatAdministratorCustomTitle", map[string]any{"chat_id": chatID, "user_id": userID, "custom_title": title}, nil)
}

func (b *Bot) SetChatPermissions(ctx context.Context, chatID int64, perms ChatPermissions) error {
	return b.call(ctx, "setChatPermissions", map[string]any{"chat_id": chatID, "permissions": perms, "use_independent_chat_permissions": true}, nil)
}

func (b *Bot) DeleteMessages(ctx context.Context, chatID int64, ids []int64) error {
	return b.call(ctx, "deleteMessages", map[string]any{"chat_id": chatID, "message_ids": ids}, nil)
}

func (b *Bot) PinChatMessage(ctx context.Context, chatID, messageID int64, silent bool) error {
	return b.call(ctx, "pinChatMessage", map[string]any{"chat_id": chatID, "message_id": messageID, "disable_notification": silent}, nil)
}

func (b *Bot) UnpinChatMessage(ctx context.Context, chatID, messageID int64) error {
	payload := map[string]any{"chat_id": chatID}
	if messageID > 0 {
		payload["message_id"] = messageID
	}
	return b.call(ctx, "unpinChatMessage", payload, nil)
}

func (b *Bot) UnpinAllChatMessages(ctx context.Context, chatID int64) error {
	return b.call(ctx, "unpinAllChatMessages", map[string]any{"chat_id": chatID}, nil)
}

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

func (b *Bot) SetChatTitle(ctx context.Context, chatID int64, title string) error {
	return b.call(ctx, "setChatTitle", map[string]any{"chat_id": chatID, "title": title}, nil)
}

func (b *Bot) SetChatDescription(ctx context.Context, chatID int64, description string) error {
	return b.call(ctx, "setChatDescription", map[string]any{"chat_id": chatID, "description": description}, nil)
}
