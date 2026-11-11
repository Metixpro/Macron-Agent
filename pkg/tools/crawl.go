package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"golang.org/x/net/html"

	"github.com/Metixpro/macron-agent/pkg/netclient"
)

const ToolCrawlSite = "crawl_site"

type CrawlResult struct {
	URL       string
	Path      string
	Title     string
	WordCount int
}

var skipExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".svg": true, ".ico": true,
	".css": true, ".js": true, ".zip": true, ".gz": true, ".tar": true, ".mp4": true, ".mp3": true, ".pdf": true, ".woff": true,
	".woff2": true, ".ttf": true, ".exe": true, ".dmg": true, ".apk": true, ".xml": true, ".json": true}

func (r *Registry) pageLinks(ctx context.Context, page *url.URL) []*url.URL {
	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, page.String(), nil)
	if err != nil {
		return nil
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; MacronAgent)")
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(resp.Header.Get("Content-Type"), "html") {
		return nil
	}
	base := resp.Request.URL
	var out []*url.URL
	z := html.NewTokenizer(io.LimitReader(resp.Body, 4<<20))
	for {
		switch z.Next() {
		case html.ErrorToken:
			return out
		case html.StartTagToken, html.SelfClosingTagToken:
			name, more := z.TagName()
			if string(name) == "base" {
				for more {
					var k, v []byte
					k, v, more = z.TagAttr()
					if string(k) == "href" {
						if u, err := base.Parse(string(v)); err == nil {
							base = u
						}
					}
				}
				continue
			}
			if string(name) != "a" {
				continue
			}
			for more {
				var k, v []byte
				k, v, more = z.TagAttr()
				if string(k) != "href" {
					continue
				}
				if u, err := base.Parse(strings.TrimSpace(string(v))); err == nil {
					u.Fragment = ""
					out = append(out, u)
				}
			}
		}
	}
}

func crawlKey(u *url.URL) string {
	k := *u
	k.Fragment = ""
	k.Host = strings.ToLower(strings.TrimPrefix(k.Host, "www."))
	k.Scheme = ""
	k.Path = strings.TrimRight(k.Path, "/")
	return k.String()
}

func (r *Registry) CrawlToKnowledgeBase(ctx context.Context, sessionID, startURL, destFolder string, maxPages int) ([]CrawlResult, error) {
	maxPages = min(max(maxPages, 1), 50)
	if destFolder = strings.Trim(strings.TrimSpace(destFolder), "/"); destFolder == "" {
		destFolder = "knowledge_base"
	}
	start, err := netclient.ValidateURL(startURL)
	if err != nil {
		return nil, err
	}
	host := strings.ToLower(strings.TrimPrefix(start.Host, "www."))
	prefix := start.Path
	if !strings.HasSuffix(prefix, "/") {
		prefix = path.Dir(prefix)
	}
	if prefix = strings.TrimRight(prefix, "/.") + "/"; !strings.HasPrefix(prefix, "/") {
		prefix = "/"
	}
	dir := destFolder + "/" + sanitizeBase(host)
	queue, seen := []*url.URL{start}, map[string]bool{crawlKey(start): true}
	names := map[string]bool{}
	var results []CrawlResult
	deadline := time.Now().Add(8 * time.Minute)
	for len(queue) > 0 && len(results) < maxPages && ctx.Err() == nil && time.Now().Before(deadline) {
		cur := queue[0]
		queue = queue[1:]
		pctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		text, err := r.Search.FetchPage(pctx, cur.String(), 20000)
		var links []*url.URL
		if err == nil {
			links = r.pageLinks(pctx, cur)
		}
		cancel()
		if err != nil || len(strings.TrimSpace(text)) < 80 {
			continue
		}
		title := cur.String()
		if line, _, _ := strings.Cut(text, "\n"); strings.HasPrefix(line, "Title: ") {
			title = strings.TrimSpace(strings.TrimPrefix(line, "Title: "))
		}
		slug := sanitizeBase(strings.Trim(cur.Path, "/"))
		if slug == "" {
			slug = "index"
		}
		slug = string([]rune(slug)[:min(len([]rune(slug)), 60)])
		name := slug
		for i := 2; names[name]; i++ {
			name = fmt.Sprintf("%s-%d", slug, i)
		}
		names[name] = true
		file := dir + "/" + name + ".md"
		body := fmt.Sprintf("---\nurl: %s\ntitle: %s\ncrawled: %s\n---\n\n%s\n", cur, title, time.Now().UTC().Format(time.RFC3339), text)
		if err := r.Machine.WriteFile(sessionID, file, []byte(body)); err != nil {
			return results, err
		}
		results = append(results, CrawlResult{URL: cur.String(), Path: file, Title: title, WordCount: len(strings.Fields(text))})
		for _, l := range links {
			if (l.Scheme != "http" && l.Scheme != "https") || strings.ToLower(strings.TrimPrefix(l.Host, "www.")) != host ||
				!strings.HasPrefix(l.Path+"/", prefix) || skipExt[strings.ToLower(path.Ext(l.Path))] {
				continue
			}
			if k := crawlKey(l); !seen[k] {
				seen[k] = true
				queue = append(queue, l)
			}
		}
	}
	return results, nil
}
