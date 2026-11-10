package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/tools"
)

func ytCookie(value string, expires time.Time) string {
	return fmt.Sprintf("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\t%s\n", expires.Unix(), value)
}

func TestUserCookies(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	const user = 2024
	uid := strconv.Itoa(user)
	later := time.Now().Add(60 * 24 * time.Hour)

	h.send(private(user, "/cookies"))
	v := lastOf(h, "sendMessage")
	if !strings.Contains(v.body["text"].(string), "کوکی‌های من") || !hasButton(buttons(v), "ck:a:"+uid) {
		t.Fatalf("panel: %v", v.body["text"])
	}
	add := func(id int64, data string) string {
		h.tg.reset()
		h.click(user, user, 900, "ck:a:"+uid)
		m := private(user, data)
		m.MessageID = id
		h.send(m)
		if sent := h.tg.of("sendMessage"); len(sent) > 0 {
			t.Errorf("every step edits the panel; sent: %v", sent[0].body["text"])
		}
		return h.tg.allText()
	}
	if out := add(501, ytCookie("secret-one", later)); !strings.Contains(out, "ذخیره شد") || !strings.Contains(out, "🩺 youtube.com") {
		t.Fatalf("add: %s", out)
	}
	deleted := false
	for _, c := range h.tg.of("deleteMessage") {
		deleted = deleted || c.body["message_id"].(float64) == 501
	}
	if !deleted {
		t.Error("the message with the cookies is deleted")
	}
	list, _ := h.st.UserCookies(ctx, user)
	if len(list) != 1 || !strings.Contains(list[0].Data, "secret-one") {
		t.Fatalf("saved: %+v", list)
	}
	var raw string
	_ = h.st.DB().QueryRow("SELECT data FROM user_cookies WHERE id = ?", list[0].ID).Scan(&raw)
	if strings.Contains(raw, "secret-one") {
		t.Error("cookies are stored encrypted")
	}

	add(502, ytCookie("secret-two", later))
	if list, _ = h.st.UserCookies(ctx, user); len(list) != 1 || !strings.Contains(list[0].Data, "secret-two") {
		t.Fatalf("replaced: %+v", list)
	}
	for data, want := range map[string]string{
		ytCookie("old", time.Now().Add(-time.Hour)):    "تموم شده",
		".youtube.com\tTRUE\t/\tTRUE\t0\tPREF\tf6=1\n": "لاگینی نیست",
		"hello there": "فرمت cookies.txt",
		".instagram.com\tTRUE\t/\tTRUE\t0\tsessionid\tinsta-secret\n": "ذخیره شد",
	} {
		if out := add(503, data); !strings.Contains(out, want) {
			t.Errorf("%q: want %q in %s", data, want, out)
		}
	}
	if list, _ = h.st.UserCookies(ctx, user); len(list) != 2 {
		t.Fatalf("youtube and instagram: %+v", list)
	}

	if got := h.bot.cookies.user(sessionFor(user)); len(got) != 2 || !strings.HasPrefix(got[0].ID, tools.UserCookiePrefix) {
		t.Errorf("user cookies: %+v", got)
	}
	if got := h.bot.cookies.user(sessionFor(user + 1)); len(got) != 0 {
		t.Error("another user's downloads never get them")
	}

	h.send(private(user, ytCookie("not-asked", later)))
	if list, _ = h.st.UserCookies(ctx, user); len(list) != 2 {
		t.Error("cookies are saved only when asked for")
	}

	h.tg.reset()
	h.click(user+1, user+1, 900, fmt.Sprintf("ck:d:%s:%d", uid, list[0].ID))
	if list, _ = h.st.UserCookies(ctx, user); len(list) != 2 {
		t.Error("a foreign press deletes nothing")
	}
	h.click(user, user, 900, fmt.Sprintf("ck:d:%s:%d", uid, list[0].ID))
	if list, _ = h.st.UserCookies(ctx, user); len(list) != 1 {
		t.Error("delete")
	}
	h.tg.reset()
	h.bot.openCookiesPanel(ctx, group(user, "/cookies"))
	h.wantText("@MacronBot")
}

func TestCookiesFromLinks(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	var version atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/cookies.txt":
			fmt.Fprintf(w, "# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tSID\tlinked-v%d\n", version.Load())
		case "/page":
			fmt.Fprint(w, "<html>not cookies</html>")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	h.bot.cookies.http = srv.Client()

	h.click(ownerID, ownerID, 900, "a:dl:links")
	h.tg.reset()
	h.send(private(ownerID, srv.URL+"/cookies.txt\n"+srv.URL+"/page\n"+srv.URL+"/missing\nftp://x"))
	out := h.tg.texts()[0]
	if strings.Count(out, "✅ #") != 1 || strings.Count(out, "❌") != 3 || !strings.Contains(out, "HTTP 404") {
		t.Fatalf("links: %s", out)
	}
	list, _ := h.st.Cookies(ctx)
	if len(list) != 1 || list[0].SourceURL != srv.URL+"/cookies.txt" || list[0].FetchedAt == nil || !strings.Contains(list[0].Data, "linked-v0") {
		t.Fatalf("pool: %+v", list)
	}
	h.click(ownerID, ownerID, 900, "a:dl")
	if text := lastOf(h, "editMessageText").body["text"].(string); !strings.Contains(text, "🔗") {
		t.Errorf("a linked cookie is marked: %s", text)
	}

	version.Store(1)
	_, _ = h.st.DB().Exec("UPDATE dl_cookies SET fetched_at = ? WHERE id = ?", time.Now().UTC().Add(-time.Hour), list[0].ID)
	p := h.bot.cookies
	c, ok := p.Pick("youtube.com", nil)
	if !ok {
		t.Fatal("pick")
	}
	p.Rest(c.ID, time.Hour, "HTTP Error 429")
	deadline := time.Now().Add(5 * time.Second)
	for {
		list, _ = h.st.Cookies(ctx)
		if strings.Contains(list[0].Data, "linked-v1") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not fetched again: %+v", list[0])
		}
		time.Sleep(20 * time.Millisecond)
	}
	if list[0].Resting(h.st.Now()) {
		t.Error("fresh cookies do not rest")
	}

	if changed, err := p.refresh(ctx, list[0]); err != nil || changed {
		t.Errorf("unchanged: %v %v", changed, err)
	}

	h.tg.reset()
	h.click(ownerID, ownerID, 900, "a:dl:test")
	h.wantText("تست کوکی‌ها")
	h.wantText("youtube.com")
}
