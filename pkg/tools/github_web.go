package tools

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

var (
	errGitHubLimit = errors.New("GitHub API rate limit reached — try again later")
	githubAPIURL   = "https://api.github.com"
	githubWebURL   = "https://github.com"
	githubRawURL   = "https://raw.githubusercontent.com"
)

func (r *Registry) githubWeb(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", browserUA)
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("repository or resource not found (it may be private)")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub answered HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

func (r *Registry) githubRawReadme(ctx context.Context, repo string) (string, error) {
	var last error
	for _, name := range []string{"README.md", "readme.md", "Readme.md", "README.rst", "README.txt", "README"} {
		b, err := r.githubWeb(ctx, githubRawURL+"/"+repo+"/HEAD/"+name, 2<<20)
		if err == nil {
			return strings.ToValidUTF8(string(b), ""), nil
		}
		last = err
	}
	return "", last
}

type atomFeed struct {
	Entries []struct {
		ID      string `xml:"id"`
		Title   string `xml:"title"`
		Updated string `xml:"updated"`
		Link    struct {
			Href string `xml:"href,attr"`
		} `xml:"link"`
		Content string `xml:"content"`
		Author  struct {
			Name string `xml:"name"`
		} `xml:"author"`
	} `xml:"entry"`
}

var tagRe = regexp.MustCompile(`<[^>]*>`)

func htmlToLine(s string) string {
	return textutil.CollapseSpace(html.UnescapeString(tagRe.ReplaceAllString(s, " ")))
}

func (r *Registry) githubFeed(ctx context.Context, repo, kind string) (string, error) {
	b, err := r.githubWeb(ctx, githubWebURL+"/"+repo+"/"+kind+".atom", 2<<20)
	if err != nil {
		return "", err
	}
	var feed atomFeed
	if err := xml.Unmarshal(b, &feed); err != nil {
		return "", errors.New("GitHub returned an unexpected feed")
	}
	if len(feed.Entries) == 0 {
		if kind == "releases" {
			return fmt.Sprintf("%s has no releases.", repo), nil
		}
		return "No commits found.", nil
	}
	var sb strings.Builder
	for i, e := range feed.Entries {
		if i == 10 || (kind == "releases" && i == 5) {
			break
		}
		title := textutil.CollapseSpace(e.Title)
		date := textutil.TruncateRunes(e.Updated, 10, "")
		if kind == "releases" {
			fmt.Fprintf(&sb, "🏷️ %s — %s\n%s\n%s\n\n", title, date, e.Link.Href, textutil.TruncateRunes(htmlToLine(e.Content), 400, "…"))
			continue
		}
		sha := e.ID[strings.LastIndex(e.ID, "/")+1:]
		fmt.Fprintf(&sb, "• %s %s — %s (%s)\n", textutil.TruncateBytes(sha, 7, ""), textutil.TruncateRunes(title, 100, "…"), e.Author.Name, date)
	}
	return sb.String() + "(read from GitHub's public feed: the API is busy)", nil
}

var (
	ghDescRe     = regexp.MustCompile(`<meta property="og:description" content="([^"]*)"`)
	ghStarsRe    = regexp.MustCompile(`<span[^>]*id="repo-stars-counter-star"[^>]*>`)
	ghForksRe    = regexp.MustCompile(`<span[^>]*id="repo-network-counter"[^>]*>`)
	ghTitleAttr  = regexp.MustCompile(`title="([\d,]+)"`)
	ghLangRe     = regexp.MustCompile(`<span class="color-fg-default text-bold mr-1">([^<]+)</span>\s*<span>([\d.]+%)</span>`)
	ghTopicRe    = regexp.MustCompile(`(?s)class="topic-tag topic-tag-link[^"]*"[^>]*>\s*([^<]+?)\s*</a>`)
	ghArchivedRe = regexp.MustCompile(`This repository has been archived`)
)

func (r *Registry) githubPage(ctx context.Context, repo string) (string, error) {
	b, err := r.githubWeb(ctx, githubWebURL+"/"+repo, 4<<20)
	if err != nil {
		return "", err
	}
	page := string(b)
	count := func(re *regexp.Regexp) string {
		if tag := re.FindString(page); tag != "" {
			if m := ghTitleAttr.FindStringSubmatch(tag); m != nil {
				return m[1]
			}
		}
		return "?"
	}
	desc := ""
	if m := ghDescRe.FindStringSubmatch(page); m != nil {
		desc = strings.TrimSuffix(html.UnescapeString(m[1]), " - "+repo)
		if strings.HasPrefix(desc, "Contribute to "+repo) {
			desc = ""
		}
	}
	var langs []string
	for _, m := range ghLangRe.FindAllStringSubmatch(page, 4) {
		langs = append(langs, m[1]+" "+m[2])
	}
	var topics []string
	for _, m := range ghTopicRe.FindAllStringSubmatch(page, 12) {
		topics = append(topics, html.UnescapeString(m[1]))
	}
	archived := ""
	if ghArchivedRe.MatchString(page) {
		archived = " (archived)"
	}
	return fmt.Sprintf("🐙 %s%s\n• URL: %s/%s\n• Description: %s\n• Languages: %s | ⭐ %s | Forks: %s\n• Topics: %s\n(read from the repository's page: the GitHub API is busy)",
		repo, archived, githubWebURL, repo, desc, strings.Join(langs, ", "), count(ghStarsRe), count(ghForksRe), strings.Join(topics, ", ")), nil
}
