package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

// fakeGitHub serves one release whose program says its version.
func fakeGitHub(t *testing.T, version string, tamper bool) *httptest.Server {
	t.Helper()
	script := "#!/bin/sh\n[ \"$1\" = version ] && echo 'macron " + version + "'\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "macron-" + version + "/macron", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	name := updater.AssetName(version)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if tamper {
		sums = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/"):
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "body": "- faster agent\n- new gate",
				"assets": []map[string]any{{"name": name, "browser_download_url": srv.URL + "/dl/" + name}, {"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/SHA256SUMS"}}})
		case strings.HasSuffix(r.URL.Path, "/dl/"+name):
			_, _ = w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "/dl/SHA256SUMS"):
			_, _ = w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// withRelease points the updater at a fake release and a program of its own.
func withRelease(t *testing.T, srv *httptest.Server) (self string, restarted *atomic.Int32) {
	t.Helper()
	t.Setenv("MACRON_HOME", t.TempDir())
	self = filepath.Join(t.TempDir(), "macron")
	_ = os.WriteFile(self, []byte("#!/bin/sh\necho old\n"), 0o755)
	oldAPI, oldSelf, oldRestart := updater.ReleaseAPI, updater.SelfPath, restartLater
	updater.ReleaseAPI = srv.URL + "/releases"
	updater.SelfPath = func() (string, error) { return self, nil }
	restarted = &atomic.Int32{}
	restartLater = func(string, bool) (string, error) { restarted.Add(1); return "test", nil }
	t.Cleanup(func() { updater.ReleaseAPI, updater.SelfPath, restartLater = oldAPI, oldSelf, oldRestart })
	return self, restarted
}

func TestUpdateFromTheChat(t *testing.T) {
	h := newHarness(t)
	self, restarted := withRelease(t, fakeGitHub(t, "99.0.0", false))
	h.send(private(ownerID, "/update"))
	h.wantText("نسخه جدید: <b>v99.0.0</b>")
	h.wantText("faster agent")
	if !hasButton(buttons(lastOf(h, "sendMessage")), "→ up:go:v99.0.0") {
		t.Fatal("no [+] update button")
	}
	h.click(ownerID, ownerID, 9, "up:go:v99.0.0")
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "macron 99.0.0") {
		t.Fatalf("not installed: %s\n%s", b, h.tg.allText())
	}
	if b, _ := os.ReadFile(self + ".old"); !strings.Contains(string(b), "echo old") {
		t.Error("the old program is kept")
	}
	if restarted.Load() != 1 {
		t.Fatal("the service is restarted from outside")
	}
	raw, _ := h.st.Setting(settingUpdatePending)
	if !strings.Contains(raw, `"version":"99.0.0"`) {
		t.Fatalf("pending: %s", raw)
	}
	// The new process says so.
	_ = h.st.SetSetting(bg, settingUpdatePending, `{"chat":`+itoaTest(ownerID)+`,"version":"`+app.Version+`"}`)
	h.bot.reportUpdate(bg)
	h.wantText("[+] آپدیت شد به <b>v" + app.Version + "</b> · سرویس فعال · uptime")
	if _, ok := h.st.Setting(settingUpdatePending); ok {
		t.Error("said once")
	}
}

func TestUpdateUpToDate(t *testing.T) {
	h := newHarness(t)
	withRelease(t, fakeGitHub(t, app.Version, false))
	h.send(private(ownerID, "/update"))
	h.wantText("تو آخرین نسخه‌ای")
	h.click(ownerID, ownerID, 9, "a:upd")
	h.wantText("تو آخرین نسخه‌ای")
}

// A download that does not match its sum, or no network: nothing changes.
func TestUpdateFailsSafely(t *testing.T) {
	h := newHarness(t)
	self, restarted := withRelease(t, fakeGitHub(t, "99.0.0", true))
	h.click(ownerID, ownerID, 9, "up:go:v99.0.0")
	h.wantText("[-] آپدیت نشد")
	h.wantText("SHA-256")
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "echo old") || restarted.Load() != 0 {
		t.Fatal("the installed program is untouched")
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	dead.Close()
	updater.ReleaseAPI = dead.URL + "/releases"
	h.tg.reset()
	h.click(ownerID, ownerID, 9, "up:go:v99.0.0")
	h.wantText("[-] آپدیت نشد")
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "echo old") {
		t.Fatal("untouched without a network")
	}
}

// The daily notice: once per version.
func TestUpdateNoticeOncePerVersion(t *testing.T) {
	h := newHarness(t)
	withRelease(t, fakeGitHub(t, "99.0.0", false))
	h.bot.checkUpdateNotice(bg)
	h.bot.checkUpdateNotice(bg) // within the day: not looked up again
	_ = h.st.SetSetting(bg, settingUpdateChecked, time.Now().Add(-48*time.Hour).UTC().Format(time.RFC3339))
	h.bot.checkUpdateNotice(bg) // a day later, the same version: not told again
	n := strings.Count(h.tg.allText(), "نسخه <b>v99.0.0</b> موجوده")
	if n != 1 {
		t.Fatalf("told %d times", n)
	}
	_ = h.st.SetSetting(bg, settingUpdateNotice, "false")
	_ = h.st.DeleteSetting(bg, settingUpdateNotified)
	_ = h.st.DeleteSetting(bg, settingUpdateChecked)
	h.bot.checkUpdateNotice(bg)
	if strings.Count(h.tg.allText(), "موجوده") != 1 {
		t.Fatal("update_notice off")
	}
}

func itoaTest(n int64) string { b, _ := json.Marshal(n); return string(b) }
