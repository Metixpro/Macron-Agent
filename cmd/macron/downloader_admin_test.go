package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func TestDownloaderHelpers(t *testing.T) {
	for in, want := range map[string][2]string{
		"Hamim - Khorshido Mah.mp3":       {"Khorshido Mah", "Hamim"},
		"Hamim - Rose (compressed).mp3":   {"Rose", "Hamim"},
		"voice_12.ogg":                    {"voice_12", "Macron"},
		"Macron_AI_-_جنگ_زرگرها_2.mp3":    {"جنگ زرگرها 2", "Macron AI"},
		"Ebi - Kish - live in Tehran.mp3": {"Kish - live in Tehran", "Ebi"},
	} {
		if title, performer := audioNames(in); title != want[0] || performer != want[1] {
			t.Errorf("audioNames(%q) = %q, %q", in, title, performer)
		}
	}
	now := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	if age, ok := ytdlpAge("2026.08.19", now); !ok || int(age.Hours()/24) != 61 {
		t.Errorf("age: %v %v", age, ok)
	}
	if _, ok := ytdlpAge("2026.08.19.232050", now); !ok {
		t.Error("nightly versions")
	}
	if _, ok := ytdlpAge("unknown", now); ok {
		t.Error("garbage")
	}
	if !looksLikeCookies("# Netscape HTTP Cookie File\n") || !looksLikeCookies(".x.com\tTRUE\t/\tTRUE\t0\tSID\tabc") || looksLikeCookies("hello") {
		t.Error("cookies format")
	}
}

func TestCookiePoolInThePanel(t *testing.T) {
	h := newHarness(t)
	h.bot.cookies = newCookiePool(h.st, "")
	h.click(ownerID, ownerID, 900, "a:dl")
	v := lastOf(h, "editMessageText")
	if !strings.Contains(v.body["text"].(string), "yt-dlp") || !hasButton(buttons(v), "a:dl:update") || !hasButton(buttons(v), "a:dl:cookies") {
		t.Fatalf("panel: %v", v.body["text"])
	}
	add := func(id int64, caption, data string) {
		h.click(ownerID, ownerID, 900, "a:dl:cookies")
		m := private(ownerID, data)
		m.MessageID, m.Caption = id, caption
		h.send(m)
	}
	add(4242, "", "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tsecret-one")
	add(4243, "insta acc", ".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tsecret-two")
	add(4244, "", ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tsecret-three")
	list, _ := h.st.Cookies(context.Background())
	if len(list) != 3 || list[1].Label != "insta acc" || strings.Join(list[0].Domains, ",") != "youtube.com" {
		t.Fatalf("pool: %+v", list)
	}
	var raw string
	_ = h.st.DB().QueryRow("SELECT data FROM dl_cookies WHERE id = ?", list[0].ID).Scan(&raw)
	if strings.Contains(raw, "secret-one") {
		t.Error("cookies are stored encrypted")
	}
	for _, id := range []float64{4242, 4243, 4244} {
		deleted := false
		for _, c := range h.tg.of("deleteMessage") {
			deleted = deleted || c.body["message_id"].(float64) == id
		}
		if !deleted {
			t.Errorf("message %v with cookies must be deleted", id)
		}
	}

	p := h.bot.cookies
	a, _ := p.Pick("youtube.com", nil)
	b, _ := p.Pick("www.youtube.com", nil)
	if a.ID == b.ID || !strings.Contains(a.Data+b.Data, "secret-one") || !strings.Contains(a.Data+b.Data, "secret-three") {
		t.Errorf("rotation: %s %s", a.ID, b.ID)
	}
	if c, ok := p.Pick("instagram.com", nil); !ok || !strings.Contains(c.Data, "secret-two") {
		t.Error("cookies are matched by site")
	}
	if _, ok := p.Pick("tiktok.com", nil); ok {
		t.Error("no cookie for other sites")
	}
	p.Rest(a.ID, 45*time.Minute, "HTTP Error 429")
	for i := 0; i < 3; i++ {
		if c, _ := p.Pick("youtube.com", nil); c.ID == a.ID {
			t.Fatal("a resting cookie is not used")
		}
	}
	h.tg.reset()
	h.click(ownerID, ownerID, 900, "a:dl")
	if text := lastOf(h, "editMessageText").body["text"].(string); !strings.Contains(text, "🧊 استراحت تا") || !strings.Contains(text, "۲ آماده") {
		t.Errorf("panel shows the rest: %s", text)
	}
	h.click(ownerID, ownerID, 900, "a:dl:delck:"+strings.TrimPrefix(a.ID, cookieDBPrefix))
	if list, _ := h.st.Cookies(context.Background()); len(list) != 2 {
		t.Error("delete")
	}
}

func TestOldSingleCookieMovesIntoThePool(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	_ = h.st.SetSecretSetting(ctx, settingDLCookies, ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\told")
	migrateSingleCookie(ctx, h.st)
	list, _ := h.st.Cookies(ctx)
	if len(list) != 1 || !strings.Contains(list[0].Data, "old") {
		t.Fatalf("migrated: %+v", list)
	}
	if v, _ := h.st.SecretSetting(settingDLCookies); v != "" {
		t.Error("the old setting is cleared")
	}
}

func TestCookieFolder(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "acc1.txt"), []byte(".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tf1"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "acc2.txt"), []byte(".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tf2"), 0o600)
	p := newCookiePool(h.st, dir)
	a, ok := p.Pick("instagram.com", nil)
	if !ok || !strings.HasPrefix(a.ID, cookieDirPrefix) {
		t.Fatalf("folder cookie: %+v", a)
	}
	p.Rest(a.ID, time.Hour, "429")
	b, ok := p.Pick("instagram.com", nil)
	if !ok || b.ID == a.ID {
		t.Error("the other file is used while the first rests")
	}
	if total, resting := p.folderStatus(); total != 2 || resting != 1 {
		t.Errorf("status %d %d", total, resting)
	}
}

func TestStandaloneYtDlp(t *testing.T) {
	var broken atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/yt-dlp") {
			http.NotFound(w, r)
			return
		}
		if broken.Load() {
			fmt.Fprint(w, "#!/bin/sh\nexit 1\n")
			return
		}
		fmt.Fprint(w, "#!/bin/sh\necho 2026.10.01\n")
	}))
	defer srv.Close()
	oldBin, oldRel := ytDlpBinary, ytDlpReleases
	ytDlpBinary, ytDlpReleases = filepath.Join(t.TempDir(), "yt-dlp"), srv.URL+"/"
	defer func() { ytDlpBinary, ytDlpReleases = oldBin, oldRel }()
	b := &Bot{app: &app.App{Registry: &tools.Registry{HTTP: srv.Client()}}}

	if got := ytDlpTarget(tools.DownloaderInfo{YtDlp: []string{"/nonexistent/yt-dlp"}}); got != ytDlpBinary {
		t.Errorf("a folder that cannot be used: %s", got)
	}
	if got := ytDlpTarget(tools.DownloaderInfo{YtDlp: []string{ytDlpBinary}}); got != ytDlpBinary {
		t.Errorf("a writable folder: %s", got)
	}
	path, err := b.installYtDlpBinary(context.Background(), ytDlpBinary)
	if out, _ := exec.Command(path).Output(); err != nil || path != ytDlpBinary || strings.TrimSpace(string(out)) != "2026.10.01" {
		t.Fatalf("install: %v %q", err, out)
	}
	broken.Store(true)
	if _, err := b.installYtDlpBinary(context.Background(), ytDlpBinary); err == nil {
		t.Fatal("a build that does not run is not installed")
	}
	if out, _ := exec.Command(ytDlpBinary).Output(); strings.TrimSpace(string(out)) != "2026.10.01" {
		t.Error("the working copy stays")
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(ytDlpBinary), ".yt-dlp-*")); len(left) > 0 {
		t.Errorf("temporary files left: %v", left)
	}
}
