package main

import (
	"archive/zip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	filesPerPage = 8
	maxSendAll   = 60
	maxZipFiles  = 2000
)

type fileCategory struct {
	key, icon, title string
	exts             []string
}

var fileCategories = []fileCategory{
	{"img", "🖼", "تصویرها", []string{".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp", ".svg", ".heic", ".tiff", ".ico"}},
	{"vid", "🎬", "ویدیوها", []string{".mp4", ".mov", ".mkv", ".webm", ".avi", ".m4v", ".3gp"}},
	{"aud", "🎵", "صوتی و موزیک", []string{".mp3", ".m4a", ".wav", ".ogg", ".oga", ".opus", ".flac", ".aac"}},
	{"doc", "📄", "اسناد", []string{".pdf", ".docx", ".doc", ".xlsx", ".xls", ".pptx", ".ppt", ".csv", ".txt", ".md", ".rtf", ".odt", ".epub", ".srt"}},
	{"code", "💻", "کد و داده", []string{".py", ".js", ".ts", ".go", ".java", ".c", ".cpp", ".h", ".rs", ".rb", ".php", ".sh", ".html", ".css",
		".json", ".yaml", ".yml", ".xml", ".sql", ".ipynb", ".kt", ".swift", ".cs", ".lua", ".r", ".toml", ".ini", ".log"}},
	{"arc", "📦", "فشرده", []string{".zip", ".tar", ".gz", ".tgz", ".rar", ".7z", ".xz", ".bz2"}},
	{"etc", "🗂", "سایر", nil},
}

var categoryAll = fileCategory{"all", "🕘", "همه (جدیدترین اول)", nil}

func categoryOf(name string) fileCategory {
	ext := strings.ToLower(filepath.Ext(name))
	for _, c := range fileCategories {
		for _, e := range c.exts {
			if e == ext {
				return c
			}
		}
	}
	return fileCategories[len(fileCategories)-1]
}

func findCategory(key string) (fileCategory, bool) {
	if key == categoryAll.key {
		return categoryAll, true
	}
	for _, c := range fileCategories {
		if c.key == key {
			return c, true
		}
	}
	return fileCategory{}, false
}

func fileHash(name string) string {
	sum := sha1.Sum([]byte(name))
	return hex.EncodeToString(sum[:4])
}

func (b *Bot) panelFiles(session string) (files []machine.FileEntry, nested int) {
	all, _ := b.app.Machine.List(session, 8, 20000)
	for _, f := range all {
		if f.IsDir || f.Symlink || f.Size == 0 || hiddenPath(f.Name) {
			continue
		}
		if strings.Contains(f.Name, "/") {
			nested++
			continue
		}
		files = append(files, f)
	}
	sort.SliceStable(files, func(i, j int) bool { return files[i].ModTime.After(files[j].ModTime) })
	return files, nested
}

func hiddenPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") || strings.HasSuffix(part, "~") {
			return true
		}
	}
	return false
}

func inCategory(files []machine.FileEntry, cat fileCategory) []machine.FileEntry {
	if cat.key == categoryAll.key {
		return files
	}
	var out []machine.FileEntry
	for _, f := range files {
		if categoryOf(f.Name).key == cat.key {
			out = append(out, f)
		}
	}
	return out
}

func faNum(n int) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, strconv.Itoa(n))
}

func (b *Bot) openFilePanel(ctx context.Context, m *telegram.Message) {
	v := b.viewFilesHome(m.From.ID)
	b.sendPanel(ctx, m, v.html, v.kb)
}

func (b *Bot) viewFilesHome(uid int64) panelView {
	l := b.lang(uid)
	session := sessionFor(uid)
	files, nested := b.panelFiles(session)
	if len(files) == 0 && nested == 0 {
		return panelView{html: l.t(textNoFiles)}
	}
	var total int64
	count := map[string]int{}
	for _, f := range files {
		total += f.Size
		count[categoryOf(f.Name).key]++
	}
	var sb strings.Builder
	fmt.Fprint(&sb, l.f("📁 <b>فایل‌های فضای کاری</b>\n\n%s فایل · %s", l.num(len(files)), sizeText(total)))
	if nested > 0 {
		fmt.Fprint(&sb, l.f("\n📂 %s فایل هم توی پوشه‌هاست (توی زیپ کامل هست).", l.num(nested)))
	}
	if ttl := b.filesTTL(); ttl > 0 {
		fmt.Fprint(&sb, l.f("\n⏳ فایل‌ها %s بعد از آخرین تغییر خودکار پاک می‌شن.", durationWord(l, ttl)))
	}
	fmt.Fprint(&sb, l.t("\n\nیه دسته رو انتخاب کن 👇"))

	id := strconv.FormatInt(uid, 10)
	var rows [][]telegram.InlineKeyboardButton
	var cur []telegram.InlineKeyboardButton
	for _, c := range fileCategories {
		n := count[c.key]
		if n == 0 {
			continue
		}
		cur = append(cur, b.button(fmt.Sprintf("%s %s (%s)", c.icon, l.t(c.title), l.num(n)), "f:c:"+id+":"+c.key+":0", ""))
		if len(cur) == 2 {
			rows, cur = append(rows, cur), nil
		}
	}
	rows = append(rows, cur)
	if len(files) > 0 {
		rows = append(rows, row(b.button(categoryAll.icon+" "+l.t(categoryAll.title), "f:c:"+id+":all:0", telegram.StylePrimary)))
	}
	rows = append(rows,
		row(b.button(l.t("🗜 دانلود همه (zip)"), "f:z:"+id, telegram.StyleSuccess)),
		row(b.button(l.t("🧹 خالی کردن"), "f:w:"+id, telegram.StyleDanger), b.button(l.t("✖️ بستن"), "f:x:"+id, "")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) viewFilesPage(uid int64, cat fileCategory, page int, deleting bool) panelView {
	l := b.lang(uid)
	files, _ := b.panelFiles(sessionFor(uid))
	list := inCategory(files, cat)
	id := strconv.FormatInt(uid, 10)
	if len(list) == 0 {
		return panelView{html: l.f("%s <b>%s</b>\n\nچیزی اینجا نمونده.", cat.icon, l.t(cat.title)),
			kb: keyboard(row(b.button(l.t("⬅️ دسته‌ها"), "f:h:"+id, "")))}
	}
	pages := (len(list) + filesPerPage - 1) / filesPerPage
	page = max(0, min(page, pages-1))
	start := page * filesPerPage
	end := min(start+filesPerPage, len(list))

	var sb strings.Builder
	fmt.Fprint(&sb, l.f("%s <b>%s</b> · %s فایل", cat.icon, l.t(cat.title), l.num(len(list))))
	if pages > 1 {
		fmt.Fprint(&sb, l.f(" · صفحه %s از %s", l.num(page+1), l.num(pages)))
	}
	if deleting {
		sb.WriteString(l.t("\n\n🗑 <b>حالت حذف:</b> روی هر فایل بزنی، همون لحظه پاک می‌شه."))
	} else {
		sb.WriteString(l.t("\n\nروی هر فایل بزنی، همون یکی برات ارسال می‌شه 👇"))
	}
	for i, f := range list[start:end] {
		fmt.Fprintf(&sb, "\n%s. <code>%s</code> · %s · %s", l.num(start+i+1), esc(f.Name), sizeText(f.Size), b.ago(l, f.ModTime))
	}

	pageArg := ":" + cat.key + ":" + strconv.Itoa(page)
	var rows [][]telegram.InlineKeyboardButton
	for _, f := range list[start:end] {
		label := categoryOf(f.Name).icon + " " + textutil.TruncateRunes(f.Name, 34, "…")
		action, style := "f:s:", ""
		if deleting {
			label, action, style = "🗑 "+textutil.TruncateRunes(f.Name, 34, "…"), "f:r:", telegram.StyleDanger
		}
		rows = append(rows, row(b.button(label, action+id+":"+fileHash(f.Name)+pageArg, style)))
	}
	if pages > 1 {
		mode := "f:c:"
		if deleting {
			mode = "f:d:"
		}
		var nav []telegram.InlineKeyboardButton
		if page > 0 {
			nav = append(nav, b.button(l.t("« قبلی"), mode+id+":"+cat.key+":"+strconv.Itoa(page-1), ""))
		}
		nav = append(nav, b.button(l.num(page+1)+"/"+l.num(pages), mode+id+pageArg, ""))
		if page < pages-1 {
			nav = append(nav, b.button(l.t("بعدی »"), mode+id+":"+cat.key+":"+strconv.Itoa(page+1), ""))
		}
		rows = append(rows, nav)
	}
	if deleting {
		rows = append(rows, row(b.button(l.t("✅ تموم شد"), "f:c:"+id+pageArg, telegram.StyleSuccess)))
	} else {
		all := l.f("📤 ارسال همه (%s)", l.num(len(list)))
		if len(list) > maxSendAll {
			all = l.t("🗜 همه به صورت zip")
		}
		rows = append(rows, row(b.button(all, "f:a:"+id+":"+cat.key, telegram.StylePrimary), b.button(l.t("🗑 حذف"), "f:d:"+id+pageArg, "")))
	}
	rows = append(rows, row(b.button(l.t("⬅️ دسته‌ها"), "f:h:"+id, "")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) ago(l lang, t time.Time) string {
	d := b.st.Now().Sub(t)
	switch {
	case d < time.Minute:
		return l.t("همین الان")
	case d < time.Hour:
		return l.num(int(d/time.Minute)) + l.t(" دقیقه پیش")
	case d < 24*time.Hour:
		return l.num(int(d/time.Hour)) + l.t(" ساعت پیش")
	}
	return t.In(b.loc).Format("01-02 15:04")
}

func (b *Bot) handleFilesCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	parts := strings.Split(q.Data, ":")
	if len(parts) < 3 {
		answer("", false)
		return
	}
	uid, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || (uid != q.From.ID && !b.isAdmin(q.From.ID)) {
		answer(l.t("این پنل مال کس دیگه‌ای است؛ برای فایل‌های خودت /files رو بزن."), true)
		return
	}
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	show := func(v panelView) {
		if err := b.editHTML(ctx, chatID, msgID, v.html, v.kb); err != nil && !telegram.IsNotModified(err) {
			log.Printf("[chat %d] file panel: %v", chatID, err)
		}
	}
	arg := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	page, _ := strconv.Atoi(arg(4))
	session := sessionFor(uid)

	switch parts[1] {
	case "h":
		answer("", false)
		show(b.viewFilesHome(uid))
	case "c", "d":
		cat, ok := findCategory(arg(3))
		if !ok {
			answer("", false)
			return
		}
		answer("", false)
		show(b.viewFilesPage(uid, cat, page, parts[1] == "d"))
	case "s", "r":
		name, ok := b.fileByHash(session, arg(3))
		cat, _ := findCategory(arg(4))
		page, _ = strconv.Atoi(arg(5))
		if !ok {
			answer(l.t("این فایل دیگه نیست (شاید پاک شده)."), true)
			show(b.viewFilesPage(uid, cat, page, parts[1] == "r"))
			return
		}
		if parts[1] == "r" {
			if err := b.app.Machine.Remove(session, name); err != nil {
				answer(l.t("نشد پاکش کنم 😕"), true)
				return
			}
			answer("🗑 «"+textutil.TruncateRunes(name, 40, "…")+l.t("» پاک شد"), false)
			show(b.viewFilesPage(uid, cat, page, true))
			return
		}
		answer(l.t("📤 در حال ارسال…"), false)
		b.withFileJob(uid, answer, func(jctx context.Context) {
			if err := b.sendFile(jctx, chatID, session, name, "", telegram.SendOptions{}); err != nil && !telegram.IsUnreachable(err) {
				log.Printf("[chat %d] sending %q: %v", chatID, name, err)
				b.sendHTML(jctx, chatID, l.f(textSendFailed, esc(name)), telegram.SendOptions{})
			}
		})
	case "a":
		cat, ok := findCategory(arg(3))
		if !ok {
			answer("", false)
			return
		}
		files, _ := b.panelFiles(session)
		list := inCategory(files, cat)
		if len(list) == 0 {
			answer(l.t("چیزی برای ارسال نیست."), true)
			return
		}
		if len(list) > maxSendAll {
			answer(l.t("🗜 تعدادشون زیاده؛ همه رو یه‌جا zip می‌کنم…"), false)
			b.withFileJob(uid, answer, func(jctx context.Context) { b.sendZip(jctx, l, chatID, session, list) })
			return
		}
		answer(l.f("📤 در حال ارسال %s فایل…", l.num(len(list))), false)
		b.withFileJob(uid, answer, func(jctx context.Context) {
			names := make([]string, len(list))
			for i, f := range list {
				names[len(list)-1-i] = f.Name
			}
			for start := 0; start < len(names); start += 10 {
				if start > 0 {
					time.Sleep(time.Second)
				}
				if _, ok := b.sendBatch(jctx, chatID, session, names[start:min(start+10, len(names))], nil, telegram.SendOptions{}); !ok {
					return
				}
			}
		})
	case "z":
		answer(l.t("🗜 دارم zip می‌کنم…"), false)
		b.withFileJob(uid, answer, func(jctx context.Context) { b.sendZip(jctx, l, chatID, session, nil) })
	case "w":
		answer("", false)
		id := strconv.FormatInt(uid, 10)
		show(panelView{html: l.t("🧹 <b>همه فایل‌های فضای کاری پاک بشن؟</b>\n\nاین کار برگشت نداره."),
			kb: keyboard(row(b.button(l.t("✅ آره، همه پاک بشن"), "f:W:"+id, telegram.StyleDanger), b.button(l.t("⬅️ نه"), "f:h:"+id, "")))})
	case "W":
		if err := b.emptyWorkspace(session); err != nil {
			log.Printf("[user %d] emptying the workspace: %v", uid, err)
			answer(l.t("نشد پاکش کنم 😕"), true)
			return
		}
		answer(l.t("🧹 فضای کاری خالی شد"), false)
		show(panelView{html: l.t("🧹 فضای کاری خالی شد.")})
	case "x":
		answer("", false)
		_ = b.api.DeleteMessage(ctx, chatID, msgID)
	default:
		answer("", false)
	}
}

func (b *Bot) emptyWorkspace(session string) error {
	entries, err := b.app.Machine.List(session, 1, 100000)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if hiddenPath(e.Name) {
			continue
		}
		if err := b.app.Machine.RemoveAll(session, e.Name); err != nil {
			return err
		}
	}
	return nil
}

func (b *Bot) fileByHash(session, hash string) (string, bool) {
	files, _ := b.panelFiles(session)
	for _, f := range files {
		if fileHash(f.Name) == hash {
			return f.Name, true
		}
	}
	return "", false
}

func (b *Bot) withFileJob(uid int64, answer func(string, bool), job func(ctx context.Context)) {
	l := b.lang(uid)
	if _, busy := b.fileJobs.LoadOrStore(uid, true); busy {
		answer(l.t("⏳ هنوز دارم فایل‌های قبلی رو می‌فرستم…"), true)
		return
	}
	b.wg.Add(1)
	go func() {
		defer guard.Recover("filepanel")
		defer b.wg.Done()
		defer b.fileJobs.Delete(uid)
		ctx, cancel := b.ctx(15 * time.Minute)
		defer cancel()
		job(ctx)
	}()
}

func (b *Bot) sendZip(ctx context.Context, l lang, chatID int64, session string, list []machine.FileEntry) {
	if list == nil {
		all, _ := b.app.Machine.List(session, 32, maxZipFiles)
		for _, f := range all {
			if !f.IsDir && !f.Symlink && f.Size > 0 && !hiddenPath(f.Name) {
				list = append(list, f)
			}
		}
	}
	if len(list) == 0 {
		b.sendHTML(ctx, chatID, l.t(textNoFiles), telegram.SendOptions{})
		return
	}
	_ = b.api.SendChatAction(ctx, chatID, "upload_document")
	tmp, err := os.CreateTemp("", "waze-zip-*.zip")
	if err != nil {
		log.Printf("zip: %v", err)
		b.sendHTML(ctx, chatID, l.t(textError), telegram.SendOptions{})
		return
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	n, err := b.writeZip(tmp, session, list)
	switch {
	case err == errTooBig:
		b.sendHTML(ctx, chatID, l.f("🗜 زیپ همه فایل‌ها از سقف %s تلگرام بزرگ‌تر می‌شه؛ از داخل دسته‌ها جداجدا بگیرشون.", sizeWord(l, b.api.UploadLimit())), telegram.SendOptions{})
		return
	case err != nil:
		log.Printf("zip: %v", err)
		b.sendHTML(ctx, chatID, l.t(textError), telegram.SendOptions{})
		return
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return
	}
	name := "workspace_" + b.st.Now().In(b.loc).Format("2006-01-02_15-04") + ".zip"
	caption := l.f("🗜 %s فایل از فضای کاری", l.num(n))
	if err := b.api.SendDocumentOpts(ctx, chatID, telegram.File{Name: name, Reader: tmp}, caption, telegram.SendOptions{}); err != nil && !telegram.IsUnreachable(err) {
		log.Printf("[chat %d] sending the zip: %v", chatID, err)
		b.sendHTML(ctx, chatID, l.f(textSendFailed, esc(name)), telegram.SendOptions{})
	}
}

var stored = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true, ".gif": true, ".mp4": true, ".mov": true, ".mkv": true,
	".webm": true, ".mp3": true, ".m4a": true, ".ogg": true, ".opus": true, ".zip": true, ".gz": true, ".tgz": true, ".rar": true, ".7z": true,
	".xz": true, ".bz2": true, ".docx": true, ".xlsx": true, ".pptx": true, ".pdf": true}

func (b *Bot) writeZip(w io.Writer, session string, list []machine.FileEntry) (int, error) {
	cw := &countingWriter{w: w}
	zw := zip.NewWriter(cw)
	n := 0
	for _, f := range list {
		r, info, err := b.app.Machine.Open(session, f.Name)
		if err != nil {
			continue
		}
		h, err := zip.FileInfoHeader(info)
		if err != nil {
			r.Close()
			continue
		}
		h.Name = f.Name
		h.Method = zip.Deflate
		if stored[strings.ToLower(filepath.Ext(f.Name))] {
			h.Method = zip.Store
		}
		dst, err := zw.CreateHeader(h)
		if err == nil {
			_, err = io.Copy(dst, io.LimitReader(r, b.api.UploadLimit()+1))
		}
		r.Close()
		if err != nil {
			return n, err
		}
		if cw.n > b.api.UploadLimit() {
			return n, errTooBig
		}
		n++
	}
	if err := zw.Close(); err != nil {
		return n, err
	}
	if cw.n > b.api.UploadLimit() {
		return n, errTooBig
	}
	return n, nil
}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}
