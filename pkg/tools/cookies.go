package tools

import (
	"maps"
	"math/rand/v2"
	"sort"
	"strings"
	"sync"
	"time"
)

// Cookies for sites that ask for a login (YouTube's bot check, Instagram…).
// Several accounts can be added; each download takes the least recently used
// cookie that fits the site. When a site blocks one (rate limit, login wall),
// that cookie rests for 30–60 minutes and the download is retried at once
// with the next one.

// Cookie is one cookies.txt of the pool.
type Cookie struct {
	ID      string
	Label   string
	Domains []string // the sites it holds a login for
	Data    string
}

// CookiePool hands out cookies and rests the ones a site blocked.
type CookiePool interface {
	// Pick returns a ready cookie for host, never one in skip.
	Pick(host string, skip map[string]bool) (Cookie, bool)
	// Rest takes a cookie out of use for d after a block.
	Rest(id string, d time.Duration, reason string)
	// Good records a successful use.
	Good(id string)
}

// CookieDomains lists the domains of a Netscape cookies.txt.
func CookieDomains(data string) []string {
	seen := map[string]bool{}
	var out []string
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "#HttpOnly_"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) != 7 {
			continue
		}
		d := strings.ToLower(strings.TrimPrefix(f[0], "."))
		d = strings.TrimPrefix(d, "www.")
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	sort.Strings(out)
	return out
}

// cookieHost is the site a target belongs to (searches included).
func cookieHost(target, host string) string {
	switch {
	case strings.HasPrefix(target, "ytsearch"):
		return "youtube.com"
	case strings.HasPrefix(target, "scsearch"):
		return "soundcloud.com"
	case host == "youtu.be":
		return "youtube.com"
	}
	return strings.TrimPrefix(strings.ToLower(host), "www.")
}

// CookieFits reports whether a cookie holds a login for host.
func CookieFits(c Cookie, host string) bool {
	for _, d := range c.Domains {
		if host == d || strings.HasSuffix(host, "."+d) || strings.HasSuffix(d, "."+host) {
			return true
		}
	}
	return false
}

// cookieRest is how long a blocked cookie waits: 30–60 minutes.
func cookieRest() time.Duration { return 30*time.Minute + rand.N(30*time.Minute) }

// cookieBlocked: the site refused this account (rest it and use another).
// A video kept from the server's country or needing an older account says
// nothing about the cookie: it never rests one.
func cookieBlocked(e string) bool {
	if geoBlocked(e) || ageGated(e) {
		return false
	}
	return containsAny(e, "429", "too many requests", "rate-limit", "rate limit", "sign in to confirm", "not a bot", "login required",
		"log in to", "checkpoint", "challenge_required", "cookies are no longer valid", "account has been")
}

// privateForAccount: another account might see it (try the next cookie, no rest).
func privateForAccount(e string) bool {
	return ageGated(e) || containsAny(e, "private video", "private account", "this content isn't available", "members-only", "join this channel")
}

// geoBlocked: the item is not shown in the server's country; no account,
// retry or cookie changes that.
func geoBlocked(e string) bool {
	return containsAny(e, "not available in your country", "available in your country", "from your location", "geo restrict",
		"geo-restrict", "georestrict", "blocked it in your country", "not available in this country")
}

// ageGated: the item needs an account old enough (another one may be).
func ageGated(e string) bool {
	return containsAny(e, "confirm your age", "age-restricted", "age restricted", "inappropriate for some users")
}

// forbidden: an HTTP 403, which the account, the IP or a stale link may
// cause: another account is tried once, and this one does not rest for it.
func forbidden(e string) bool {
	return containsAny(e, "http error 403", "403: forbidden", "403 forbidden")
}

// MemoryCookiePool is a CookiePool kept in memory (cookies from files).
type MemoryCookiePool struct {
	mu      sync.Mutex
	cookies []Cookie
	resting map[string]time.Time
	used    map[string]time.Time
}

// NewMemoryCookiePool builds a pool from cookies.txt contents.
func NewMemoryCookiePool(cookies ...Cookie) *MemoryCookiePool {
	p := &MemoryCookiePool{resting: map[string]time.Time{}, used: map[string]time.Time{}}
	for _, c := range cookies {
		if len(c.Domains) == 0 {
			c.Domains = CookieDomains(c.Data)
		}
		p.cookies = append(p.cookies, c)
	}
	return p
}

func (p *MemoryCookiePool) Pick(host string, skip map[string]bool) (Cookie, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	var best *Cookie
	for i := range p.cookies {
		c := &p.cookies[i]
		if skip[c.ID] || time.Now().Before(p.resting[c.ID]) || !CookieFits(*c, host) || CookieExpired(*c, host, time.Now()) {
			continue
		}
		if best == nil || p.used[c.ID].Before(p.used[best.ID]) {
			best = c
		}
	}
	if best == nil {
		return Cookie{}, false
	}
	p.used[best.ID] = time.Now()
	return *best, true
}

func (p *MemoryCookiePool) Rest(id string, d time.Duration, _ string) {
	p.mu.Lock()
	p.resting[id] = time.Now().Add(d)
	p.mu.Unlock()
}

func (p *MemoryCookiePool) Good(id string) {
	p.mu.Lock()
	p.used[id] = time.Now()
	p.mu.Unlock()
}

// Inherit carries the rests and the order of use of an older pool over (its
// cookies that are still here keep their place).
func (p *MemoryCookiePool) Inherit(old *MemoryCookiePool) {
	old.mu.Lock()
	resting, used := maps.Clone(old.resting), maps.Clone(old.used)
	old.mu.Unlock()
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.cookies {
		if t, ok := resting[c.ID]; ok {
			p.resting[c.ID] = t
		}
		if t, ok := used[c.ID]; ok {
			p.used[c.ID] = t
		}
	}
}

// RestingUntil reports when a resting cookie is back (zero when ready).
func (p *MemoryCookiePool) RestingUntil(id string) time.Time {
	p.mu.Lock()
	defer p.mu.Unlock()
	if t := p.resting[id]; time.Now().Before(t) {
		return t
	}
	return time.Time{}
}

// SetCookiePool gives the downloaders cookies for sites that ask for a login.
func (r *Registry) SetCookiePool(p CookiePool) { r.cookiePool = p }
