package main

import (
	"fmt"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

type adminRight struct {
	name  string
	field func(*telegram.AdminRights) *bool
}

var adminRightList = []adminRight{
	{"change_info", func(r *telegram.AdminRights) *bool { return &r.CanChangeInfo }},
	{"delete_messages", func(r *telegram.AdminRights) *bool { return &r.CanDeleteMessages }},
	{"restrict_members", func(r *telegram.AdminRights) *bool { return &r.CanRestrictMembers }},
	{"invite_users", func(r *telegram.AdminRights) *bool { return &r.CanInviteUsers }},
	{"pin_messages", func(r *telegram.AdminRights) *bool { return &r.CanPinMessages }},
	{"manage_topics", func(r *telegram.AdminRights) *bool { return &r.CanManageTopics }},
	{"manage_video_chats", func(r *telegram.AdminRights) *bool { return &r.CanManageVideoChats }},
	{"promote_members", func(r *telegram.AdminRights) *bool { return &r.CanPromoteMembers }},
	{"anonymous", func(r *telegram.AdminRights) *bool { return &r.IsAnonymous }},
	{"post_stories", func(r *telegram.AdminRights) *bool { return &r.CanPostStories }},
	{"edit_stories", func(r *telegram.AdminRights) *bool { return &r.CanEditStories }},
	{"delete_stories", func(r *telegram.AdminRights) *bool { return &r.CanDeleteStories }},
}

var rightPresets = map[string][]string{
	"moderator": {"delete_messages", "restrict_members", "pin_messages", "invite_users", "manage_video_chats"},
	"full": {"change_info", "delete_messages", "restrict_members", "invite_users", "pin_messages", "manage_topics", "manage_video_chats",
		"promote_members", "post_stories", "edit_stories", "delete_stories"},
	"helper":  {"pin_messages", "invite_users", "manage_video_chats"},
	"cleaner": {"delete_messages"},
}

var presetAliases = map[string]string{"mod": "moderator", "moderator": "moderator", "admin": "moderator", "full": "full", "all": "full",
	"everything": "full", "helper": "helper", "assistant": "helper", "cleaner": "cleaner", "janitor": "cleaner"}

var rightAliases = map[string][]string{
	"info": {"change_info"}, "change_info": {"change_info"}, "edit_info": {"change_info"},
	"delete": {"delete_messages"}, "delete_messages": {"delete_messages"}, "del": {"delete_messages"}, "clean": {"delete_messages"},
	"ban": {"restrict_members"}, "restrict": {"restrict_members"}, "restrict_members": {"restrict_members"}, "mute": {"restrict_members"},
	"kick":   {"restrict_members"},
	"invite": {"invite_users"}, "invite_users": {"invite_users"}, "invite_links": {"invite_users"}, "add_members": {"invite_users"},
	"pin": {"pin_messages"}, "pin_messages": {"pin_messages"}, "pins": {"pin_messages"},
	"topics": {"manage_topics"}, "manage_topics": {"manage_topics"},
	"video": {"manage_video_chats"}, "video_chats": {"manage_video_chats"}, "manage_video_chats": {"manage_video_chats"},
	"voice_chats": {"manage_video_chats"}, "calls": {"manage_video_chats"}, "live": {"manage_video_chats"},
	"promote": {"promote_members"}, "promote_members": {"promote_members"}, "add_admins": {"promote_members"},
	"admins":    {"promote_members"},
	"anonymous": {"anonymous"}, "anon": {"anonymous"}, "is_anonymous": {"anonymous"},
	"stories":      {"post_stories", "edit_stories", "delete_stories"},
	"post_stories": {"post_stories"}, "edit_stories": {"edit_stories"}, "delete_stories": {"delete_stories"},
}

func normWord(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "_", "-", "_").Replace(s)
	return strings.TrimPrefix(s, "can_")
}

func signed(item string) (word string, on, absolute bool) {
	item = strings.TrimSpace(item)
	switch {
	case strings.HasPrefix(item, "+"):
		return normWord(item[1:]), true, false
	case strings.HasPrefix(item, "-"), strings.HasPrefix(item, "−"):
		return normWord(strings.TrimLeft(item, "-−")), false, false
	}
	return normWord(item), true, true
}

func setRight(r *telegram.AdminRights, name string, on bool) {
	for _, ar := range adminRightList {
		if ar.name == name {
			*ar.field(r) = on
		}
	}
}

func rightNamesOf(r telegram.AdminRights) []string {
	var out []string
	for _, ar := range adminRightList {
		if *ar.field(&r) {
			out = append(out, ar.name)
		}
	}
	return out
}

func parseRights(items []string, base telegram.AdminRights) (telegram.AdminRights, error) {
	r := base
	for _, it := range items {
		if _, _, abs := signed(it); abs {
			r = telegram.AdminRights{}
			break
		}
	}
	for _, it := range items {
		word, on, _ := signed(it)
		if word == "" {
			continue
		}
		if p, ok := presetAliases[word]; ok {
			for _, name := range rightPresets[p] {
				setRight(&r, name, on)
			}
			continue
		}
		names, ok := rightAliases[word]
		if !ok {
			return r, fmt.Errorf("unknown right %q: use a preset (moderator, full, helper, cleaner) or %s", it, strings.Join(allRightNames(), ", "))
		}
		for _, name := range names {
			setRight(&r, name, on)
		}
	}
	r.CanManageChat = true
	return r, nil
}

func allRightNames() []string {
	out := make([]string, len(adminRightList))
	for i, ar := range adminRightList {
		out[i] = ar.name
	}
	return out
}

func capRights(want telegram.AdminRights, asker, me *telegram.ChatMember) (telegram.AdminRights, []string) {
	got := want
	var dropped []string
	for _, ar := range adminRightList {
		if !*ar.field(&got) {
			continue
		}
		switch {
		case asker != nil && !*ar.field(ptr(asker.Rights())):
			*ar.field(&got) = false
			dropped = append(dropped, ar.name+" (you don't have it yourself)")
		case me != nil && !*ar.field(ptr(me.Rights())):
			*ar.field(&got) = false
			dropped = append(dropped, ar.name+" (I don't have it myself)")
		}
	}
	return got, dropped
}

func ptr[T any](v T) *T { return &v }

func beyond(r telegram.AdminRights, limit *telegram.ChatMember) []string {
	if limit == nil {
		return nil
	}
	have := limit.Rights()
	var out []string
	for _, ar := range adminRightList {
		if *ar.field(&r) && !*ar.field(&have) {
			out = append(out, ar.name)
		}
	}
	return out
}

func rightsText(r telegram.AdminRights) string {
	names := rightNamesOf(r)
	if len(names) == 0 {
		return "no special rights"
	}
	return strings.Join(names, ", ")
}

type memberPerm struct {
	name  string
	field func(*telegram.ChatPermissions) *bool
}

var memberPermList = []memberPerm{
	{"messages", func(p *telegram.ChatPermissions) *bool { return &p.CanSendMessages }},
	{"photos", func(p *telegram.ChatPermissions) *bool { return &p.CanSendPhotos }},
	{"videos", func(p *telegram.ChatPermissions) *bool { return &p.CanSendVideos }},
	{"video_notes", func(p *telegram.ChatPermissions) *bool { return &p.CanSendVideoNotes }},
	{"voice", func(p *telegram.ChatPermissions) *bool { return &p.CanSendVoiceNotes }},
	{"audios", func(p *telegram.ChatPermissions) *bool { return &p.CanSendAudios }},
	{"documents", func(p *telegram.ChatPermissions) *bool { return &p.CanSendDocuments }},
	{"stickers", func(p *telegram.ChatPermissions) *bool { return &p.CanSendOtherMessages }},
	{"polls", func(p *telegram.ChatPermissions) *bool { return &p.CanSendPolls }},
	{"links", func(p *telegram.ChatPermissions) *bool { return &p.CanAddWebPagePreviews }},
	{"invite", func(p *telegram.ChatPermissions) *bool { return &p.CanInviteUsers }},
	{"pin", func(p *telegram.ChatPermissions) *bool { return &p.CanPinMessages }},
	{"info", func(p *telegram.ChatPermissions) *bool { return &p.CanChangeInfo }},
	{"topics", func(p *telegram.ChatPermissions) *bool { return &p.CanManageTopics }},
}

var mediaPerms = []string{"photos", "videos", "video_notes", "voice", "audios", "documents"}

var permAliases = map[string][]string{
	"messages": {"messages"}, "text": {"messages"}, "send_messages": {"messages"}, "msg": {"messages"}, "write": {"messages"},
	"media":  mediaPerms,
	"photos": {"photos"}, "photo": {"photos"}, "pictures": {"photos"}, "images": {"photos"}, "send_photos": {"photos"},
	"videos": {"videos"}, "video": {"videos"}, "send_videos": {"videos"},
	"video_notes": {"video_notes"}, "round": {"video_notes"}, "circles": {"video_notes"}, "send_video_notes": {"video_notes"},
	"voice": {"voice"}, "voices": {"voice"}, "voice_notes": {"voice"}, "send_voice_notes": {"voice"},
	"audios": {"audios"}, "audio": {"audios"}, "music": {"audios"}, "send_audios": {"audios"},
	"documents": {"documents"}, "files": {"documents"}, "docs": {"documents"}, "send_documents": {"documents"},
	"stickers": {"stickers"}, "gifs": {"stickers"}, "games": {"stickers"}, "inline": {"stickers"}, "other": {"stickers"},
	"send_other_messages": {"stickers"},
	"polls":               {"polls"}, "send_polls": {"polls"},
	"links": {"links"}, "previews": {"links"}, "link_previews": {"links"}, "add_web_page_previews": {"links"},
	"invite": {"invite"}, "invite_users": {"invite"},
	"pin": {"pin"}, "pin_messages": {"pin"},
	"info": {"info"}, "change_info": {"info"},
	"topics": {"topics"}, "manage_topics": {"topics"},
}

func setPerm(p *telegram.ChatPermissions, name string, on bool) {
	for _, mp := range memberPermList {
		if mp.name == name {
			*mp.field(p) = on
		}
	}
}

func parsePerms(items []string, base telegram.ChatPermissions) (telegram.ChatPermissions, error) {
	p := base
	for _, it := range items {
		word, on, abs := signed(it)
		if abs {
			on = false
		}
		var names []string
		switch word {
		case "":
			continue
		case "all", "everything":
			for _, mp := range memberPermList {
				names = append(names, mp.name)
			}
		default:
			var ok bool
			if names, ok = permAliases[word]; !ok {
				return p, fmt.Errorf("unknown permission %q: use media, messages, photos, videos, video_notes, voice, audios, documents, "+
					"stickers, polls, links, invite, pin, info, topics or all", it)
			}
		}
		for _, name := range names {
			setPerm(&p, name, on)
		}
	}
	return p, nil
}

func permsText(p telegram.ChatPermissions) (allowed, blocked []string) {
	for _, mp := range memberPermList {
		if *mp.field(&p) {
			allowed = append(allowed, mp.name)
		} else {
			blocked = append(blocked, mp.name)
		}
	}
	return allowed, blocked
}
