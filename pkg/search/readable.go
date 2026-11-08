package search

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/html"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

var boilerRe = regexp.MustCompile(`(?i)(?:^|[\s_-])(?:nav|navbar|navigation|menu|menubar|breadcrumbs?|footer|sidebar|side-bar|cookies?|consent|` +
	`share|sharing|social|comments?|advert|adverts|ads|adsbox|newsletter|popup|modal|subscribe|signup|` +
	`langlinks|interlanguage|language-list|lang-list|toc|mw-jump-link|catlinks|navbox|sitenotice|mw-panel|printfooter|` +
	`mw-editsection|reference|noprint|` +
	`vector-header|vector-menu|vector-page-toolbar|skip-link|skiplink|screen-reader-text|visually-hidden|sr-only)(?:$|[\s_-])`)

var mainIDRe = regexp.MustCompile(`(?i)^(?:mw-content-text|content|main-content|maincontent|main|article-body|articlebody|article-content|` +
	`post-content|entry-content|story-body|news-content|newsbody|news-text|item-text|body-text)$`)

var frameTags = map[string]bool{"nav": true, "footer": true, "aside": true, "menu": true, "dialog": true}

func framed(n *html.Node) bool {
	if frameTags[n.Data] {
		return true
	}
	if _, ok := attrOK(n, "hidden"); ok || attr(n, "aria-hidden") == "true" {
		return true
	}
	if style := strings.ReplaceAll(strings.ToLower(attr(n, "style")), " ", ""); strings.Contains(style, "display:none") || strings.Contains(style, "visibility:hidden") {
		return true
	}
	switch attr(n, "role") {
	case "navigation", "banner", "contentinfo", "complementary", "search", "dialog", "menu", "menubar":
		return true
	}
	if n.Data == "header" && !inside(n, "article", "main") {
		return true
	}
	if n.Data == "body" || n.Data == "main" || n.Data == "article" || n.Data == "html" {
		return false
	}
	return boilerRe.MatchString(attr(n, "class")) || boilerRe.MatchString(attr(n, "id"))
}

func rawText(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		if c.Type == html.TextNode {
			sb.WriteString(c.Data)
		}
		if c.Type == html.ElementNode && c.Data == "br" {
			sb.WriteByte('\n')
		}
		for x := c.FirstChild; x != nil; x = x.NextSibling {
			walk(x)
		}
	}
	walk(n)
	return sb.String()
}

func attrOK(n *html.Node, key string) (string, bool) {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val, true
		}
	}
	return "", false
}

func inside(n *html.Node, tags ...string) bool {
	for p := n.Parent; p != nil; p = p.Parent {
		for _, t := range tags {
			if p.Type == html.ElementNode && p.Data == t {
				return true
			}
		}
	}
	return false
}

func readableText(doc *html.Node) string {
	all := render(doc, true)
	total := utf8.RuneCountInString(all)
	if total < 400 {
		return all
	}
	if n := mainNode(doc); n != nil {
		if text := render(n, true); utf8.RuneCountInString(text)*4 >= total {
			return text
		}
	}
	if n := densestNode(doc); n != nil {
		if text := render(n, true); utf8.RuneCountInString(text)*5 >= total*2 {
			return text
		}
	}
	return all
}

func mainNode(doc *html.Node) *html.Node {
	var mains, articles, ids []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipTags[n.Data] || framed(n) {
				return
			}
			switch {
			case n.Data == "main" || attr(n, "role") == "main":
				mains = append(mains, n)
			case n.Data == "article":
				articles = append(articles, n)
			case mainIDRe.MatchString(attr(n, "id")):
				ids = append(ids, n)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	switch {
	case len(ids) > 0 && ids[0].Parent != nil && attr(ids[0], "id") == "mw-content-text":
		return ids[0]
	case len(articles) == 1:
		return articles[0]
	case len(mains) == 1:
		return mains[0]
	case len(ids) == 1:
		return ids[0]
	}
	return nil
}

func densestNode(doc *html.Node) *html.Node {
	score := map[*html.Node]int{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if skipTags[n.Data] || framed(n) {
				return
			}
			if n.Data == "p" {
				if l := utf8.RuneCountInString(extractText(n)); l >= 25 && n.Parent != nil {
					score[n.Parent] += l
					if n.Parent.Parent != nil {
						score[n.Parent.Parent] += l / 2
					}
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	var best *html.Node
	for n, s := range score {
		if best == nil || s > score[best] {
			best = n
		}
	}
	if best == nil || score[best] < 300 {
		return nil
	}
	return best
}

func render(root *html.Node, noFrame bool) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			if t := textutil.CollapseSpace(n.Data); t != "" {
				sb.WriteString(t)
				sb.WriteByte(' ')
			}
			return
		case html.ElementNode:
			if skipTags[n.Data] || (noFrame && n != root && framed(n)) {
				return
			}
			block := blockTags[n.Data]
			if block {
				sb.WriteByte('\n')
			}
			if n.Data == "pre" {
				code := strings.Trim(rawText(n), "\n")
				if code != "" {
					sb.WriteString("\n```\n" + strings.ReplaceAll(code, "\n\n\n", "\n\n") + "\n```\n")
				}
				return
			}
			switch n.Data {
			case "li":
				sb.WriteString("• ")
			case "h1":
				sb.WriteString("# ")
			case "h2":
				sb.WriteString("## ")
			case "h3", "h4":
				sb.WriteString("### ")
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				walk(c)
			}
			switch {
			case n.Data == "td" || n.Data == "th":
				sb.WriteString("| ")
			case block:
				sb.WriteByte('\n')
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)

	lines := strings.Split(sb.String(), "\n")
	out := lines[:0]
	code := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "```" {
			code = !code
		}
		if code {
			out = append(out, strings.TrimRight(l, " \t"))
			continue
		}
		l = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(l), "|"))
		if strings.Trim(l, "|#• ") == "" && l != "" {
			continue
		}
		out = append(out, l)
	}
	return strings.TrimSpace(blankLinesRe.ReplaceAllString(strings.Join(out, "\n"), "\n\n"))
}
