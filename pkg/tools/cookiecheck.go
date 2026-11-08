package tools

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// Checking cookies before they are used, as tg-ytdlp-bot does: a cookies.txt
// whose login has run out is skipped without a wasted download, and a
// YouTube cookie can be tried for real (yt-dlp reads a video's details with
// it, nothing is downloaded).

// loginCookies are the cookies that carry a site's login.
var loginCookies = map[string][]string{
	"youtube.com":    {"SID", "SAPISID", "__Secure-3PAPISID", "__Secure-1PSID", "LOGIN_INFO"},
	"google.com":     {"SID", "SAPISID", "__Secure-3PAPISID", "__Secure-1PSID"},
	"instagram.com":  {"sessionid"},
	"tiktok.com":     {"sessionid", "sid_tt"},
	"x.com":          {"auth_token"},
	"twitter.com":    {"auth_token"},
	"facebook.com":   {"c_user", "xs"},
	"soundcloud.com": {"oauth_token"},
	"reddit.com":     {"reddit_session", "token_v2"},
	"twitch.tv":      {"auth-token"},
	"pinterest.com":  {"_pinterest_sess"},
	"vimeo.com":      {"vimeo"},
}

// CookieLogin is what a cookies.txt holds for one site.
type CookieLogin struct {
	Site    string
	Expires time.Time // when the login runs out (zero: it lasts the session)
	Expired bool
}

// CookieLogins lists the logins of a Netscape cookies.txt, by site.
func CookieLogins(data string, now time.Time) []CookieLogin {
	type found struct {
		latest  time.Time
		session bool
	}
	sites := map[string]*found{}
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#HttpOnly_"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			continue
		}
		domain := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(f[0]), "."), "www.")
		for site, names := range loginCookies {
			if domain != site && !strings.HasSuffix(domain, "."+site) {
				continue
			}
			for _, n := range names {
				if f[5] != n || strings.TrimSpace(f[6]) == "" {
					continue
				}
				s := sites[site]
				if s == nil {
					s = &found{}
					sites[site] = s
				}
				exp, _ := strconv.ParseInt(f[4], 10, 64)
				if exp <= 0 {
					s.session = true
				} else if t := time.Unix(exp, 0); t.After(s.latest) {
					s.latest = t
				}
			}
		}
	}
	var out []CookieLogin
	for site, s := range sites {
		l := CookieLogin{Site: site}
		if !s.session {
			l.Expires, l.Expired = s.latest, s.latest.Before(now)
		}
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Site < out[j].Site })
	return out
}

// CookieExpired reports a cookie whose login for host has run out (one
// without a known login for the site is not judged).
func CookieExpired(c Cookie, host string, now time.Time) bool {
	host = strings.TrimPrefix(strings.ToLower(host), "www.")
	for _, l := range CookieLogins(c.Data, now) {
		if host == l.Site || strings.HasSuffix(host, "."+l.Site) {
			return l.Expired
		}
	}
	return false
}

// cookieTestURL is the video a YouTube cookie is tried on (YouTube's own channel).
const cookieTestURL = "https://www.youtube.com/watch?v=_GuOjXYl5ew"

// CookieTest is the outcome of trying a cookie.
type CookieTest struct {
	OK      bool
	Live    bool   // tried on the site itself (YouTube); otherwise only read
	Refused bool   // the account's fault (no login, run out, refused), not the network's or the video's
	Detail  string // the video title, or why it failed
}

// TestCookie checks a cookies.txt for host: the login is there and current,
// and for YouTube yt-dlp can read a video's details with it.
func (r *Registry) TestCookie(ctx context.Context, data, host string) CookieTest {
	host = cookieHost("", host)
	now := time.Now()
	logins := CookieLogins(data, now)
	var mine *CookieLogin
	for i := range logins {
		if host == logins[i].Site || strings.HasSuffix(host, "."+logins[i].Site) {
			mine = &logins[i]
		}
	}
	switch {
	case mine == nil && loginCookies[host] != nil:
		return CookieTest{Refused: true, Detail: "no login of " + host + " in it (logged out, or exported from another site)"}
	case mine != nil && mine.Expired:
		return CookieTest{Refused: true, Detail: "the login ran out on " + mine.Expires.Format("2006-01-02")}
	case host != "youtube.com":
		return CookieTest{OK: true, Detail: "the login is there"}
	}
	d := r.downloaderInfo()
	if d == nil || len(d.YtDlp) == 0 {
		return CookieTest{OK: true, Detail: "the login is there (yt-dlp is missing, so it was not tried)"}
	}
	// Each try has a private folder of its own, so several run side by side.
	const session = "cookie-check"
	secret, err := newSecretDir()
	if err != nil {
		return CookieTest{Detail: err.Error()}
	}
	defer secret.close()
	if err := secret.write(cookiesName, []byte(data)); err != nil {
		return CookieTest{Detail: err.Error()}
	}
	argv := append([]string(nil), d.YtDlp...)
	argv = append(argv, "--ignore-config", "--no-colors", "--no-cache-dir", "--no-warnings", "--skip-download", "--no-playlist",
		"--ignore-no-formats-error", "--socket-timeout", "15", "--extractor-retries", "1", "--print", "%(title)s", "--cookies", secret.path(r.Machine, cookiesName))
	if d.JSRuntime != "" && d.flags["--js-runtimes"] {
		argv = append(argv, "--js-runtimes", d.JSRuntime)
	}
	if d.flags["--remote-components"] {
		argv = append(argv, "--remote-components", "ejs:github")
	}
	argv = append(argv, "--", cookieTestURL)
	res := r.Machine.Run(ctx, session, machine.RunOptions{Args: argv, Timeout: 60 * time.Second})
	out, errText := strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr+"\n"+res.Error)
	switch {
	case res.ExitCode == 0 && out != "":
		return CookieTest{OK: true, Live: true, Detail: lastLine(out)}
	case containsAny(errText, "requested format is not available"): // a format problem, not the login's
		return CookieTest{OK: true, Live: true, Detail: "works (" + tail(lastLine(errText), 120) + ")"}
	}
	return CookieTest{Live: true, Refused: cookieBlocked(errText), Detail: tail(lastLine(errText), 200)}
}

// cookieRotationsPerHour bounds the cookies one user's downloads may go
// through in an hour, so one user cannot burn the whole pool.
const cookieRotationsPerHour = 8

type rotationLimiter struct {
	mu   sync.Mutex
	used map[string][]time.Time
}

func (l *rotationLimiter) allow(session string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used == nil {
		l.used = map[string][]time.Time{}
	}
	recent := l.used[session][:0]
	for _, t := range l.used[session] {
		if now.Sub(t) < time.Hour {
			recent = append(recent, t)
		}
	}
	if len(recent) >= cookieRotationsPerHour {
		l.used[session] = recent
		return false
	}
	l.used[session] = append(recent, now)
	return true
}

// UserCookiePrefix starts the id of a cookie a user added for their own downloads.
const UserCookiePrefix = "user:"

// SetUserCookies gives the downloaders each user's own cookies (tried
// before the shared pool, and only for that user).
func (r *Registry) SetUserCookies(f func(sessionID string) []Cookie) { r.userCookies = f }

// KnownLoginSite reports a domain whose login cookies are known.
func KnownLoginSite(domain string) bool {
	domain = strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(domain), "."), "www.")
	for site := range loginCookies {
		if domain == site || strings.HasSuffix(domain, "."+site) {
			return true
		}
	}
	return false
}
