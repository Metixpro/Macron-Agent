package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"golang.org/x/sys/unix"
)

const settingDLCookies = "dl_cookies"

func ytdlpAge(version string, now time.Time) (time.Duration, bool) {
	if len(version) < 10 {
		return 0, false
	}
	t, err := time.Parse("2006.01.02", version[:10])
	if err != nil {
		return 0, false
	}
	return now.Sub(t), true
}

func yesNo(ok bool, yes, no string) string {
	if ok {
		return "✅ " + yes
	}
	return "❌ " + no
}

func (b *Bot) viewDownloader(l lang) panelView {
	d := b.app.Registry.DownloaderStatus()
	var sb strings.Builder
	sb.WriteString(l.t("📥 <b>دانلودر</b> (یوتیوب، اینستاگرام، تیک‌تاک، توییتر، ساندکلاد، آپارات و…)\n\n"))
	switch {
	case d.YtDlp == nil:
		sb.WriteString(l.t("❌ <b>yt-dlp نصب نیست</b>؛ دانلود از سایت‌ها کار نمی‌کنه. راهنمای نصب توی README هست.\n"))
	default:
		fmt.Fprintf(&sb, "🎬 yt-dlp: <code>%s</code>", esc(d.Version))
		if age, ok := ytdlpAge(d.Version, time.Now()); ok {
			days := int(age.Hours() / 24)
			fmt.Fprint(&sb, l.f(" (%s روز پیش)", l.num(days)))
			if days > 45 {
				sb.WriteString(l.t(" ⚠️ <b>قدیمیه؛ آپدیتش کن</b>"))
			}
		}
		sb.WriteString("\n")
	}
	js, _, _ := strings.Cut(d.JSRuntime, ":")
	fmt.Fprint(&sb, l.f("🧩 موتور جاوااسکریپت (برای یوتیوب): %s\n", yesNo(d.JSRuntime != "", js, l.t("نیست؛ یوتیوب ممکنه کار نکنه"))))
	fmt.Fprint(&sb, l.f("🔑 PO token یوتیوب: %s\n", yesNo(d.POTProvider, l.t("فعال (127.0.0.1:4416)"), l.t("نیست؛ روی خیلی از سرورها یوتیوب 403 می‌ده"))))
	fmt.Fprint(&sb, l.f("🖼 gallery-dl (پست‌های عکس): %s\n", yesNo(d.GalleryDL != "", l.t("هست"), l.t("نیست"))))
	fmt.Fprint(&sb, l.f("🎞 ffmpeg (MP3، ادغام، فشرده‌سازی): %s\n", yesNo(d.FFmpeg, l.t("هست"), l.t("نیست"))))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("⬆️ آپدیت yt-dlp"), "a:dl:update", telegram.StylePrimary), b.button(l.t("➕ افزودن کوکی"), "a:dl:cookies", telegram.StyleSuccess)),
		row(b.button(l.t("🔗 کوکی از لینک"), "a:dl:links", ""), b.button(l.t("🩺 تست کوکی‌ها"), "a:dl:test", "")),
	}

	list, _ := b.st.Cookies(context.Background())
	now := b.st.Now()
	ready := 0
	for _, c := range list {
		if !c.Resting(now) {
			ready++
		}
	}
	fmt.Fprint(&sb, l.f("\n🍪 <b>استخر کوکی‌ها</b>: %s کوکی، %s آماده", l.num(len(list)), l.num(ready)))
	if b.cookies != nil && b.cookies.dir != "" {
		total, resting := b.cookies.folderStatus()
		fmt.Fprint(&sb, l.f(" · پوشه <code>%s</code>: %s فایل (%s در استراحت)", esc(b.cookies.dir), l.num(total), l.num(resting)))
	}
	sb.WriteString("\n")
	for i, c := range list {
		if i == 12 {
			fmt.Fprint(&sb, l.f("… و %s تای دیگه\n", l.num(len(list)-12)))
			break
		}
		status := l.t("✅ آماده")
		switch site := cookieMainSite(c.Data, c.Domains); {
		case c.Resting(now):
			status = l.t("🧊 استراحت تا ") + c.RestUntil.In(b.loc).Format("15:04")
		case site != "" && tools.CookieExpired(tools.Cookie{Data: c.Data}, site, now):
			status = l.t("⌛ لاگینش تموم شده")
		}
		if c.SourceURL != "" {
			status += " · 🔗"
		}
		fmt.Fprint(&sb, l.f("<b>#%d</b> %s · %s · %s بار استفاده، %s بار بلاک\n", c.ID, esc(c.Label), status, l.num(c.Uses), l.num(c.Blocks)))
		rows = append(rows, row(b.button(l.f("🗑 حذف #%d %s", c.ID, textutil.TruncateRunes(c.Label, 20, "…")), fmt.Sprintf("a:dl:delck:%d", c.ID), telegram.StyleDanger)))
	}
	sb.WriteString(l.t("\nهر دانلود کوکیِ آماده‌ای که کمتر ازش استفاده شده رو برمی‌داره؛ اگه سایت بلاکش کنه (۴۲۹، «Sign in»، لاگین)، ") +
		l.t("۳۰ تا ۶۰ دقیقه استراحت می‌کنه و همون لحظه با کوکی بعدی دوباره امتحان می‌شه. بهتره از اکانت‌های فرعی باشن.") +
		l.t("\n🔗 کوکیِ لینک‌دار هر ۶ ساعت و هر بار که بلاک بشه یا لاگینش تموم بشه، دوباره از لینکش گرفته می‌شه. ") +
		l.t("کاربرها هم می‌تونن با /cookies کوکی خودشون رو بذارن (اول با همون دانلود می‌کنن)."))
	rows = append(rows, row(b.button(l.t("🔄 بررسی دوباره"), "a:dl", ""), b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

const maxCookieLinks = 10

func (b *Bot) addCookieLinks(ctx context.Context, l lang, text string) (string, bool) {
	var sb strings.Builder
	added, n := false, 0
	for _, line := range strings.Split(text, "\n") {
		link := strings.TrimSpace(line)
		if link == "" {
			continue
		}
		if n++; n > maxCookieLinks {
			sb.WriteString(l.f("… فقط %s لینک اول اضافه شد.\n", l.num(maxCookieLinks)))
			break
		}
		short := esc(textutil.TruncateRunes(link, 60, "…"))
		data, err := b.cookies.fetchCookies(ctx, link)
		if err != nil {
			fmt.Fprint(&sb, l.f("❌ %s: %s\n", short, esc(err.Error())))
			continue
		}
		domains := tools.CookieDomains(data)
		host := ""
		if u, err := url.Parse(link); err == nil {
			host = u.Hostname()
		}
		label := cookieLabel(cookieSites(data, domains), host)
		id, err := b.st.AddCookieFrom(ctx, label, domains, data, link)
		if err != nil {
			fmt.Fprint(&sb, l.f("❌ %s: %s\n", short, l.t(textError)))
			continue
		}
		added = true
		fmt.Fprint(&sb, l.f("✅ #%d %s ← %s · %s\n", id, esc(label), short, loginStatus(l, data, b.loc)))
	}
	if n == 0 {
		return l.t("هیچ لینکی نفرستادی؛ هر خط یه لینک."), false
	}
	return strings.TrimSpace(sb.String()), added
}

func (b *Bot) testCookies(ctx context.Context, l lang) string {
	list, err := b.st.Cookies(ctx)
	if err != nil {
		return l.t(textError)
	}
	if len(list) == 0 {
		return l.t("🍪 استخر کوکی خالیه.")
	}
	const shown = 12
	tested := list[:min(len(list), shown)]
	lines := make([]string, len(tested))
	sem := make(chan struct{}, 3)
	var wg sync.WaitGroup
	for i, c := range tested {
		wg.Add(1)
		go func() {
			defer guard.Recover("downloader_admin")
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			site := cookieMainSite(c.Data, c.Domains)
			res := b.app.Registry.TestCookie(ctx, c.Data, site)
			refreshed := ""
			if !res.OK && c.SourceURL != "" {
				if changed, err := b.cookies.refresh(ctx, c); err == nil && changed {
					if fresh, ok := b.cookies.byID(ctx, c.ID); ok {
						res, refreshed = b.app.Registry.TestCookie(ctx, fresh.Data, site), l.t(" (از لینکش تازه شد)")
					}
				}
			}
			mark := "✅"
			if !res.OK {
				mark = "❌"
			}
			lines[i] = fmt.Sprintf("%s <b>#%d</b> %s · %s: %s%s", mark, c.ID, esc(c.Label), esc(site), esc(textutil.TruncateRunes(res.Detail, 120, "…")), refreshed)
		}()
	}
	wg.Wait()
	var sb strings.Builder
	sb.WriteString(l.t("🩺 <b>تست کوکی‌ها</b>\n"))
	sb.WriteString(strings.Join(lines, "\n"))
	if len(list) > shown {
		fmt.Fprint(&sb, "\n"+l.f("… و %s تای دیگه تست نشد\n", l.num(len(list)-shown)))
	}
	return strings.TrimSpace(sb.String())
}

func (b *Bot) updateYtDlp(ctx context.Context, l lang) string {
	d := b.app.Registry.DownloaderStatus()
	run := func(args ...string) (string, error) {
		cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
		defer cancel()
		out, err := exec.CommandContext(cctx, args[0], args[1:]...).CombinedOutput()
		return string(out), err
	}
	var out string
	var err error
	if len(d.YtDlp) == 1 {
		out, err = run(d.YtDlp[0], "-U")
	}

	if low := strings.ToLower(out); len(d.YtDlp) != 1 || strings.Contains(low, "pip") || strings.Contains(low, "apt") ||
		strings.Contains(low, "package manager") {
		pip := []string{"python3", "-m", "pip", "install", "--upgrade", "yt-dlp[default]"}
		out, err = run(pip...)
		if strings.Contains(out, "externally-managed") {
			out, err = run(append(pip, "--break-system-packages")...)
		}
		if err != nil {
			if path, berr := b.installYtDlpBinary(ctx, ytDlpTarget(d)); berr == nil {
				out, err = "installed "+path, nil
			}
		}
	}
	after := b.app.Registry.DownloaderStatus()
	result := l.f("نسخه: %s → %s", d.Version, after.Version)
	if err != nil {
		result = l.t("❌ آپدیت نشد: ") + textutil.TruncateRunes(strings.TrimSpace(out), 300, "…")
	}
	return result
}

var (
	ytDlpBinary   = "/usr/local/bin/yt-dlp"
	ytDlpReleases = "https://github.com/yt-dlp/yt-dlp/releases/latest/download/"
)

func ytDlpTarget(d tools.DownloaderInfo) string {
	if len(d.YtDlp) == 1 && unix.Access(filepath.Dir(d.YtDlp[0]), unix.W_OK) == nil {
		return d.YtDlp[0]
	}
	return ytDlpBinary
}

func (b *Bot) installYtDlpBinary(ctx context.Context, target string) (string, error) {
	name := "yt-dlp"
	switch runtime.GOARCH {
	case "amd64":
		name = "yt-dlp_linux"
	case "arm64":
		name = "yt-dlp_linux_aarch64"
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ytDlpReleases+name, nil)
	if err != nil {
		return "", err
	}
	resp, err := b.app.Registry.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), ".yt-dlp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, io.LimitReader(resp.Body, 200<<20))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp.Name(), 0o755)
	}
	if err == nil {
		err = exec.CommandContext(ctx, tmp.Name(), "--version").Run()
	}
	if err == nil {
		err = os.Rename(tmp.Name(), target)
	}
	return target, err
}

func looksLikeCookies(s string) bool {
	if strings.Contains(s, "# Netscape HTTP Cookie File") || strings.Contains(s, "# HTTP Cookie File") {
		return true
	}
	for _, line := range strings.Split(s, "\n") {
		if len(strings.Split(strings.TrimSpace(line), "\t")) == 7 {
			return true
		}
	}
	return false
}
