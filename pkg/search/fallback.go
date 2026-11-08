package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Reading pages that block bots. A plain request goes first; when the site
// refuses it (403, 429, 503…), answers with a challenge page ("Just a
// moment…") or with an almost empty page, the page is read again, quietly,
// through a reader service (r.jina.ai), treg.to's page reader (with a token)
// and a headless browser, in that order. Private and internal addresses are
// refused before any of this and never reach a fallback.

// DefaultReaderURL is the reader service: the page URL is appended to it.
const DefaultReaderURL = "https://r.jina.ai/"

const (
	thinRunes       = 200 // a page with less readable text than this is "empty"
	readerTimeout   = 30 * time.Second
	renderTimeout   = 75 * time.Second
	fallbackMaxText = 60000
)

// StatusError is a page that answered with an HTTP error.
type StatusError struct{ Code int }

func (e *StatusError) Error() string { return fmt.Sprintf("fetch returned status: %d", e.Code) }

// errNotPage is a URL that is a file, not a page: no fallback can help.
var errNotPage = errors.New("not a web page")

// challengeMarks are the words of anti-bot pages.
var challengeMarks = []string{
	"just a moment", "attention required", "checking your browser", "enable javascript and cookies", "verify you are human",
	"are you a robot", "ddos-guard", "cf-chl", "challenge-platform", "please enable javascript", "access denied", "captcha",
	"please wait while we verify", "one more step",
}

// blockedPage reports a challenge page, or an almost empty page built by
// scripts (it needs a browser to show its text).
func blockedPage(p *Page) bool {
	if p == nil || !p.HTML {
		return false
	}
	text := strings.TrimSpace(p.Text)
	if len([]rune(text)) < thinRunes && p.Scripts > 0 {
		return true
	}
	if len([]rune(text)) > 3000 {
		return false // a real page that happens to mention a captcha
	}
	head := strings.ToLower(p.Title + " " + textutil.TruncateRunes(text, 600, ""))
	for _, m := range challengeMarks {
		if strings.Contains(head, m) {
			return true
		}
	}
	return false
}

// worthRetrying reports a failed plain request that another way may get past.
func worthRetrying(err error) bool {
	if err == nil || errors.Is(err, errNotPage) ||
		errors.Is(err, context.Canceled) {
		return false
	}
	var se *StatusError
	if errors.As(err, &se) {
		switch c := se.Code; {
		case c == 401, c == 403, c == 406, c == 409, c == 418, c == 429, c == 451, c >= 500:
			return true
		}
		return false // 404, 410…: the page is not there
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true // timeouts, resets: often a firewall that drops bots
	}
	return strings.Contains(err.Error(), "tls") || strings.Contains(err.Error(), "EOF") || strings.Contains(err.Error(), "reset")
}

// pageReader is one way to read a page.
type pageReader struct {
	name string
	read func(ctx context.Context, url string) (*Page, error)
}

// readers are the fallbacks, in order; those not set up are left out.
func (e *Engine) readers() []pageReader {
	var out []pageReader
	if e.ReaderURL != "" {
		out = append(out, pageReader{"reader", e.readViaReader})
	}
	if e.Treg.Token() != "" {
		out = append(out, pageReader{"treg", e.readViaTreg})
	}
	if e.Render != nil {
		out = append(out, pageReader{"browser", func(ctx context.Context, url string) (*Page, error) {
			ctx, cancel := context.WithTimeout(ctx, renderTimeout)
			defer cancel()
			return e.Render(ctx, url)
		}})
	}
	return out
}

// fetchWithFallback reads u directly, then through the readers when blocked.
func (e *Engine) fetchWithFallback(ctx context.Context, u string, direct func() (*Page, error)) (*Page, error) {
	page, err := direct()
	if err == nil && !blockedPage(page) {
		return page, nil
	}
	if err != nil && !worthRetrying(err) {
		return nil, err
	}
	why := "an almost empty or challenge page"
	if err != nil {
		why = err.Error()
	}
	for _, r := range e.readers() {
		if ctx.Err() != nil {
			break
		}
		p, ferr := r.read(ctx, u)
		if ferr == nil && p != nil && len([]rune(strings.TrimSpace(p.Text))) >= thinRunes && !blockedPage(&Page{HTML: true, Scripts: 1, Title: p.Title, Text: p.Text}) {
			p.Via = r.name
			if p.URL == "" {
				p.URL = u
			}
			if page != nil && len(p.Media) == 0 {
				p.Media = page.Media
			}
			log.Printf("search: %s was read through the %s (%s)", textutil.TruncateRunes(u, 80, "…"), r.name, why)
			return p, nil
		}
	}
	if page != nil {
		return page, nil // all we have: a short page
	}
	return nil, err
}

// ---------------------------------------------------------------- reader service

func (e *Engine) readViaReader(ctx context.Context, u string) (*Page, error) {
	ctx, cancel := context.WithTimeout(ctx, readerTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.ReaderURL+u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/plain")
	req.Header.Set("X-Timeout", "25")
	if e.ReaderKey != "" {
		req.Header.Set("Authorization", "Bearer "+e.ReaderKey)
	}
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Code: resp.StatusCode}
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		return nil, err
	}
	return parseReader(string(raw)), nil
}

// parseReader reads the reader's answer: "Title: …", "URL Source: …", then
// "Markdown Content:" and the page.
func parseReader(s string) *Page {
	p := &Page{}
	head, body, found := strings.Cut(s, "Markdown Content:")
	if !found {
		head, body = "", s
	}
	for _, line := range strings.Split(head, "\n") {
		switch k, v, _ := strings.Cut(line, ":"); strings.TrimSpace(k) {
		case "Title":
			p.Title = strings.TrimSpace(v)
		case "URL Source":
			p.URL = strings.TrimSpace(v)
		}
	}
	p.Text = textutil.TruncateRunes(strings.TrimSpace(body), fallbackMaxText, "")
	return p
}

// ---------------------------------------------------------------- treg.to

func (e *Engine) readViaTreg(ctx context.Context, u string) (*Page, error) {
	t := e.Treg
	ctx, cancel := context.WithTimeout(ctx, readerTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"url": u})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/call/treg.web.extract", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Treg-Token", t.Token())
	req.Header.Set("X-Treg-Route-Prefer", "tinyfish") // free
	if t.MaxCost != "" {
		req.Header.Set("X-Treg-Route-Max-Cost", t.MaxCost)
	}
	resp, err := e.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &TregError{Status: resp.StatusCode, Detail: tregDetail(raw)}
	}
	var env struct {
		Output any `json:"output"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	p := &Page{}
	p.Text = longestText(env.Output, 3)
	if m, ok := env.Output.(map[string]any); ok {
		p.Title = firstString(m, "title", "name")
		p.URL = firstString(m, "url", "source_url")
	}
	p.Text = textutil.TruncateRunes(p.Text, fallbackMaxText, "")
	return p, nil
}

// longestText finds the page text in a vendor's answer: its longest string
// field (markdown, content, text…), looking a few levels deep.
func longestText(v any, depth int) string {
	best := ""
	switch x := v.(type) {
	case string:
		return x
	case map[string]any:
		if depth == 0 {
			return ""
		}
		for k, val := range x {
			if k == "url" || k == "html" || strings.HasSuffix(k, "_url") {
				continue
			}
			if s := longestText(val, depth-1); len(s) > len(best) {
				best = s
			}
		}
	case []any:
		if depth == 0 {
			return ""
		}
		for _, val := range x {
			if s := longestText(val, depth-1); len(s) > len(best) {
				best = s
			}
		}
	}
	return best
}
