package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	TregBaseURL = "https://treg.to"

	TregMaxCost = "0.01"
	tregTimeout = 20 * time.Second
)

type Treg struct {
	BaseURL string
	MaxCost string

	mu    sync.RWMutex
	token func() string

	hits, misses, fails atomic.Int64
	last                atomic.Value
}

type tregEvent struct {
	At       time.Time
	ServedBy string
	Err      string
}

type TregStatus struct {
	On                  bool
	Hits, Misses, Fails int64
	LastAt              time.Time
	LastServedBy        string
	LastErr             string
}

func NewTreg(token func() string) *Treg {
	return &Treg{BaseURL: TregBaseURL, MaxCost: TregMaxCost, token: token}
}

func (t *Treg) SetTokenSource(token func() string) {
	t.mu.Lock()
	t.token = token
	t.mu.Unlock()
}

func (t *Treg) Token() string {
	if t == nil {
		return ""
	}
	t.mu.RLock()
	f := t.token
	t.mu.RUnlock()
	if f == nil {
		return ""
	}
	return strings.TrimSpace(f())
}

func (t *Treg) Status() TregStatus {
	s := TregStatus{On: t.Token() != "", Hits: t.hits.Load(), Misses: t.misses.Load(), Fails: t.fails.Load()}
	if e, ok := t.last.Load().(tregEvent); ok {
		s.LastAt, s.LastServedBy, s.LastErr = e.At, e.ServedBy, e.Err
	}
	return s
}

type TregError struct {
	Status int
	Detail string
}

func (e *TregError) Error() string {
	return fmt.Sprintf("treg: HTTP %d: %s", e.Status, e.Detail)
}

func (e *TregError) Unauthorized() bool {
	return e.Status == http.StatusUnauthorized || e.Status == http.StatusForbidden
}

func (t *Treg) Search(ctx context.Context, client *http.Client, token, query string, limit int) ([]SearchResult, string, error) {
	ctx, cancel := context.WithTimeout(ctx, tregTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"q": query, "limit": limit})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(t.BaseURL, "/")+"/call/treg.web.search", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Treg-Token", token)
	req.Header.Set("X-Treg-Route-Prefer", "tinyfish")
	if t.MaxCost != "" {
		req.Header.Set("X-Treg-Route-Max-Cost", t.MaxCost)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, "", err
	}
	served := resp.Header.Get("X-Treg-Served-By")
	switch {
	case resp.StatusCode == http.StatusAccepted:
		return nil, served, nil
	case resp.StatusCode != http.StatusOK:
		return nil, served, &TregError{Status: resp.StatusCode, Detail: tregDetail(raw)}
	}
	var env struct {
		Output json.RawMessage `json:"output"`
		Treg   struct {
			ServedBy string `json:"served_by"`
		} `json:"_treg"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, served, fmt.Errorf("treg: unreadable answer: %w", err)
	}
	if env.Treg.ServedBy != "" {
		served = env.Treg.ServedBy
	}
	return tregRows(env.Output, limit), served, nil
}

func tregDetail(raw []byte) string {
	var e map[string]any
	if json.Unmarshal(raw, &e) == nil {
		for _, k := range []string{"detail", "error", "message"} {
			if v, ok := e[k]; ok {
				if s, ok := v.(string); ok {
					return s
				}
				b, _ := json.Marshal(v)
				return string(b)
			}
		}
	}
	return textutil.TruncateRunes(strings.TrimSpace(string(raw)), 200, "…")
}

func tregRows(output json.RawMessage, limit int) []SearchResult {
	var rows []map[string]any
	var obj struct {
		Results []map[string]any `json:"results"`
		Data    []map[string]any `json:"data"`
		Items   []map[string]any `json:"items"`
	}
	if json.Unmarshal(output, &obj) == nil {
		rows = append(append(append(rows, obj.Results...), obj.Data...), obj.Items...)
	}
	if len(rows) == 0 {
		_ = json.Unmarshal(output, &rows)
	}
	var out []SearchResult
	seen := map[string]bool{}
	for _, r := range rows {
		link := firstString(r, "url", "link", "href", "source_url", "sourceUrl")
		if link == "" || seen[link] || !(strings.HasPrefix(link, "http://") || strings.HasPrefix(link, "https://")) {
			continue
		}
		seen[link] = true
		title := firstString(r, "title", "name", "heading")
		if title == "" {
			title = link
		}
		snippet := firstString(r, "snippet", "description", "summary", "excerpt", "content", "text", "markdown", "body")
		if snippet == "" {
			if hs, ok := r["highlights"].([]any); ok {
				var parts []string
				for _, h := range hs {
					if s, ok := h.(string); ok {
						parts = append(parts, s)
					}
				}
				snippet = strings.Join(parts, " … ")
			}
		}
		out = append(out, SearchResult{Title: textutil.OneLine(title), URL: link,
			Snippet: textutil.TruncateRunes(textutil.OneLine(snippet), 500, "…")})
		if len(out) == limit {
			break
		}
	}
	return out
}

func firstString(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

var tregLogged atomic.Int64

func logTregFailure(err error) {
	now := time.Now().Unix()
	if last := tregLogged.Load(); now-last >= 60 && tregLogged.CompareAndSwap(last, now) {
		log.Printf("search: treg.to failed (%v); using DuckDuckGo", err)
	}
}

func (t *Treg) record(served string, n int, err error) {
	e := tregEvent{At: time.Now(), ServedBy: served}
	switch {
	case err != nil:
		t.fails.Add(1)
		e.Err = err.Error()
	case n == 0:
		t.misses.Add(1)
	default:
		t.hits.Add(1)
	}
	t.last.Store(e)
}
