package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// cookiePool gives the downloaders the cookies added in the admin panel
// (database) and the cookies.txt files of WAZE_COOKIES_DIR. Picks prefer
// the least recently used ready cookie; a blocked one rests for a while.
// A cookie added from a link (as tg-ytdlp-bot's cookie URLs) is fetched
// again when it is blocked or its login runs out, and every few hours.
type cookiePool struct {
	st   *store.Store
	dir  string
	http *http.Client // fetches the linked cookies: the registry's client

	mu         sync.Mutex
	files      *tools.MemoryCookiePool
	filesSig   string
	filesAt    time.Time // when the folder was last looked at
	refreshing map[int64]bool
	queue      chan store.DLCookie // linked cookies waiting to be fetched again
}

func newCookiePool(st *store.Store, dir string) *cookiePool { return &cookiePool{st: st, dir: dir} }

// plainFetch fetches cookies from their links (they are fetched again by
// themselves, for good).
var plainFetch = netclient.NewClient(netclient.Options{Timeout: 30 * time.Second})

const (
	cookieDBPrefix  = "db:"
	cookieDirPrefix = "dir:"
	maxCookieBytes  = 1 << 20

	cookieRefreshEvery = 6 * time.Hour
	cookieRefreshGap   = 5 * time.Minute // a blocked linked cookie is not fetched more often
	cookieFolderTTL    = 5 * time.Second // the folder is listed again at most this often
	cookieFetchers     = 2               // linked cookies fetched at once
)

// folder reloads the files of the cookies folder when they change.
func (p *cookiePool) folder() *tools.MemoryCookiePool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dir == "" {
		return nil
	}
	if p.files != nil && time.Since(p.filesAt) < cookieFolderTTL {
		return p.files
	}
	p.filesAt = time.Now()
	entries, _ := filepath.Glob(filepath.Join(p.dir, "*.txt"))
	sort.Strings(entries)
	var sig strings.Builder
	for _, e := range entries {
		if fi, err := os.Stat(e); err == nil {
			sig.WriteString(e + strconv.FormatInt(fi.ModTime().UnixNano(), 10) + ";")
		}
	}
	if p.files != nil && sig.String() == p.filesSig {
		return p.files
	}
	var cookies []tools.Cookie
	for _, e := range entries {
		data, err := os.ReadFile(e)
		if err != nil || len(data) > maxCookieBytes {
			continue
		}
		cookies = append(cookies, tools.Cookie{ID: cookieDirPrefix + filepath.Base(e), Label: filepath.Base(e), Data: string(data)})
	}
	old := p.files
	p.files, p.filesSig = tools.NewMemoryCookiePool(cookies...), sig.String()
	if old != nil { // the cookies still there keep their rests and their turn
		p.files.Inherit(old)
	}
	return p.files
}

func (p *cookiePool) Pick(host string, skip map[string]bool) (tools.Cookie, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if list, err := p.st.Cookies(ctx); err == nil { // least recently used first
		now := p.st.Now()
		for _, c := range list {
			tc := tools.Cookie{ID: cookieDBPrefix + strconv.FormatInt(c.ID, 10), Label: c.Label, Domains: c.Domains, Data: c.Data}
			if skip[tc.ID] || c.Resting(now) || !tools.CookieFits(tc, host) {
				continue
			}
			if tools.CookieExpired(tc, host, now) {
				p.refreshLater(c)
				continue
			}
			_ = p.st.CookieUsed(ctx, c.ID)
			return tc, true
		}
	}
	if f := p.folder(); f != nil {
		return f.Pick(host, skip)
	}
	return tools.Cookie{}, false
}

func (p *cookiePool) Rest(id string, d time.Duration, reason string) {
	log.Printf("downloader: cookie %s rests for %s: %s", id, d.Round(time.Minute), reason)
	if n, ok := strings.CutPrefix(id, cookieDBPrefix); ok {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if dbID, err := strconv.ParseInt(n, 10, 64); err == nil {
			_ = p.st.RestCookie(ctx, dbID, p.st.Now().Add(d), reason)
			if c, ok := p.byID(ctx, dbID); ok {
				p.refreshLater(c)
			}
		}
		return
	}
	if f := p.folder(); f != nil {
		f.Rest(id, d, reason)
	}
}

func (p *cookiePool) Good(id string) {
	if strings.HasPrefix(id, cookieDirPrefix) {
		if f := p.folder(); f != nil {
			f.Good(id)
		}
	}
}

func (p *cookiePool) byID(ctx context.Context, id int64) (store.DLCookie, bool) {
	c, err := p.st.Cookie(ctx, id)
	return c, err == nil
}

// user gives a user's own cookies (/cookies) to their downloads.
func (p *cookiePool) user(sessionID string) []tools.Cookie {
	n, ok := strings.CutPrefix(sessionID, "tg-")
	if !ok {
		return nil
	}
	userID, err := strconv.ParseInt(n, 10, 64)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	list, err := p.st.UserCookies(ctx, userID)
	if err != nil {
		return nil
	}
	out := make([]tools.Cookie, 0, len(list))
	for _, c := range list {
		out = append(out, tools.Cookie{ID: tools.UserCookiePrefix + strconv.FormatInt(c.ID, 10), Label: c.Label, Domains: c.Domains, Data: c.Data})
	}
	return out
}

// fetchCookies downloads a cookies.txt from a link.
func (p *cookiePool) fetchCookies(ctx context.Context, link string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(link))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", errors.New("not an http(s) link")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return "", err
	}
	client := p.http
	if client == nil {
		client = plainFetch
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxCookieBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxCookieBytes {
		return "", errors.New("larger than 1 MB")
	}
	text := strings.TrimPrefix(string(data), "\uFEFF")
	if !looksLikeCookies(text) || len(tools.CookieDomains(text)) == 0 {
		return "", errors.New("not a Netscape cookies.txt")
	}
	return text, nil
}

// refresh fetches a linked cookie again; it reports whether it changed (new
// cookies are ready at once).
func (p *cookiePool) refresh(ctx context.Context, c store.DLCookie) (bool, error) {
	data, err := p.fetchCookies(ctx, c.SourceURL)
	if err != nil {
		return false, err
	}
	changed := data != c.Data
	return changed, p.st.RefreshCookie(ctx, c.ID, tools.CookieDomains(data), data, changed)
}

// refreshLater queues a linked cookie to be fetched again: a few workers
// fetch them one after the other, so a start with many stale cookies never
// fires them all at once.
func (p *cookiePool) refreshLater(c store.DLCookie) {
	if c.SourceURL == "" || c.FetchedAt != nil && p.st.Now().Sub(*c.FetchedAt) < cookieRefreshGap {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refreshing[c.ID] {
		return
	}
	if p.refreshing == nil {
		p.refreshing = map[int64]bool{}
	}
	if p.queue == nil {
		p.queue = make(chan store.DLCookie, 64)
		for range cookieFetchers {
			go p.fetcher()
		}
	}
	select {
	case p.queue <- c:
		p.refreshing[c.ID] = true
	default: // many waiting already: the next round takes it
	}
}

func (p *cookiePool) fetcher() {
	for c := range p.queue {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		switch changed, err := p.refresh(ctx, c); {
		case err != nil:
			log.Printf("downloader: could not fetch cookie #%d again from its link: %v", c.ID, err)
		case changed:
			log.Printf("downloader: cookie #%d was fetched again from its link (new cookies)", c.ID)
		}
		cancel()
		p.mu.Lock()
		delete(p.refreshing, c.ID)
		p.mu.Unlock()
		time.Sleep(2 * time.Second) // spread over time
	}
}

// refreshLoop fetches the linked cookies again every few hours.
func (p *cookiePool) refreshLoop(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if list, err := p.st.Cookies(ctx); err == nil {
			now := p.st.Now()
			for _, c := range list {
				if c.SourceURL != "" && (c.FetchedAt == nil || now.Sub(*c.FetchedAt) >= cookieRefreshEvery) {
					p.refreshLater(c)
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// folderStatus counts the folder's cookies and the resting ones.
func (p *cookiePool) folderStatus() (total, resting int) {
	f := p.folder()
	if f == nil {
		return 0, 0
	}
	entries, _ := filepath.Glob(filepath.Join(p.dir, "*.txt"))
	for _, e := range entries {
		total++
		if !f.RestingUntil(cookieDirPrefix + filepath.Base(e)).IsZero() {
			resting++
		}
	}
	return total, resting
}

// migrateSingleCookie moves the single cookies.txt of older versions into the pool.
func migrateSingleCookie(ctx context.Context, st *store.Store) {
	data, err := st.SecretSetting(settingDLCookies)
	if err != nil || strings.TrimSpace(data) == "" {
		return
	}
	domains := tools.CookieDomains(data)
	sum := sha1.Sum([]byte(data))
	if _, err := st.AddCookie(ctx, cookieLabel(domains, hex.EncodeToString(sum[:3])), domains, data); err == nil {
		_ = st.SetSecretSetting(ctx, settingDLCookies, "")
		log.Printf("downloader: the cookies.txt was moved into the cookie pool")
	}
}

// cookieLabel names a cookie after its main sites.
func cookieLabel(domains []string, suffix string) string {
	main := []string{}
	for _, d := range domains {
		if strings.Count(d, ".") == 1 && len(main) < 2 {
			main = append(main, d)
		}
	}
	if len(main) == 0 && len(domains) > 0 {
		main = domains[:1]
	}
	label := strings.Join(main, ", ")
	if label == "" {
		label = "cookies"
	}
	if suffix != "" {
		label += " · " + suffix
	}
	return label
}

// The pool's cookies are tried in the background now and then, one at a
// time (as 🩺 does): a cookie whose account was refused rests before a
// user's download meets it, and a linked one is fetched again first. A
// failure that is not the account's (the network, the video) rests nothing.
const (
	cookieCheckEvery = 6 * time.Hour
	cookieCheckRest  = 6 * time.Hour
)

func (p *cookiePool) checkLoop(ctx context.Context, test func(ctx context.Context, data, host string) tools.CookieTest) {
	t := time.NewTimer(15*time.Minute + rand.N(15*time.Minute))
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := p.checkAll(ctx, test); n > 0 {
			log.Printf("downloader: the background check rested %d cookies", n)
		}
		t.Reset(cookieCheckEvery + rand.N(30*time.Minute))
	}
}

// checkAll tries the cookies that are not resting and returns how many it rested.
func (p *cookiePool) checkAll(ctx context.Context, test func(ctx context.Context, data, host string) tools.CookieTest) int {
	list, err := p.st.Cookies(ctx)
	if err != nil {
		return 0
	}
	rested := 0
	for _, c := range list {
		if ctx.Err() != nil {
			break
		}
		if c.Resting(p.st.Now()) {
			continue
		}
		site := cookieMainSite(c.Data, c.Domains)
		res := test(ctx, c.Data, site)
		if !res.OK && c.SourceURL != "" {
			if changed, err := p.refresh(ctx, c); err == nil && changed {
				if fresh, ok := p.byID(ctx, c.ID); ok {
					res = test(ctx, fresh.Data, site)
				}
			}
		}
		if !res.OK && res.Refused {
			p.Rest(cookieDBPrefix+strconv.FormatInt(c.ID, 10), cookieCheckRest, "background check: "+res.Detail)
			rested++
		}
	}
	return rested
}
