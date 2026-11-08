package tools

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func cookieLine(domain, name string, expires time.Time) string {
	exp := "0"
	if !expires.IsZero() {
		exp = strconv.FormatInt(expires.Unix(), 10)
	}
	return domain + "\tTRUE\t/\tTRUE\t" + exp + "\t" + name + "\tv\n"
}

func TestCookieLoginsAndExpiry(t *testing.T) {
	now := time.Now()
	data := "# Netscape HTTP Cookie File\n" +
		cookieLine(".youtube.com", "LOGIN_INFO", now.Add(90*24*time.Hour)) +
		cookieLine(".youtube.com", "PREF", now.Add(-time.Hour)) +
		"#HttpOnly_" + cookieLine(".instagram.com", "sessionid", now.Add(-time.Hour)) +
		cookieLine(".x.com", "auth_token", time.Time{}) +
		cookieLine(".reddit.com", "csv", now.Add(time.Hour))
	logins := CookieLogins(data, now)
	if len(logins) != 3 {
		t.Fatalf("logins: %+v", logins)
	}
	if l := logins[0]; l.Site != "instagram.com" || !l.Expired {
		t.Errorf("instagram: %+v", l)
	}
	if l := logins[1]; l.Site != "x.com" || l.Expired || !l.Expires.IsZero() {
		t.Errorf("a session cookie never expires: %+v", l)
	}
	if l := logins[2]; l.Site != "youtube.com" || l.Expired || l.Expires.Before(now.Add(89*24*time.Hour)) {
		t.Errorf("youtube: %+v", l)
	}
	c := Cookie{Data: data}
	if !CookieExpired(c, "www.instagram.com", now) || CookieExpired(c, "m.youtube.com", now) || CookieExpired(c, "reddit.com", now) {
		t.Error("only the site whose login ran out is expired; a site without a known login is not judged")
	}

	pool := NewMemoryCookiePool(Cookie{ID: "old", Data: cookieLine(".instagram.com", "sessionid", now.Add(-time.Hour))},
		Cookie{ID: "new", Data: cookieLine(".instagram.com", "sessionid", now.Add(time.Hour))})
	if got, ok := pool.Pick("instagram.com", nil); !ok || got.ID != "new" {
		t.Errorf("an expired login is never picked: %+v", got)
	}
}

func TestRotationLimiter(t *testing.T) {
	var l rotationLimiter
	now := time.Now()
	for i := 0; i < cookieRotationsPerHour; i++ {
		if !l.allow("a", now) {
			t.Fatalf("rotation %d refused", i)
		}
	}
	if l.allow("a", now) {
		t.Error("one user goes through at most cookieRotationsPerHour cookies an hour")
	}
	if !l.allow("b", now) || !l.allow("a", now.Add(61*time.Minute)) {
		t.Error("other users, and the next hour, are not limited")
	}
}

func TestUserCookiesComeFirst(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	pool := NewMemoryCookiePool(
		Cookie{ID: "a", Label: "first", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tone\n"},
		Cookie{ID: "b", Label: "second", Data: ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\ttwo\n"})
	r.SetCookiePool(pool)
	mine := ".youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tmine\n"
	expired := cookieLine(".youtube.com", "LOGIN_INFO", time.Now().Add(-time.Hour))
	r.SetUserCookies(func(session string) []Cookie {
		if session != "u" {
			return nil
		}
		return []Cookie{{ID: UserCookiePrefix + "1", Label: "mine", Domains: CookieDomains(mine), Data: mine},
			{ID: UserCookiePrefix + "2", Label: "old", Domains: CookieDomains(expired), Data: expired}}
	})
	out := call(t, r, "u", ToolDownloadMedia, map[string]any{"url": "ytsearch1:botcheck"})
	if !strings.Contains(out, "Test Song.mp3") || !strings.Contains(out, "own cookie for this site was refused") ||
		strings.Count(out, "was blocked and rests for") != 1 {
		t.Fatalf("the user's cookie first, then the pool: %s", out)
	}
	if pool.RestingUntil("a").IsZero() || !pool.RestingUntil("b").IsZero() {
		t.Error("only the pool cookie that was blocked rests; the expired user cookie is never tried")
	}

	out = call(t, r, "v", ToolDownloadMedia, map[string]any{"url": "ytsearch1:botcheck"})
	if strings.Contains(out, "own cookie") {
		t.Errorf("another user never gets someone's own cookies: %s", out)
	}
}

func TestGalleryDLTriesWithoutCookiesAfterALoginWall(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	pool := NewMemoryCookiePool(Cookie{ID: "ip", Label: "site", Data: "93.184.216.34\tFALSE\t/\tFALSE\t0\tsid\tx\n"})
	r.SetCookiePool(pool)
	out := call(t, r, "g", ToolDownloadMedia, map[string]any{"url": "https://93.184.216.34/p/walled-photos"})
	if !strings.Contains(out, `Downloaded "post_1.jpg"`) {
		t.Fatalf("the last try goes without cookies: %s", out)
	}
	if pool.RestingUntil("ip").IsZero() {
		t.Error("the refused cookie rests")
	}
}

func TestTestCookie(t *testing.T) {
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	ctx := context.Background()
	now := time.Now()
	good := cookieLine(".youtube.com", "LOGIN_INFO", now.Add(30*24*time.Hour))
	if res := r.TestCookie(ctx, good, "www.youtube.com"); !res.OK || !res.Live || res.Detail != "YouTube Turns 20" {
		t.Errorf("a working YouTube cookie: %+v", res)
	}
	if left, _ := filepath.Glob(filepath.Join(must(r.Machine.Workspace("cookie-check")), ".test_cookies*")); len(left) > 0 {
		t.Error("the cookies never stay in the workspace")
	}
	if res := r.TestCookie(ctx, cookieLine(".youtube.com", "LOGIN_INFO", now.Add(-time.Hour)), "youtube.com"); res.OK || !strings.Contains(res.Detail, "ran out") {
		t.Errorf("an expired login is not tried: %+v", res)
	}
	if res := r.TestCookie(ctx, cookieLine(".youtube.com", "PREF", time.Time{}), "youtube.com"); res.OK || !strings.Contains(res.Detail, "no login") {
		t.Errorf("a logged-out export: %+v", res)
	}
	if res := r.TestCookie(ctx, cookieLine(".instagram.com", "sessionid", time.Time{}), "instagram.com"); !res.OK || res.Live {
		t.Errorf("other sites are only read: %+v", res)
	}
	if !KnownLoginSite(".m.tiktok.com") || KnownLoginSite("example.com") {
		t.Error("KnownLoginSite")
	}
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}
