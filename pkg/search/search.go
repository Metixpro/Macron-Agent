package search

import (
	"context"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	userAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
	maxPageBytes = 3 << 20
)

type SearchResult struct {
	Title    string   `json:"title"`
	URL      string   `json:"url"`
	Snippet  string   `json:"snippet"`
	Date     string   `json:"date,omitempty"`
	Source   string   `json:"source,omitempty"`
	Passages []string `json:"passages,omitempty"`
}

type MusicResult struct {
	Title   string   `json:"title"`
	Source  string   `json:"source"`
	MP3Urls []string `json:"mp3_urls"`
}

type Page struct {
	URL     string
	Title   string
	Text    string
	Media   []string
	HTML    bool
	Scripts int
	Via     string
}

type Engine struct {
	HTTPClient *http.Client
	Treg       *Treg

	ReaderURL string
	ReaderKey string
	Render    func(ctx context.Context, url string) (*Page, error)

	liteURL     string
	htmlURL     string
	bingURL     string
	bingNewsURL string
	gnewsURL    string
	iaURL       string
	wikiURL     func(lang string) string

	health engineHealth
	cache  searchCache
}

func NewEngine(client *http.Client) *Engine {
	if client == nil {
		client = netclient.NewClient(netclient.Options{Timeout: 25 * time.Second})
	}
	return &Engine{
		HTTPClient:  client,
		ReaderURL:   DefaultReaderURL,
		liteURL:     "https://lite.duckduckgo.com/lite/",
		htmlURL:     "https://html.duckduckgo.com/html/",
		bingURL:     "https://www.bing.com/search",
		bingNewsURL: "https://www.bing.com/news/search",
		gnewsURL:    "https://news.google.com/rss/search",
		iaURL:       "https://api.duckduckgo.com/",
		wikiURL:     func(lang string) string { return "https://" + lang + ".wikipedia.org/w/api.php" },
	}
}

func (e *Engine) Search(ctx context.Context, query string, limit int) ([]SearchResult, error) {
	rep, err := e.SearchWith(ctx, query, SearchOptions{Limit: limit})
	if rep == nil {
		return nil, err
	}
	return rep.Results, err
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

func parseLite(doc *html.Node, limit int) []SearchResult {
	var results []SearchResult
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit+1 {
			return
		}
		if n.Type == html.ElementNode {
			switch {
			case n.Data == "a" && hasClass(n, "result-link"):
				target := cleanDDGUrl(attr(n, "href"))
				if target != "" && !seen[target] && !strings.Contains(target, "duckduckgo.com") {
					seen[target] = true
					results = append(results, SearchResult{Title: extractText(n), URL: target})
				}
			case n.Data == "td" && hasClass(n, "result-snippet") && len(results) > 0:
				if last := &results[len(results)-1]; last.Snippet == "" {
					last.Snippet = extractText(n)
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	if len(results) > limit {
		results = results[:limit]
	}
	for i := range results {
		if results[i].Snippet == "" {
			results[i].Snippet = results[i].Title
		}
	}
	return results
}

func parseHTMLResults(doc *html.Node, limit int) []SearchResult {
	var results []SearchResult
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "div" && hasClass(n, "result__body") {
			var r SearchResult
			var inner func(*html.Node)
			inner = func(m *html.Node) {
				if m.Type == html.ElementNode && m.Data == "a" {
					switch {
					case hasClass(m, "result__a"):
						r.Title = extractText(m)
						if r.URL == "" {
							r.URL = cleanDDGUrl(attr(m, "href"))
						}
					case hasClass(m, "result__snippet"):
						r.Snippet = extractText(m)
					}
				}
				if m.Type == html.ElementNode && m.Data == "h2" && hasClass(m, "result__title") && r.Title == "" {
					r.Title = extractText(m)
				}
				for c := m.FirstChild; c != nil; c = c.NextSibling {
					inner(c)
				}
			}
			inner(n)
			if r.URL != "" && r.Title != "" {
				results = append(results, r)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return results
}

func cleanDDGUrl(raw string) string {
	raw = strings.TrimSpace(raw)
	if i := strings.Index(raw, "uddg="); i >= 0 {
		target := raw[i+len("uddg="):]
		if j := strings.IndexByte(target, '&'); j >= 0 {
			target = target[:j]
		}
		if decoded, err := url.QueryUnescape(target); err == nil {
			return decoded
		}
	}
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func extractText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
			sb.WriteByte(' ')
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return textutil.CollapseSpace(sb.String())
}

var mediaExts = map[string]bool{
	".mp3": true, ".m4a": true, ".wav": true, ".flac": true, ".ogg": true, ".aac": true,
	".mp4": true, ".mkv": true, ".webm": true, ".mov": true,
	".zip": true, ".rar": true, ".7z": true, ".pdf": true,
}

var audioExts = map[string]bool{".mp3": true, ".m4a": true, ".wav": true, ".flac": true, ".ogg": true, ".aac": true}

func (e *Engine) Fetch(ctx context.Context, rawURL string) (*Page, error) {
	u, err := netclient.ValidateURL(rawURL)
	if err != nil {
		return nil, err
	}
	return e.fetchWithFallback(ctx, u.String(), func() (*Page, error) { return e.fetchDirect(ctx, u) })
}

func (e *Engine) fetchDirect(ctx context.Context, u *url.URL) (*Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("create fetch request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,*/*;q=0.8")
	req.Header.Set("Accept-Language", "fa-IR,fa;q=0.9,en-US;q=0.8,en;q=0.7")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Sec-Ch-Ua", `"Chromium";v="124", "Google Chrome";v="124", "Not-A.Brand";v="99"`)
	req.Header.Set("Sec-Ch-Ua-Mobile", "?0")
	req.Header.Set("Sec-Ch-Ua-Platform", `"Windows"`)

	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch page: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}

	contentType := resp.Header.Get("Content-Type")
	mediaType, _, _ := mime.ParseMediaType(contentType)
	finalURL := u
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL
	}
	page := &Page{URL: finalURL.String()}

	body := io.LimitReader(resp.Body, maxPageBytes)
	switch {
	case mediaType == "" || mediaType == "text/html" || mediaType == "application/xhtml+xml":
		reader, err := charset.NewReader(body, contentType)
		if err != nil {
			return nil, fmt.Errorf("decode page charset: %w", err)
		}
		doc, err := html.Parse(reader)
		if err != nil {
			return nil, fmt.Errorf("parse html: %w", err)
		}
		page.Title = findTitle(doc)
		page.Media = collectMedia(doc, finalURL)
		page.Text = readableText(doc)
		page.HTML, page.Scripts = true, countScripts(doc)
	case strings.HasPrefix(mediaType, "text/") || strings.HasSuffix(mediaType, "json") || strings.HasSuffix(mediaType, "xml"):
		reader, err := charset.NewReader(body, contentType)
		if err != nil {
			reader = body
		}
		b, err := io.ReadAll(reader)
		if err != nil {
			return nil, fmt.Errorf("read body: %w", err)
		}
		page.Text = strings.ToValidUTF8(string(b), "")
	default:
		return nil, fmt.Errorf("%w: unsupported content type %q — use download_file to save it into the workspace", errNotPage, mediaType)
	}
	return page, nil
}

func (e *Engine) FetchPage(ctx context.Context, rawURL string, maxChars int) (string, error) {
	if maxChars <= 0 {
		maxChars = 8000
	}
	page, err := e.Fetch(ctx, rawURL)
	if err != nil {
		return "", err
	}
	var sb strings.Builder
	if page.Title != "" {
		sb.WriteString("Title: " + page.Title + "\n")
	}
	sb.WriteString("URL: " + page.URL + "\n")
	if page.Via != "" {
		sb.WriteString("(The site blocked a plain request; the page was read through a " + page.Via + ".)\n")
	}
	sb.WriteString("\n")
	if len(page.Media) > 0 {
		sb.WriteString("=== DIRECT MEDIA & DOWNLOAD LINKS FOUND ON THIS PAGE ===\n")
		for _, m := range page.Media {
			sb.WriteString("• " + m + "\n")
		}
		sb.WriteString("========================================================\n\n")
	}
	sb.WriteString(page.Text)
	return textutil.TruncateRunes(sb.String(), maxChars, "\n... [truncated]"), nil
}

func countScripts(n *html.Node) int {
	c := 0
	if n.Type == html.ElementNode && n.Data == "script" {
		c++
	}
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		c += countScripts(ch)
	}
	return c
}

func findTitle(doc *html.Node) string {
	var title string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if title != "" {
			return
		}
		if n.Type == html.ElementNode && n.Data == "title" {
			title = extractText(n)
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return textutil.TruncateRunes(title, 200, "…")
}

func collectMedia(doc *html.Node, base *url.URL) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(out) >= 25 {
			return
		}
		if n.Type == html.ElementNode {
			var ref string
			switch n.Data {
			case "a":
				ref = attr(n, "href")
			case "audio", "source", "video":
				ref = attr(n, "src")
			}
			if ref = strings.TrimSpace(ref); ref != "" {
				if u, err := base.Parse(ref); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					if mediaExts[strings.ToLower(path.Ext(u.Path))] && !seen[u.String()] {
						seen[u.String()] = true
						out = append(out, u.String())
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return out
}

var (
	skipTags = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "nav": true,
		"footer": true, "iframe": true, "head": true, "template": true, "form": true, "button": true, "select": true}
	blockTags = map[string]bool{"p": true, "div": true, "br": true, "li": true, "tr": true, "ul": true, "ol": true,
		"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "section": true, "article": true,
		"table": true, "blockquote": true, "pre": true, "header": true, "main": true, "aside": true, "hr": true, "dd": true, "dt": true}
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
)

func renderReadable(doc *html.Node) string { return render(doc, false) }

func (e *Engine) SearchPersianMusic(ctx context.Context, query string) ([]MusicResult, error) {
	results, err := e.Search(ctx, "دانلود آهنگ "+query, 5)
	if err != nil {
		return nil, err
	}

	found := make([]*MusicResult, len(results))
	var wg sync.WaitGroup
	for i, res := range results {
		wg.Add(1)
		go func(i int, res SearchResult) {
			defer wg.Done()
			defer guard.Recover("reading a music page")
			page, err := e.Fetch(ctx, res.URL)
			if err != nil {
				return
			}
			var urls []string
			for _, m := range page.Media {
				if u, err := url.Parse(m); err == nil && audioExts[strings.ToLower(path.Ext(u.Path))] {
					urls = append(urls, m)
					if len(urls) >= 6 {
						break
					}
				}
			}
			if len(urls) > 0 {
				found[i] = &MusicResult{Title: res.Title, Source: res.URL, MP3Urls: urls}
			}
		}(i, res)
	}
	wg.Wait()

	var list []MusicResult
	for _, f := range found {
		if f != nil {
			list = append(list, *f)
		}
	}
	return list, nil
}

func (e *Engine) SetEndpoints(lite, html string) {
	e.liteURL, e.htmlURL = lite, html
	e.bingURL, e.bingNewsURL, e.gnewsURL, e.iaURL, e.wikiURL = "", "", "", "", nil
}

func (e *Engine) SetEngines(bing, bingNews, googleNews, instant, wiki string) {
	e.bingURL, e.bingNewsURL, e.gnewsURL, e.iaURL = bing, bingNews, googleNews, instant
	e.wikiURL = nil
	if wiki != "" {
		e.wikiURL = func(lang string) string { return wiki + "/" + lang }
	}
}

func HTMLText(src string) (title, text string) {
	doc, err := html.Parse(strings.NewReader(src))
	if err != nil {
		return "", ""
	}
	return findTitle(doc), renderReadable(doc)
}
