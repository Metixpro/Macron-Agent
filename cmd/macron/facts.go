package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	toolRemember   = "remember"
	toolForget     = "forget"
	factsPerPage   = 10
	factsDeleteRow = 5

	aboutUser = "user"
	aboutWork = "work"
)

func (b *Bot) factsOn(ctx context.Context, userID int64) bool {
	v, err := b.st.UserPref(ctx, userID, store.PrefFactsOff)
	return err == nil && v == ""
}

func (b *Bot) notesOf(about string) *brain.Notes {
	if about == aboutWork {
		return b.brain.Memory
	}
	return b.brain.User
}

type memItem struct {
	about string
	id    int
	text  string
}

func (b *Bot) memoryItems() ([]memItem, error) {
	var out []memItem
	for _, about := range []string{aboutUser, aboutWork} {
		list, err := b.notesOf(about).List()
		if err != nil {
			return nil, err
		}
		for _, e := range list {
			out = append(out, memItem{about: about, id: e.ID, text: e.Text})
		}
	}
	return out, nil
}

func (b *Bot) factsAbout(ctx context.Context, userID int64, public bool) string {
	if !b.factsOn(ctx, userID) {
		return ""
	}
	items, err := b.memoryItems()
	if err != nil || len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("What you remember (long-term memory in USER.md, about the owner, and MEMORY.md, about their work and machine; the numbers are for remember/forget; the owner manages it with /memory or by editing the files):")
	for _, it := range items {
		tag := "user"
		if it.about == aboutWork {
			tag = "work"
		}
		fmt.Fprintf(&sb, "\n[%s #%d] %s", tag, it.id, it.text)
	}
	if public {
		sb.WriteString("\nThis chat is not private: use these only when they help, and never reveal private ones in front of others.")
	}
	return sb.String()
}

func (b *Bot) factTools(u *store.User) []agent.LocalTool {
	return []agent.LocalTool{{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolRemember,
			Description: "Save a lasting fact. about=user (default): the owner ('Lives in Mashhad'); about=work: their work and machine ('Deploys with make ship'). Never secrets.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"fact":     map[string]any{"type": "string"},
				"about":    map[string]any{"type": "string", "enum": []string{aboutUser, aboutWork}},
				"replaces": map[string]any{"type": "integer", "description": "Number of the fact it corrects"},
			}, "required": []string{"fact"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			fact, _ := args["fact"].(string)
			fact = textutil.OneLine(strings.TrimSpace(fact))
			if fact == "" {
				return agent.ToolOutput{Text: "error: fact is required"}, nil
			}
			if looksSecret(fact) {
				return agent.ToolOutput{Text: "error: this looks like a secret (a password, card or ID number); it is not saved. Tell the user you don't keep such things."}, nil
			}
			about, _ := args["about"].(string)
			if about != aboutWork {
				about = aboutUser
			}
			notes := b.notesOf(about)
			if id := int(numArg(args["replaces"])); id > 0 {
				ok, err := notes.Replace(id, fact)
				if err != nil {
					return agent.ToolOutput{}, err
				}
				if ok {
					return agent.ToolOutput{Text: fmt.Sprintf("Memory #%d updated.", id)}, nil
				}
			}
			id, existed, err := notes.Add(fact)
			switch {
			case errors.Is(err, brain.ErrFull), errors.Is(err, brain.ErrTooLong):
				return agent.ToolOutput{Text: fmt.Sprintf("error: this memory file is full (%d facts at most). Merge or forget outdated ones first, or tell the owner to clean up /memory.", brain.MaxEntries)}, nil
			case err != nil:
				return agent.ToolOutput{}, err
			case existed:
				return agent.ToolOutput{Text: fmt.Sprintf("Already remembered as #%d.", id)}, nil
			}
			return agent.ToolOutput{Text: fmt.Sprintf("Saved as #%d. Mention briefly that you'll remember it.", id)}, nil
		},
	}, {
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolForget,
			Description: "Delete a remembered fact (asked to forget, or no longer true).",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"id":    map[string]any{"type": "integer"},
				"about": map[string]any{"type": "string", "enum": []string{aboutUser, aboutWork}},
			}, "required": []string{"id"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			id := int(numArg(args["id"]))
			about, _ := args["about"].(string)
			if about != aboutWork {
				about = aboutUser
			}
			ok, err := b.notesOf(about).Delete(id)
			if err != nil {
				return agent.ToolOutput{}, err
			}
			if !ok {
				return agent.ToolOutput{Text: fmt.Sprintf("error: there is no memory #%d", id)}, nil
			}
			return agent.ToolOutput{Text: fmt.Sprintf("Memory #%d forgotten.", id)}, nil
		},
	}}
}

func looksSecret(s string) bool {
	l := strings.ToLower(s)
	for _, w := range []string{"password", "passwd", "رمز", "پسورد", "cvv", "cvv2", "otp", "pin code", "api key", "apikey", "secret key", "private key", "seed phrase"} {
		if strings.Contains(l, w) {
			return true
		}
	}
	run := 0
	for _, r := range toASCIIDigits(s) {
		switch {
		case r >= '0' && r <= '9':
			run++
			if run >= 10 {
				return true
			}
		case r == ' ' || r == '-':
		default:
			run = 0
		}
	}
	return false
}

func toASCIIDigits(s string) string {
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

func (b *Bot) openMemoryPanel(ctx context.Context, m *telegram.Message) {
	v := b.viewMemory(ctx, m.From.ID, 0)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func (b *Bot) viewMemory(ctx context.Context, uid int64, page int) panelView {
	l := b.lang(uid)
	items, err := b.memoryItems()
	if err != nil {
		return panelView{html: l.t(textError)}
	}
	on := b.factsOn(ctx, uid)
	id := strconv.FormatInt(uid, 10)
	var sb strings.Builder
	sb.WriteString(l.t("🧠 <b>حافظه من درباره تو</b>\n"))
	if !on {
		sb.WriteString(l.t("\n⏸ حافظه بلندمدت <b>خاموشه</b>: چیز تازه‌ای یاد نمی‌گیرم و از اینا هم استفاده نمی‌کنم.\n"))
	}
	pages := max(1, (len(items)+factsPerPage-1)/factsPerPage)
	page = max(0, min(page, pages-1))
	start, end := page*factsPerPage, min(len(items), (page+1)*factsPerPage)
	if len(items) == 0 {
		sb.WriteString(l.t("\nهنوز چیزی درباره‌ات یادم نمونده. هر وقت خواستی بگو «یادت باشه …» تا نگهش دارم."))
	} else {
		fmt.Fprint(&sb, l.f("\nچیزهایی که یادم مونده (%s مورد):\n", l.num(len(items))))
		last := ""
		for i, it := range items[start:end] {
			if it.about != last {
				last = it.about
				if it.about == aboutUser {
					sb.WriteString(l.t("\n🧑 <b>درباره تو</b> (USER.md)"))
				} else {
					sb.WriteString(l.t("\n🛠 <b>درباره کار و سیستم</b> (MEMORY.md)"))
				}
			}
			fmt.Fprintf(&sb, "\n<b>%s.</b> %s", l.num(start+i+1), esc(it.text))
		}
		sb.WriteString(l.t("\n\nبرای پاک کردن هر مورد، دکمه شماره‌اش رو بزن. برای اضافه کردن کافیه بگی «یادت باشه …»."))
	}
	sb.WriteString(l.t("\n\n📄 همه‌اش فایله: SOUL.md، USER.md و MEMORY.md توی پوشه خونه‌ام. با «📄 فایل‌ها» می‌گیریشون، و هر جا ویرایششون کنی از پیام بعدی اعمال می‌شن."))
	if mem, err := b.mem.Load(ctx, uid); err == nil && mem != nil && mem.Tokens > 0 {
		fmt.Fprint(&sb, l.f("\n\n💬 حافظه گفتگو: %s از %s توکن", humanInt(mem.Tokens), humanInt(mem.Budget)))
	}

	var rows [][]telegram.InlineKeyboardButton
	var cur []telegram.InlineKeyboardButton
	for i, it := range items[start:end] {
		kind := "u"
		if it.about == aboutWork {
			kind = "w"
		}
		cur = append(cur, b.button("🗑 "+l.num(start+i+1), fmt.Sprintf("m:d:%s:%s:%d:%d", id, kind, it.id, page), telegram.StyleDanger))
		if len(cur) == factsDeleteRow {
			rows, cur = append(rows, cur), nil
		}
	}
	rows = append(rows, cur)
	if pages > 1 {
		var nav []telegram.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, b.button(l.t("« قبلی"), fmt.Sprintf("m:h:%s:%d", id, page-1), ""))
		}
		nav = append(nav, b.button(l.num(page+1)+"/"+l.num(pages), fmt.Sprintf("m:h:%s:%d", id, page), ""))
		if page < pages-1 {
			nav = append(nav, b.button(l.t("بعدی »"), fmt.Sprintf("m:h:%s:%d", id, page+1), ""))
		}
		rows = append(rows, nav)
	}
	toggle := b.button(l.t("⏸ خاموش کردن حافظه"), fmt.Sprintf("m:t:%s:%d", id, page), "")
	if !on {
		toggle = b.button(l.t("▶️ روشن کردن حافظه"), fmt.Sprintf("m:t:%s:%d", id, page), telegram.StyleSuccess)
	}
	rows = append(rows, row(toggle, b.button(l.t("🔄 گفتگوی تازه"), "m:n:"+id, "")))
	last := []telegram.InlineKeyboardButton{b.button(l.t("📄 فایل‌ها"), "m:f:"+id, telegram.StylePrimary)}
	if len(items) > 0 {
		last = append(last, b.button(l.t("🧹 فراموش کردن همه"), "m:c:"+id, telegram.StyleDanger))
	}
	rows = append(rows, last, row(b.button(l.t("✖️ بستن"), "m:x:"+id, "")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) sendBrainFiles(ctx context.Context, l lang, chatID int64) {
	for _, f := range []struct{ name, text, caption string }{
		{brain.SoulFile, b.currentSoul(), l.t("🧬 روح من: شخصیت و لحن")},
		{brain.UserFile, b.brain.User.Raw(), l.t("🧑 اینا رو درباره تو می‌دونم")},
		{brain.MemoryFile, b.brain.Memory.Raw(), l.t("🛠 اینا رو از کار و سیستم یاد گرفتم")},
	} {
		doc := telegram.File{Name: f.name, Reader: strings.NewReader(f.text)}
		if err := b.api.SendDocumentOpts(ctx, chatID, doc, f.caption, telegram.SendOptions{}); err != nil && !telegram.IsUnreachable(err) {
			log.Printf("[chat %d] memory files: %v", chatID, err)
		}
	}
}

func (b *Bot) handleMemoryCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		answer("", false)
		return
	}
	uid, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || uid != q.From.ID {
		answer(l.t("این پنل مال کس دیگه‌ای است؛ برای حافظه خودت /memory رو بزن."), true)
		return
	}
	arg := func(i int) int64 {
		if i < len(parts) {
			n, _ := strconv.ParseInt(parts[i], 10, 64)
			return n
		}
		return 0
	}
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	show := func(v panelView) {
		if err := b.editHTML(ctx, chatID, msgID, v.html, v.kb); err != nil && !telegram.IsNotModified(err) {
			log.Printf("[chat %d] memory panel: %v", chatID, err)
		}
	}
	id := strconv.FormatInt(uid, 10)
	switch parts[1] {
	case "h":
		answer("", false)
		show(b.viewMemory(ctx, uid, int(arg(3))))
	case "d":
		about := aboutUser
		if len(parts) > 3 && parts[3] == "w" {
			about = aboutWork
		}
		if ok, err := b.notesOf(about).Delete(int(arg(4))); err != nil {
			answer(l.t(textError), true)
			return
		} else if ok {
			answer(l.t("🗑 فراموشش کردم"), false)
		} else {
			answer(l.t("این مورد قبلاً پاک شده بود"), false)
		}
		show(b.viewMemory(ctx, uid, int(arg(5))))
	case "c":
		answer("", false)
		show(panelView{html: l.t("🧹 <b>همه چیزهایی که درباره‌ات یادمه پاک بشه؟</b>\n\nاین کار برگشت نداره."),
			kb: keyboard(row(b.button(l.t("✅ آره، همه رو فراموش کن"), "m:C:"+id, telegram.StyleDanger), b.button(l.t("⬅️ نه"), "m:h:"+id+":0", "")))})
	case "C":
		total := 0
		for _, about := range []string{aboutUser, aboutWork} {
			n, err := b.notesOf(about).Clear()
			if err != nil {
				answer(l.t(textError), true)
				return
			}
			total += n
		}
		answer(l.f("🧹 %s مورد فراموش شد", l.num(total)), false)
		show(b.viewMemory(ctx, uid, 0))
	case "t":
		value := "1"
		if !b.factsOn(ctx, uid) {
			value = ""
		}
		if err := b.st.SetUserPref(ctx, uid, store.PrefFactsOff, value); err != nil {
			answer(l.t(textError), true)
			return
		}
		if value == "" {
			answer(l.t("▶️ حافظه روشن شد"), false)
		} else {
			answer(l.t("⏸ حافظه خاموش شد"), false)
		}
		show(b.viewMemory(ctx, uid, int(arg(3))))
	case "n":
		if err := b.mem.Reset(ctx, uid); err != nil {
			answer(l.t(textError), true)
			return
		}
		answer(l.t("🔄 گفتگو از نو شروع شد؛ چیزهایی که یادم مونده سر جاشونه"), true)
		show(b.viewMemory(ctx, uid, 0))
	case "f":
		answer("", false)
		b.sendBrainFiles(ctx, l, chatID)
	case "x":
		answer("", false)
		_ = b.api.DeleteMessage(ctx, chatID, msgID)
	default:
		answer("", false)
	}
}
