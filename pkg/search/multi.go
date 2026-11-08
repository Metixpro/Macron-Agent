package search

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	xhtml "golang.org/x/net/html"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Every search asks all the engines at once — treg.to (when it has a
// token), DuckDuckGo and Bing for the web; Bing News and Google News for
// news — and merges their lists: a page several engines found ranks higher
// (reciprocal rank fusion), the same page under two addresses comes once,
// and no site fills the list. An engine that keeps failing (a captcha, a
// block) rests for a while, so it does not slow every search. The first
// list in starts a short grace time for the others; one that has not
// answered by then is left out. Alongside, a quick answer is looked up
// (DuckDuckGo's instant answer, else the Wikipedia article the query
// names). The same search within a few minutes is answered from memory.

// SearchOptions shape one search.
type SearchOptions struct {
	Limit  int
	Time   string // "day", "week", "month", "year"; "" = any time
	News   bool   // news articles, newest first, with their dates and outlets
	Answer bool   // also look for a quick answer
}

// SearchReport is what a search found.
type SearchReport struct {
	Results []SearchResult
	Answer  *QuickAnswer
	Engines []string // the engines whose results are in the list
	Failed  []string // engines that failed this time ("bing: blocked")
}

// QuickAnswer is a short, sourced answer from an encyclopedia or a calculation.
type QuickAnswer struct {
	Title, Text, URL, Source string
	Facts                    []string // "Height: 435 m"
}

type webEngine struct {
	name   string
	weight float64
	run    func(ctx context.Context, query string, opt SearchOptions, limit int) ([]SearchResult, error)
}

const (
	rrfK        = 20.0
	perDomain   = 2 // in the top of the list
	engineLimit = 12 * time.Second
	coolAfter   = 2 // failures in a row before an engine rests
	coolFor     = 10 * time.Minute
	cacheFor    = 3 * time.Minute
	cacheSize   = 200
)

// searchGrace is how long the other engines may take once one has answered.
var searchGrace = 2500 * time.Millisecond

var (
	errBlocked   = errors.New("blocked (a bot check)")
	errUnrelated = errors.New("unrelated results (the engine distrusts this server)")
)

// termsIn counts the query's words in a result's title, snippet and address.
func termsIn(r SearchResult, terms []string) int {
	addr, _ := url.PathUnescape(r.URL)
	hay := normalize(r.Title + " " + r.Snippet + " " + r.Source + " " + addr)
	n := 0
	for _, t := range terms {
		if strings.Contains(hay, t) {
			n++
		}
	}
	return n
}

// engineState is how an engine has been doing.
type engineState struct {
	Name      string
	Hits      int64
	Fails     int64
	InARow    int
	RestUntil time.Time
	LastErr   string
	LastAt    time.Time
}

type engineHealth struct {
	mu    sync.Mutex
	state map[string]*engineState
}

func (h *engineHealth) get(name string) *engineState {
	if h.state == nil {
		h.state = map[string]*engineState{}
	}
	s := h.state[name]
	if s == nil {
		s = &engineState{Name: name}
		h.state[name] = s
	}
	return s
}

func (h *engineHealth) resting(name string, now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return now.Before(h.get(name).RestUntil)
}

func (h *engineHealth) note(name string, err error, now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	s := h.get(name)
	s.LastAt = now
	if err == nil {
		s.Hits++
		s.InARow, s.LastErr = 0, ""
		return
	}
	s.Fails++
	s.InARow++
	s.LastErr = textutil.TruncateRunes(err.Error(), 200, "…")
	if s.InARow >= coolAfter {
		s.RestUntil = now.Add(coolFor)
	}
}

// EngineStatus is how each search engine has been doing since the start.
func (e *Engine) EngineStatus() []engineState {
	e.health.mu.Lock()
	defer e.health.mu.Unlock()
	var out []engineState
	for _, s := range e.health.state {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

type cachedReport struct {
	at  time.Time
	rep SearchReport
}

type searchCache struct {
	mu   sync.Mutex
	m    map[string]cachedReport
	keys []string
}

func (c *searchCache) get(key string, now time.Time) (SearchReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	x, ok := c.m[key]
	if !ok || now.Sub(x.at) > cacheFor {
		return SearchReport{}, false
	}
	rep := x.rep
	rep.Results = append([]SearchResult(nil), rep.Results...) // passages are added to a copy
	return rep, true
}

func (c *searchCache) put(key string, rep SearchReport, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedReport{}
	}
	if _, ok := c.m[key]; !ok {
		c.keys = append(c.keys, key)
	}
	rep.Results = append([]SearchResult(nil), rep.Results...)
	c.m[key] = cachedReport{now, rep}
	for len(c.keys) > cacheSize {
		delete(c.m, c.keys[0])
		c.keys = c.keys[1:]
	}
}

// webEngines are the engines of a search, in their order of trust.
func (e *Engine) webEngines(opt SearchOptions) []webEngine {
	var list []webEngine
	if opt.News {
		if e.bingNewsURL != "" {
			list = append(list, webEngine{"bing-news", 1.0, e.searchBingNews})
		}
		if e.gnewsURL != "" {
			list = append(list, webEngine{"google-news", 0.8, e.searchGoogleNews})
		}
		return list
	}
	if token := e.Treg.Token(); token != "" && opt.Time == "" {
		list = append(list, webEngine{"treg", 1.2, func(ctx context.Context, q string, _ SearchOptions, limit int) ([]SearchResult, error) {
			res, served, err := e.Treg.Search(ctx, e.HTTPClient, token, q, limit)
			e.Treg.record(served, len(res), err)
			if err != nil && ctx.Err() == nil {
				logTregFailure(err)
			}
			return res, err
		}})
	}
	if e.liteURL != "" || e.htmlURL != "" {
		list = append(list, webEngine{"duckduckgo", 1.0, e.searchDDG})
	}
	if e.bingURL != "" {
		list = append(list, webEngine{"bing", 1.0, e.searchBing})
	}
	return list
}

type engineResult struct {
	engine webEngine
	res    []SearchResult
	err    error
}

// SearchWith runs one search on every engine at once and merges the lists.
func (e *Engine) SearchWith(ctx context.Context, query string, opt SearchOptions) (*SearchReport, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("empty query")
	}
	if opt.Limit <= 0 {
		opt.Limit = 5
	}
	opt.Limit = min(opt.Limit, 15)
	switch opt.Time {
	case "", "day", "week", "month", "year":
	default:
		opt.Time = ""
	}
	key := fmt.Sprintf("%s\x00%d\x00%s\x00%v\x00%v", query, opt.Limit, opt.Time, opt.News, opt.Answer)
	now := time.Now()
	if rep, ok := e.cache.get(key, now); ok {
		return &rep, nil
	}

	engines := e.webEngines(opt)
	var awake []webEngine
	for _, en := range engines {
		if !e.health.resting(en.name, now) {
			awake = append(awake, en)
		}
	}
	if len(awake) == 0 { // all resting: ask them anyway rather than find nothing
		awake = engines
	}
	if len(awake) == 0 {
		return nil, errors.New("no search engine is configured")
	}

	ectx, cancelAll := context.WithCancel(ctx)
	defer cancelAll()
	answer := make(chan *QuickAnswer, 1)
	if opt.Answer && !opt.News {
		go func() { answer <- e.quickAnswer(ectx, query) }()
	} else {
		answer <- nil
	}
	got := make(chan engineResult, len(awake))
	for _, en := range awake {
		go func() {
			c, cancel := context.WithTimeout(ectx, engineLimit)
			defer cancel()
			res, err := en.run(c, query, opt, opt.Limit+5)
			if err != nil && ectx.Err() != nil && ctx.Err() == nil {
				err = context.Canceled // left out after the grace time: not the engine's fault
			}
			if err != nil && !errors.Is(err, context.Canceled) {
				e.health.note(en.name, err, time.Now()) // a success counts once its list is judged
			}
			got <- engineResult{en, res, err}
		}()
	}
	var lists []engineResult
	var grace <-chan time.Time
collect:
	for len(lists) < len(awake) {
		select {
		case r := <-got:
			lists = append(lists, r)
			if len(r.res) > 0 && grace == nil {
				grace = time.After(searchGrace)
			}
		case <-grace:
			break collect
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	order := map[string]int{}
	for i, en := range awake {
		order[en.name] = i
	}
	sort.SliceStable(lists, func(i, j int) bool { return order[lists[i].engine.name] < order[lists[j].engine.name] }) // not the order they came in
	dropUnrelated(lists, query, func(name string) { e.health.note(name, errUnrelated, time.Now()) })
	rep := &SearchReport{}
	var firstErr error
	for _, r := range lists {
		switch {
		case len(r.res) > 0:
			rep.Engines = append(rep.Engines, r.engine.name)
			e.health.note(r.engine.name, nil, time.Now())
		case r.err != nil:
			rep.Failed = append(rep.Failed, r.engine.name+": "+textutil.TruncateRunes(r.err.Error(), 120, "…"))
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", r.engine.name, r.err)
			}
		}
	}
	rep.Results = fuse(lists, opt.Limit)
	if opt.News {
		rep.Results = sortNews(rep.Results, opt.Time, now)
	}
	select {
	case rep.Answer = <-answer:
	case <-time.After(1500 * time.Millisecond):
	}
	if len(rep.Results) == 0 && firstErr != nil {
		return rep, fmt.Errorf("search failed: %w", firstErr)
	}
	if len(rep.Results) > 0 {
		e.cache.put(key, *rep, now)
	}
	return rep, nil
}

// dropUnrelated: an engine that distrusts the server answers with anything
// (its own company's pages, popular sites). Only results in the query's
// own script are judged (a Persian query may well find English pages).
// When most of an engine's list lacks the query's words, the list is not
// used and the engine rests (junk is called); when many do, those results
// go, unless another engine found them too.
func dropUnrelated(lists []engineResult, query string, junk func(engine string)) {
	terms := queryTerms(query)
	if len(terms) == 0 {
		return
	}
	foundBy := map[string]int{}
	for _, l := range lists {
		seen := map[string]bool{}
		for _, x := range l.res {
			if k := normURL(x.URL); !seen[k] {
				seen[k] = true
				foundBy[k]++
			}
		}
	}
	rtl := arabicRe.MatchString(query)
	for i, l := range lists {
		judged, related := 0, 0
		for _, x := range l.res {
			if arabicRe.MatchString(x.Title) == rtl {
				judged++
				if termsIn(x, terms) > 0 {
					related++
				}
			}
		}
		switch {
		case judged < 3 || related*10 >= judged*7:
		case related*10 < judged*3:
			lists[i].res, lists[i].err = nil, errUnrelated
			junk(l.engine.name)
		default:
			var kept []SearchResult
			for _, x := range l.res {
				if arabicRe.MatchString(x.Title) != rtl || termsIn(x, terms) > 0 || foundBy[normURL(x.URL)] > 1 {
					kept = append(kept, x)
				}
			}
			lists[i].res = kept
		}
	}
}

// fuse merges the engines' lists by reciprocal rank fusion.
func fuse(lists []engineResult, limit int) []SearchResult {
	type entry struct {
		r     SearchResult
		score float64
		order int
	}
	byKey := map[string]*entry{}
	var all []*entry
	for _, l := range lists {
		for rank, r := range l.res {
			if r.URL == "" || r.Title == "" {
				continue
			}
			k := normURL(r.URL)
			x := byKey[k]
			if x == nil {
				x = &entry{r: r, order: len(all)}
				byKey[k] = x
				all = append(all, x)
			} else {
				if len([]rune(r.Snippet)) > len([]rune(x.r.Snippet)) {
					x.r.Snippet = r.Snippet
				}
				if x.r.Date == "" {
					x.r.Date = r.Date
				}
				if x.r.Source == "" {
					x.r.Source = r.Source
				}
			}
			x.score += l.engine.weight / (rrfK + float64(rank) + 1)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].score != all[j].score {
			return all[i].score > all[j].score
		}
		return all[i].order < all[j].order
	})
	var head, tail []SearchResult
	perSite := map[string]int{}
	for _, x := range all {
		site := siteOf(x.r.URL)
		if perSite[site] >= perDomain {
			tail = append(tail, x.r)
			continue
		}
		perSite[site]++
		head = append(head, x.r)
	}
	out := append(head, tail...)
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// sortNews puts the newest first and leaves out what is older than the time asked for.
func sortNews(list []SearchResult, window string, now time.Time) []SearchResult {
	limit := map[string]time.Duration{"day": 36 * time.Hour, "week": 8 * 24 * time.Hour, "month": 32 * 24 * time.Hour, "year": 370 * 24 * time.Hour}[window]
	when := func(r SearchResult) time.Time {
		t, _ := time.Parse(time.RFC3339, r.Date)
		return t
	}
	sort.SliceStable(list, func(i, j int) bool { return when(list[i]).After(when(list[j])) })
	if limit == 0 {
		return list
	}
	var kept []SearchResult
	for _, r := range list {
		if t := when(r); t.IsZero() || now.Sub(t) <= limit {
			kept = append(kept, r)
		}
	}
	return kept
}

var trackingParams = map[string]bool{"fbclid": true, "gclid": true, "yclid": true, "mc_cid": true, "mc_eid": true, "ref": true, "ref_src": true, "igshid": true, "si": true}

// normURL is a page's address without what does not change the page:
// the scheme, www. and m., tracking parameters, the fragment, a final slash.
func normURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.ToLower(raw)
	}
	host := strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(u.Hostname()), "www."), "m.")
	q := u.Query()
	for k := range q {
		if strings.HasPrefix(strings.ToLower(k), "utm_") || trackingParams[strings.ToLower(k)] {
			q.Del(k)
		}
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	if enc := q.Encode(); enc != "" {
		p += "?" + enc
	}
	return host + p
}

// siteOf is a page's site (its host without www.).
func siteOf(raw string) string {
	if u, err := url.Parse(raw); err == nil {
		return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	return raw
}

// ---------------------------------------------------------------- engines

// searchDDG asks DuckDuckGo Lite, then its HTML page.
func (e *Engine) searchDDG(ctx context.Context, query string, opt SearchOptions, limit int) ([]SearchResult, error) {
	df := map[string]string{"day": "d", "week": "w", "month": "m", "year": "y"}[opt.Time]
	var firstErr error
	if e.liteURL != "" {
		res, err := e.searchLiteDF(ctx, query, df, limit)
		if len(res) > 0 {
			return res, nil
		}
		firstErr = err
	}
	if e.htmlURL == "" {
		return nil, firstErr
	}
	res, err := e.searchHTMLDF(ctx, query, df, limit)
	if err != nil && firstErr != nil {
		return nil, fmt.Errorf("%v; %w", firstErr, err)
	}
	return res, err
}

func (e *Engine) searchLiteDF(ctx context.Context, query, df string, limit int) ([]SearchResult, error) {
	form := url.Values{"q": {query}}
	if df != "" {
		form.Set("df", df)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.liteURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return e.ddgPage(req, query, limit, parseLite)
}

func (e *Engine) searchHTMLDF(ctx context.Context, query, df string, limit int) ([]SearchResult, error) {
	u := e.htmlURL + "?q=" + url.QueryEscape(query)
	if df != "" {
		u += "&df=" + df
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	return e.ddgPage(req, query, limit, parseHTMLResults)
}

func (e *Engine) ddgPage(req *http.Request, query string, limit int, parse func(*xhtml.Node, int) []SearchResult) ([]SearchResult, error) {
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept-Language", acceptLanguage(query))
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, errBlocked // DuckDuckGo's bot check answers 202
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("duckduckgo returned status %d", resp.StatusCode)
	}
	doc, err := xhtml.Parse(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	return parse(doc, limit), nil
}

func acceptLanguage(q string) string {
	if arabicRe.MatchString(q) {
		return "fa-IR,fa;q=0.9,en;q=0.6"
	}
	return "en-US,en;q=0.9"
}

var arabicRe = regexp.MustCompile(`\p{Arabic}`)

// searchBing reads Bing's results page.
func (e *Engine) searchBing(ctx context.Context, query string, opt SearchOptions, limit int) ([]SearchResult, error) {
	v := url.Values{"q": {query}, "count": {fmt.Sprint(min(limit, 20))}}
	switch opt.Time {
	case "day":
		v.Set("filters", `ex1:"ez1"`)
	case "week":
		v.Set("filters", `ex1:"ez2"`)
	case "month":
		v.Set("filters", `ex1:"ez3"`)
	case "year":
		today := int(time.Now().Unix() / 86400)
		v.Set("filters", fmt.Sprintf(`ex1:"ez5_%d_%d"`, today-365, today))
	}
	if arabicRe.MatchString(query) {
		v.Set("setlang", "fa")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.bingURL+"?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", acceptLanguage(query))
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusForbidden {
		return nil, errBlocked
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bing returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	doc, err := xhtml.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	res := parseBing(doc, limit)
	if len(res) == 0 && (strings.Contains(string(body), "/challenge") || strings.Contains(strings.ToLower(string(body)), "captcha")) {
		return nil, errBlocked
	}
	return res, nil
}

// parseBing reads the <li class="b_algo"> results of a Bing page.
func parseBing(doc *xhtml.Node, limit int) []SearchResult {
	var out []SearchResult
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if len(out) >= limit {
			return
		}
		if n.Type == xhtml.ElementNode && n.Data == "li" && hasClass(n, "b_algo") {
			if r, ok := bingResult(n); ok {
				out = append(out, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

func bingResult(li *xhtml.Node) (SearchResult, bool) {
	var r SearchResult
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if n.Type == xhtml.ElementNode {
			switch {
			case n.Data == "h2" && r.URL == "":
				for a := n.FirstChild; a != nil; a = a.NextSibling {
					if a.Type == xhtml.ElementNode && a.Data == "a" {
						r.URL, r.Title = bingTarget(attr(a, "href")), extractText(a)
					}
				}
				return
			case n.Data == "span" && hasClass(n, "news_dt") && r.Date == "":
				r.Date = extractText(n)
			case n.Data == "p" && r.Snippet == "":
				r.Snippet = extractText(n)
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(li)
	if r.Date != "" {
		r.Snippet = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(r.Snippet, r.Date), "·"))
	}
	r.Date = "" // Bing's dates are relative words in the page's language: kept in the snippet only
	return r, r.URL != "" && r.Title != "" && strings.HasPrefix(r.URL, "http")
}

// bingTarget turns Bing's click-tracking link into the page's own address
// (its u parameter is "a1" + the address in base64).
func bingTarget(href string) string {
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if !strings.HasSuffix(u.Hostname(), "bing.com") || !strings.HasPrefix(u.Path, "/ck/") {
		return href
	}
	enc := u.Query().Get("u")
	if !strings.HasPrefix(enc, "a1") {
		return ""
	}
	enc = enc[2:]
	for _, dec := range []*base64.Encoding{base64.RawURLEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.StdEncoding} {
		if b, err := dec.DecodeString(enc); err == nil && strings.HasPrefix(string(b), "http") {
			return string(b)
		}
	}
	return ""
}

type rssFeed struct {
	Items []struct {
		Title       string `xml:"title"`
		Link        string `xml:"link"`
		Description string `xml:"description"`
		PubDate     string `xml:"pubDate"`
		Source      string `xml:"source"`
		BingSource  string `xml:"Source"` // News:Source (its namespace is the feed's own address)
	} `xml:"channel>item"`
}

func (e *Engine) rss(ctx context.Context, u string) (*rssFeed, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("news feed returned status %d", resp.StatusCode)
	}
	var f rssFeed
	if err := xml.NewDecoder(io.LimitReader(resp.Body, maxPageBytes)).Decode(&f); err != nil {
		return nil, fmt.Errorf("news feed: %w", err)
	}
	return &f, nil
}

func rssDate(s string) string {
	for _, layout := range []string{time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 MST", "Mon, 2 Jan 2006 15:04:05 -0700"} {
		if t, err := time.Parse(layout, strings.TrimSpace(s)); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return ""
}

// searchBingNews reads Bing News' feed: the real links, dates and outlets.
func (e *Engine) searchBingNews(ctx context.Context, query string, opt SearchOptions, limit int) ([]SearchResult, error) {
	v := url.Values{"q": {query}, "format": {"rss"}}
	if iv := map[string]string{"day": "7", "week": "8", "month": "9"}[opt.Time]; iv != "" {
		v.Set("qft", `interval="`+iv+`"`)
	}
	f, err := e.rss(ctx, e.bingNewsURL+"?"+v.Encode())
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, it := range f.Items {
		link := it.Link
		if u, err := url.Parse(link); err == nil && strings.HasSuffix(u.Hostname(), "bing.com") {
			link = u.Query().Get("url")
		}
		if !strings.HasPrefix(link, "http") {
			continue
		}
		src := it.BingSource
		if src == "" {
			src = siteOf(link)
		}
		out = append(out, SearchResult{Title: textutil.CollapseSpace(html.UnescapeString(it.Title)), URL: link,
			Snippet: htmlLine(it.Description), Date: rssDate(it.PubDate), Source: src})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

// searchGoogleNews reads Google News' feed (its links lead through Google).
func (e *Engine) searchGoogleNews(ctx context.Context, query string, opt SearchOptions, limit int) ([]SearchResult, error) {
	q := query
	if w := map[string]string{"day": "1d", "week": "7d", "month": "30d", "year": "1y"}[opt.Time]; w != "" {
		q += " when:" + w
	}
	v := url.Values{"q": {q}}
	if arabicRe.MatchString(query) {
		v.Set("hl", "fa")
		v.Set("gl", "IR")
		v.Set("ceid", "IR:fa")
	} else {
		v.Set("hl", "en-US")
		v.Set("gl", "US")
		v.Set("ceid", "US:en")
	}
	f, err := e.rss(ctx, e.gnewsURL+"?"+v.Encode())
	if err != nil {
		return nil, err
	}
	var out []SearchResult
	for _, it := range f.Items {
		title, src := textutil.CollapseSpace(html.UnescapeString(it.Title)), strings.TrimSpace(it.Source)
		if src != "" {
			title = strings.TrimSuffix(title, " - "+src)
		}
		if !strings.HasPrefix(it.Link, "http") {
			continue
		}
		out = append(out, SearchResult{Title: title, URL: it.Link, Date: rssDate(it.PubDate), Source: src})
		if len(out) == limit {
			break
		}
	}
	return out, nil
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func htmlLine(s string) string {
	return textutil.CollapseSpace(html.UnescapeString(tagRe.ReplaceAllString(html.UnescapeString(s), " ")))
}

// ---------------------------------------------------------------- quick answer

// quickAnswer looks for a short sourced answer: DuckDuckGo's instant
// answer, else (for a name of a few words) the Wikipedia article it names.
func (e *Engine) quickAnswer(ctx context.Context, query string) *QuickAnswer {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if e.iaURL != "" {
		if a := e.instantAnswer(ctx, query); a != nil {
			return a
		}
	}
	if e.wikiURL != nil && len(strings.Fields(query)) <= 5 {
		return e.wikiAnswer(ctx, query)
	}
	return nil
}

func (e *Engine) instantAnswer(ctx context.Context, query string) *QuickAnswer {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.iaURL+"?format=json&no_html=1&skip_disambig=1&q="+url.QueryEscape(query), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var d struct {
		Heading        string          `json:"Heading"`
		Answer         json.RawMessage `json:"Answer"`
		AbstractText   string          `json:"AbstractText"`
		AbstractURL    string          `json:"AbstractURL"`
		AbstractSource string          `json:"AbstractSource"`
		Definition     string          `json:"Definition"`
		DefinitionURL  string          `json:"DefinitionURL"`
		DefSource      string          `json:"DefinitionSource"`
		Infobox        json.RawMessage `json:"Infobox"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil {
		return nil
	}
	var answer string
	_ = json.Unmarshal(d.Answer, &answer)
	a := &QuickAnswer{Title: d.Heading}
	switch {
	case strings.TrimSpace(answer) != "":
		a.Text, a.Source = answer, "DuckDuckGo"
	case d.AbstractText != "":
		a.Text, a.URL, a.Source = d.AbstractText, d.AbstractURL, d.AbstractSource
	case d.Definition != "":
		a.Text, a.URL, a.Source = d.Definition, d.DefinitionURL, d.DefSource
	default:
		return nil
	}
	var box struct {
		Content []struct {
			Label string `json:"label"`
			Value any    `json:"value"`
		} `json:"content"`
	}
	if json.Unmarshal(d.Infobox, &box) == nil {
		for _, c := range box.Content {
			if v, ok := c.Value.(string); ok && c.Label != "" && strings.TrimSpace(v) != "" && len(a.Facts) < 8 {
				a.Facts = append(a.Facts, c.Label+": "+textutil.TruncateRunes(v, 80, "…"))
			}
		}
	}
	a.Text = textutil.TruncateRunes(a.Text, 700, "…")
	return a
}

// wikiAnswer is the opening of the Wikipedia article a short query names,
// in the query's language, when its title carries the query's words.
func (e *Engine) wikiAnswer(ctx context.Context, query string) *QuickAnswer {
	lang := "en"
	if arabicRe.MatchString(query) {
		lang = "fa"
	}
	api := e.wikiURL(lang) + "?action=query&format=json&formatversion=2&redirects=1&generator=search&gsrlimit=1" +
		"&prop=extracts|info&exintro=1&explaintext=1&exchars=700&inprop=url&gsrsearch=" + url.QueryEscape(query)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Waze-Agent/1.0 (+https://github.com/Metixpro/macron-agent)")
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var d struct {
		Query struct {
			Pages []struct {
				Title   string `json:"title"`
				Extract string `json:"extract"`
				FullURL string `json:"fullurl"`
			} `json:"pages"`
		} `json:"query"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d) != nil || len(d.Query.Pages) == 0 {
		return nil
	}
	p := d.Query.Pages[0]
	if strings.TrimSpace(p.Extract) == "" || !sharesTerm(p.Title, query) {
		return nil
	}
	return &QuickAnswer{Title: p.Title, Text: textutil.TruncateRunes(textutil.CollapseSpace(p.Extract), 700, "…"), URL: p.FullURL, Source: "Wikipedia"}
}

// sharesTerm: the title carries one of the query's words.
func sharesTerm(title, query string) bool {
	t := " " + strings.Join(queryTerms(title), " ") + " "
	for _, w := range queryTerms(query) {
		if strings.Contains(t, " "+w+" ") {
			return true
		}
	}
	return false
}
