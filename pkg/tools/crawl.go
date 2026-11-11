package tools

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const ToolCrawlSite = "crawl_site"

var hrefRegex = regexp.MustCompile(`(?i)href=["']([^"']+)["']`)

type CrawlResult struct {
	URL        string `json:"url"`
	Path       string `json:"path"`
	Title      string `json:"title"`
	WordCount  int    `json:"word_count"`
	StatusCode int    `json:"status_code"`
}

// CrawlToKnowledgeBase crawls up to maxPages within the same domain and writes markdown into workspace.
func (r *Registry) CrawlToKnowledgeBase(ctx context.Context, sessionID, startURL, destFolder string, maxPages int) ([]CrawlResult, error) {
	if maxPages <= 0 {
		maxPages = 15
	}
	maxPages = min(maxPages, 50)
	if destFolder == "" {
		destFolder = "knowledge_base"
	}

	parsedStart, err := url.Parse(startURL)
	if err != nil || parsedStart.Host == "" {
		return nil, fmt.Errorf("invalid start url: %s", startURL)
	}
	baseHost := strings.ToLower(parsedStart.Host)

	visited := make(map[string]bool)
	queue := []string{startURL}
	var results []CrawlResult

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	folderSlug := sanitizeBase(baseHost)
	targetDir := destFolder + "/" + folderSlug

	for len(queue) > 0 && len(results) < maxPages {
		if ctx.Err() != nil {
			break
		}
		currentURL := queue[0]
		queue = queue[1:]

		normURL := normalizeURL(currentURL)
		if visited[normURL] {
			continue
		}
		visited[normURL] = true

		sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		text, err := r.Search.FetchPage(sctx, currentURL, 6000)
		cancel()
		if err != nil || len(text) < 100 {
			continue
		}

		title := extractTitle(text, currentURL)
		pageSlug := sanitizeBase(title)
		if len(pageSlug) > 40 {
			pageSlug = pageSlug[:40]
		}
		if pageSlug == "" {
			pageSlug = fmt.Sprintf("page_%03d", len(results)+1)
		}

		filePath := fmt.Sprintf("%s/%s.md", targetDir, pageSlug)
		content := fmt.Sprintf("---\nurl: %s\ntitle: %s\ncrawled_at: %s\n---\n\n# %s\n\n%s\n",
			currentURL, title, time.Now().UTC().Format(time.RFC3339), title, text)

		if err := r.Machine.WriteFile(sessionID, filePath, []byte(content)); err != nil {
			continue
		}

		words := len(strings.Fields(text))
		results = append(results, CrawlResult{
			URL:       currentURL,
			Path:      filePath,
			Title:     title,
			WordCount: words,
		})

		// Extract internal links
		for _, m := range hrefRegex.FindAllStringSubmatch(text, -1) {
			rawLink := m[1]
			if u, err := url.Parse(rawLink); err == nil {
				resolved := parsedStart.ResolveReference(u)
				if strings.ToLower(resolved.Host) == baseHost && (resolved.Scheme == "http" || resolved.Scheme == "https") {
					resolved.Fragment = "" // strip anchor
					linkStr := resolved.String()
					if !visited[normalizeURL(linkStr)] {
						queue = append(queue, linkStr)
					}
				}
			}
		}
	}

	return results, nil
}

func normalizeURL(u string) string {
	u = strings.TrimRight(u, "/")
	if i := strings.Index(u, "#"); i >= 0 {
		u = u[:i]
	}
	return strings.ToLower(u)
}

func extractTitle(content, fallback string) string {
	lines := strings.Split(content, "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "# ") {
			return strings.TrimSpace(strings.TrimPrefix(l, "# "))
		}
	}
	return textutil.OneLine(fallback)
}
