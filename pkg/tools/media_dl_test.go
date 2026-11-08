package tools

import (
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fakeDownloader(t *testing.T, r *Registry) {
	t.Helper()
	// Host fixture paths must not contain the virtual /workspace prefix,
	// which Machine intentionally translates to the conversation directory.
	dir := t.TempDir()
	for _, name := range []string{"fake-yt-dlp", "fake-gallery-dl"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	r.dl.info = &DownloaderInfo{YtDlp: []string{filepath.Join(dir, "fake-yt-dlp")}, GalleryDL: filepath.Join(dir, "fake-gallery-dl"),
		FFmpeg: true, JSRuntime: "node:/usr/bin/node", flags: map[string]bool{"--js-runtimes": true}}
}

func args(t *testing.T, r *Registry, session string, attempt int) string {
	t.Helper()
	data, _, err := r.Machine.ReadFile(session, ".args_"+string(rune('0'+attempt)), 1<<20)
	if err != nil {
		t.Fatalf("attempt %d did not run", attempt)
	}
	return string(data)
}

func TestDownloadMediaPicksFormatsAndReportsTheFile(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	out := call(t, r, "a", ToolDownloadMedia, map[string]any{"url": "ytsearch1:hamim rose"})
	if !strings.Contains(out, `Downloaded "Test Song.mp3"`) || !strings.Contains(out, "«Test Song» · by Singer · 3:35") {
		t.Fatalf("audio: %s", out)
	}
	a := args(t, r, "a", 1)
	for _, want := range []string{"-x\n--audio-format\nmp3", "--embed-thumbnail", "--embed-metadata", "--js-runtimes\nnode:/usr/bin/node",
		"--no-playlist", "--\nytsearch1:hamim rose"} {
		if !strings.Contains(a, want) {
			t.Errorf("audio args miss %q:\n%s", want, a)
		}
	}
	if strings.Contains(a, "--remote-components") {
		t.Error("flags this yt-dlp does not know are never passed")
	}

	_ = r.Machine.Remove("a", ".attempts")
	out = call(t, r, "a", ToolDownloadMedia, map[string]any{"url": "ytsearch1:clip", "format": "video", "quality": "medium", "section": "1:30-2:45", "items": "1-3"})
	if !strings.Contains(out, `Downloaded "Test Video.mp4"`) || !strings.Contains(out, "1:35 · 720p") {
		t.Fatalf("video: %s", out)
	}
	a = args(t, r, "a", 1)
	for _, want := range []string{"res:480,vcodec:h264", "--merge-output-format\nmp4", "--download-sections\n*1:30-2:45", "--yes-playlist\n--playlist-items\n1-3"} {
		if !strings.Contains(a, want) {
			t.Errorf("video args miss %q:\n%s", want, a)
		}
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"url": "ytsearch1:x", "section": "soon"}, "section must look like"},
		{map[string]any{"url": "ytsearch1:x", "items": "1-50"}, "at most 10"},
		{map[string]any{"url": "--exec rm"}, "error"},
		{map[string]any{"url": "file:///etc/passwd"}, "error"},
	} {
		if out := call(t, r, "a", ToolDownloadMedia, c.args); !strings.Contains(out, c.want) {
			t.Errorf("%v: %s", c.args, out)
		}
	}
}

func TestDownloadMediaRetriesWhenTheSitePushesBack(t *testing.T) {
	// Without cookies: the ladder of other clients.
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	if out := call(t, r, "b", ToolDownloadMedia, map[string]any{"url": "ytsearch1:botcheck"}); !strings.Contains(out, "Test Song.mp3") {
		t.Fatalf("third attempt wins: %s", out)
	}
	if a := args(t, r, "b", 2); !strings.Contains(a, "youtube:player_client=default,tv_simple") || strings.Contains(a, "--cookies") {
		t.Errorf("second attempt: %s", a)
	}
	if a := args(t, r, "b", 3); !strings.Contains(a, "android_vr") || !strings.Contains(a, "--force-ipv4") {
		t.Errorf("third attempt: %s", a)
	}

	// With a pool: a blocked account rests and the next one is used at once.
	rp := newRegistry(t, true, nil)
	fakeDownloader(t, rp)
	pool := NewMemoryCookiePool(
		Cookie{ID: "a", Label: "first", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tone\n"},
		Cookie{ID: "b", Label: "second", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\ttwo\n"},
		Cookie{ID: "c", Label: "insta", Data: ".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tx\n"})
	rp.SetCookiePool(pool)
	out := call(t, rp, "p", ToolDownloadMedia, map[string]any{"url": "ytsearch1:botcheck"})
	if !strings.Contains(out, "Test Song.mp3") || strings.Count(out, "was blocked and rests for") != 2 {
		t.Fatalf("rotation: %s", out)
	}
	a1 := args(t, rp, "p", 1)
	if !strings.Contains(a1, "--cookies\n") || !strings.Contains(a1, "/"+cookiesName) || strings.Contains(a1, "./"+dlCookiesFile) {
		t.Errorf("first try uses a cookie, from outside the workspace: %s", a1)
	}
	ws := must(rp.Machine.Workspace("p"))
	for _, line := range strings.Split(a1, "\n") {
		if strings.HasSuffix(line, "/"+cookiesName) {
			if strings.HasPrefix(line, ws) || strings.HasPrefix(line, filepath.Dir(ws)) {
				t.Errorf("the cookie file is outside every workspace: %s", line)
			}
			if _, err := os.Stat(line); err == nil {
				t.Errorf("the cookie file is gone after the download: %s", line)
			}
		}
	}
	if a := args(t, rp, "p", 2); !strings.Contains(a, "--cookies") || strings.Contains(a, "player_client") {
		t.Errorf("the next account is tried at once, same attempt: %s", a)
	}
	if a := args(t, rp, "p", 3); strings.Contains(a, "--cookies") || !strings.Contains(a, "tv_simple") {
		t.Errorf("no account left: the ladder goes on without cookies: %s", a)
	}
	if pool.RestingUntil("a").IsZero() || pool.RestingUntil("b").IsZero() || !pool.RestingUntil("c").IsZero() {
		t.Error("both YouTube accounts rest; the Instagram one is untouched")
	}
	if until := time.Until(pool.RestingUntil("a")); until < 29*time.Minute || until > 61*time.Minute {
		t.Errorf("rest: %v", until)
	}
	if _, err := rp.Machine.Stat("p", dlCookiesFile); err == nil {
		t.Error("the cookies never stay in the workspace")
	}

	// A private video is not retried, and the model learns why.
	r2 := newRegistry(t, true, nil)
	fakeDownloader(t, r2)
	out = call(t, r2, "c", ToolDownloadMedia, map[string]any{"url": "ytsearch1:private"})
	if !strings.Contains(out, "private, removed or unavailable") {
		t.Errorf("private: %s", out)
	}
	if _, err := r2.Machine.Stat("c", ".args_2"); err == nil {
		t.Error("a private video is tried once")
	}
	// Without cookies, a login wall tells the model the admin can add them.
	r3 := newRegistry(t, true, nil)
	fakeDownloader(t, r3)
	_ = r3.Machine.WriteFile("d", ".attempts", []byte("-5")) // keep failing
	if out := call(t, r3, "d", ToolDownloadMedia, map[string]any{"url": "ytsearch1:botcheck"}); !strings.Contains(out, "admin can add cookies") {
		t.Errorf("login wall: %s", out)
	}
}

func TestDownloadMediaFallsBackToGalleryDL(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	out := call(t, r, "e", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/p/photos"})
	if !strings.Contains(out, `Downloaded "post_1.jpg"`) || !strings.Contains(out, `Downloaded "post_2.jpg"`) {
		t.Fatalf("gallery-dl: %s", out)
	}
}

func TestDownloadMediaFitsTelegram(t *testing.T) {
	if exec.Command("ffmpeg", "-version").Run() != nil {
		t.Skip("ffmpeg is needed")
	}
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ws, _ := r.Machine.Workspace("f")
	src := filepath.Join(ws, "big_src.mp4")
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=1280x720:rate=30", "-f", "lavfi", "-i",
		"sine=frequency=440", "-t", "6", "-c:v", "libx264", "-b:v", "4M", "-c:a", "aac", src).CombinedOutput(); err != nil {
		t.Skipf("cannot make a test video: %v %s", err, out)
	}
	info, _ := os.Stat(src)
	oldSend, oldFit := dlSendLimit, dlFitBytes
	dlSendLimit, dlFitBytes = info.Size()*3/5, info.Size()/2
	defer func() { dlSendLimit, dlFitBytes = oldSend, oldFit }()

	out := call(t, r, "f", ToolDownloadMedia, map[string]any{"url": "ytsearch1:big", "format": "video"})
	if !strings.Contains(out, `Downloaded "Big Clip (compressed).mp4"`) || !strings.Contains(out, "re-encoded from") {
		t.Fatalf("compress: %s", out)
	}
	fi, err := r.Machine.Stat("f", "Big Clip (compressed).mp4")
	if err != nil || fi.Size() > dlSendLimit {
		t.Fatalf("the result fits: %v %v", fi, err)
	}
	if _, err := r.Machine.Stat("f", "Big Clip.mp4"); err == nil {
		t.Error("the oversized original is removed, so only the result is sent")
	}
}

func TestYouTubeSongFallsBackToSoundCloud(t *testing.T) {
	if _, err := net.LookupHost("www.youtube.com"); err != nil {
		t.Skip("no DNS")
	}
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	out := call(t, r, "g", ToolDownloadMedia, map[string]any{"url": "https://www.youtube.com/watch?v=blocked", "format": "audio"})
	if !strings.Contains(out, `Downloaded "Test Song.mp3"`) || !strings.Contains(out, "found by its title on SoundCloud (Hamim Khorshido Mah)") {
		t.Fatalf("fallback: %s", out)
	}
	if q, _, _ := r.Machine.ReadFile("g", ".scquery", 100); strings.TrimSpace(string(q)) != "scsearch1:Hamim Khorshido Mah" {
		t.Errorf("query: %q", q)
	}
	// A video cannot fall back: the model is told why and what helps.
	out = call(t, r, "g", ToolDownloadMedia, map[string]any{"url": "https://www.youtube.com/watch?v=blocked", "format": "video"})
	if !strings.Contains(out, "YouTube refused to send the media") || !strings.Contains(out, "scsearch1:") {
		t.Errorf("video: %s", out)
	}
}

// A cookie rests only for what is its own fault: a video kept from the
// server's country is tried once and rests nothing; an age gate or a 403
// tries another account without resting this one (a 403, once).
func TestCookiesRestOnlyForTheirOwnFault(t *testing.T) {
	pool := func() *MemoryCookiePool {
		return NewMemoryCookiePool(
			Cookie{ID: "a", Label: "first", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tone\n"},
			Cookie{ID: "b", Label: "second", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\ttwo\n"})
	}
	run := func(target string) (*Registry, *MemoryCookiePool, string) {
		r := newRegistry(t, true, nil)
		fakeDownloader(t, r)
		p := pool()
		r.SetCookiePool(p)
		return r, p, call(t, r, "s", ToolDownloadMedia, map[string]any{"url": "ytsearch1:" + target})
	}
	r, p, out := run("geoblock")
	if !strings.Contains(out, "not available in the server's country") || !p.RestingUntil("a").IsZero() || !p.RestingUntil("b").IsZero() {
		t.Fatalf("geo-block: %s", out)
	}
	if _, err := r.Machine.Stat("s", ".args_2"); err == nil {
		t.Error("a geo-blocked video is tried once")
	}
	r, p, _ = run("agegate")
	if !p.RestingUntil("a").IsZero() || !p.RestingUntil("b").IsZero() {
		t.Error("an age gate rests no cookie")
	}
	if !strings.Contains(args(t, r, "s", 2), "--cookies") {
		t.Error("an age gate tries the next account")
	}
	r, p, _ = run("forbid")
	if !p.RestingUntil("a").IsZero() || !p.RestingUntil("b").IsZero() {
		t.Error("a 403 rests no cookie")
	}
	if a2, a3 := args(t, r, "s", 2), args(t, r, "s", 3); !strings.Contains(a2, "--cookies") || strings.Contains(a2, "player_client") ||
		!strings.Contains(a3, "player_client") {
		t.Errorf("a 403 tries one other account at once, then the ladder goes on: %s", a3)
	}
	for _, c := range []struct {
		err                     string
		blocked, private, retry bool
	}{
		{"Sign in to confirm you're not a bot", true, false, true},
		{"Sign in to confirm your age", false, true, true},
		{"The uploader has not made this video available in your country", false, false, false},
		{"This video is not available from your location due to geo restriction", false, false, false},
		{"Private video", false, true, false},
	} {
		if cookieBlocked(c.err) != c.blocked || privateForAccount(c.err) != c.private || retryable(c.err) != c.retry {
			t.Errorf("%q: blocked %v private %v retry %v", c.err, cookieBlocked(c.err), privateForAccount(c.err), retryable(c.err))
		}
	}
}

// yt-dlp's errors reach the model as advice, without its bug-report request.
func TestExplainDownloadError(t *testing.T) {
	e := "ERROR: [youtube] x: Unable to download API page: (caused by SSLError('[SSL: UNEXPECTED_EOF_WHILE_READING] EOF occurred')); please report this issue on  https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using  yt-dlp -U"
	got := explainDownloadError(e, false)
	if !strings.Contains(got, "could not reach the site") || strings.Contains(got, "report this issue") || strings.Contains(got, "yt-dlp -U") {
		t.Errorf("a network error: %s", got)
	}
	if got := explainDownloadError("ERROR: [youtube] x: Video unavailable", false); !strings.Contains(got, "private, removed or unavailable") {
		t.Errorf("an unavailable video: %s", got)
	}
}
