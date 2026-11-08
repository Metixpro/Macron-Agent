package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

type commandRole uint8

const (
	commandUser commandRole = iota
	commandAdmin
	commandOwner
)

type commandSpec struct {
	names string
	fa    string
	en    string
	role  commandRole
}

var commandSpecs = []commandSpec{
	{"start", "شروع و معرفی", "Start and introduction", commandUser},
	{"help", "راهنما", "Help", commandUser},
	{"queue", "کار مستقل بعدی", "Queue a separate task", commandUser},
	{"new reset clear", "گفتگوی تازه", "New conversation", commandUser},
	{"memory", "حافظه و فایل‌ها", "Memory and files", commandUser},
	{"mode", "انتخاب حالت پاسخ", "Choose response mode", commandUser},
	{"fast", "پاسخ سریع", "Fast response mode", commandUser},
	{"deep", "پاسخ عمیق", "Deep response mode", commandUser},
	{"files", "مدیریت فایل‌ها", "Manage files", commandUser},
	{"cookies cookie", "مدیریت کوکی دانلود", "Manage download cookies", commandUser},
	{"reminders schedules", "یادآورها و زمان‌بندی", "Reminders and schedules", commandUser},
	{"tasks", "کارهای پس‌زمینه", "Background tasks", commandUser},
	{"search", "جستجوی گفتگوهای قبلی", "Search past conversations", commandOwner},
	{"board", "برد کارها", "Task board", commandOwner},
	{"dashboard", "داشبورد وب", "Web dashboard", commandOwner},
	{"settings panel admin", "پنل مدیریت", "Administration panel", commandAdmin},
	{"update", "بروزرسانی از گیت‌هاب", "Update from GitHub", commandOwner},
	{"learn", "مدیریت یادگیری", "Manage learning", commandAdmin},
	{"teach", "یاد دادن یک درس", "Teach a lesson", commandAdmin},
	{"moa", "رأی‌گیری چندمدلی", "Multi-model voting", commandAdmin},
	{"approve", "تأیید دستور منتظر", "Approve a pending command", commandAdmin},
	{"yolo", "تنظیم تأیید دستورات", "Configure command approval", commandOwner},
	{"language lang zaban", "زبان ربات", "Bot language", commandUser},
	{"id", "شناسه کاربر و گفتگو", "User and chat IDs", commandUser},
	{"me", "وضعیت حساب و مصرف", "Account and usage", commandUser},
	{"cancel stop", "توقف کار فعلی", "Stop the current task", commandUser},
}

func commandMenu(l lang, role commandRole) []telegram.BotCommand {
	var out []telegram.BotCommand
	for _, spec := range commandSpecs {
		if spec.role > role {
			continue
		}
		description := spec.fa
		if l == en {
			description = spec.en
		}
		for _, name := range strings.Fields(spec.names) {
			out = append(out, telegram.BotCommand{Command: name, Description: description})
		}
	}
	return out
}

func personalCommands(l lang) []telegram.BotCommand      { return commandMenu(l, commandUser) }
func (b *Bot) userCommands(l lang) []telegram.BotCommand { return personalCommands(l) }

var menuLangs = []struct {
	l    lang
	code string
}{{en, ""}, {en, "en"}, {fa, "fa"}}

func (b *Bot) registerCommands(ctx context.Context) error {
	var errs []error
	for _, m := range menuLangs {
		for _, scope := range []*telegram.BotCommandScope{nil, {Type: "all_group_chats"}, {Type: "all_private_chats"}} {
			var commands []telegram.BotCommand
			if scope != nil && scope.Type == "all_private_chats" {
				commands = b.userCommands(m.l)
			}
			if err := b.api.SetMyCommandsFor(ctx, commands, scope, m.code); err != nil {
				errs = append(errs, fmt.Errorf("register commands (language %q, scope %v): %w", m.code, scope, err))
			}
		}
	}
	errs = append(errs, b.registerAdminCommands(ctx))
	return errors.Join(errs...)
}

func (b *Bot) registerAdminCommands(ctx context.Context) error {
	ids, err := b.st.Admins(ctx)
	if err != nil {
		return fmt.Errorf("load command-menu admins: %w", err)
	}
	for id := range b.owner {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	ids = slices.Compact(ids)
	var errs []error
	for _, m := range menuLangs {
		for _, id := range ids {
			role := commandAdmin
			if b.isOwner(id) {
				role = commandOwner
			}
			if err := b.api.SetMyCommandsFor(ctx, commandMenu(m.l, role), &telegram.BotCommandScope{Type: "chat", ChatID: id}, m.code); err != nil {
				errs = append(errs, fmt.Errorf("register command menu for %d (%s): %w", id, m.code, err))
			}
		}
	}
	return errors.Join(errs...)
}
