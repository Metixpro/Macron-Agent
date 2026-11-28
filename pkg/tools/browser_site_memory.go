package tools

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// SiteMemory stores a short, domain-scoped operational note that Macron
// automatically injects on the first browser snapshot of that host per turn.
type SiteMemory struct {
	ID        int    `json:"id"`
	Host      string `json:"host"`
	Text      string `json:"text"`
	UpdatedAt int64  `json:"updatedAt"`
}

type siteMemoryStore struct {
	mu      sync.Mutex
	path    string
	loaded  bool
	nextID  int
	entries []SiteMemory
	// turnSeen tracks which (sessionKey, host) pairs already had their site
	// memories injected during the current turn window.
	turnSeen map[string]map[string]time.Time
}

const (
	maxSiteMemoriesInject = 5
	maxSiteMemoryChars    = 320
	siteTurnInjectTTL     = 90 * time.Second
)

func defaultMacronBrowserConfigDir() string {
	if custom := strings.TrimSpace(os.Getenv("MACRON_HOME")); custom != "" {
		return custom
	}
	if dir, err := os.UserConfigDir(); err == nil && dir != "" {
		return filepath.Join(dir, "macron")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".macron")
	}
	return filepath.Join(os.TempDir(), "macron")
}

func (r *Registry) siteMem() *siteMemoryStore {
	r.siteMemOnce.Do(func() {
		base := r.BrowserDataDir
		if base == "" {
			base = defaultMacronBrowserConfigDir()
		}
		r.siteMemories = &siteMemoryStore{
			path:     filepath.Join(base, "site_memories.json"),
			nextID:   1,
			turnSeen: map[string]map[string]time.Time{},
		}
	})
	return r.siteMemories
}

func normalizeSiteHost(raw string) string {
	s := strings.TrimSpace(strings.ToLower(raw))
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			s = u.Hostname()
		}
	} else if strings.Contains(s, "/") {
		if u, err := url.Parse("https://" + s); err == nil {
			s = u.Hostname()
		}
	} else if idx := strings.IndexByte(s, ':'); idx > 0 {
		s = s[:idx]
	}
	s = strings.TrimPrefix(strings.TrimSuffix(s, "."), "www.")
	return strings.ToLower(strings.TrimSpace(s))
}

func (s *siteMemoryStore) loadLocked() {
	if s.loaded {
		return
	}
	s.loaded = true
	raw, err := os.ReadFile(s.path)
	if err != nil {
		return
	}
	var list []SiteMemory
	if json.Unmarshal(raw, &list) != nil {
		return
	}
	maxID := 0
	for _, e := range list {
		if e.ID > maxID {
			maxID = e.ID
		}
	}
	s.entries = list
	s.nextID = maxID + 1
}

func (s *siteMemoryStore) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *siteMemoryStore) list() []SiteMemory {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	out := make([]SiteMemory, len(s.entries))
	copy(out, s.entries)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Host != out[j].Host {
			return out[i].Host < out[j].Host
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func (s *siteMemoryStore) add(host, text string) (SiteMemory, error) {
	h := normalizeSiteHost(host)
	if h == "" {
		return SiteMemory{}, errors.New("site host is required (e.g. github.com)")
	}
	t := strings.TrimSpace(text)
	if t == "" {
		return SiteMemory{}, errors.New("site memory text is required")
	}
	if len(t) > maxSiteMemoryChars {
		t = strings.TrimSpace(t[:maxSiteMemoryChars])
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	// Deduplicate exact host+text pair.
	for i := range s.entries {
		if s.entries[i].Host == h && strings.EqualFold(s.entries[i].Text, t) {
			s.entries[i].UpdatedAt = time.Now().Unix()
			_ = s.saveLocked()
			return s.entries[i], nil
		}
	}
	entry := SiteMemory{
		ID:        s.nextID,
		Host:      h,
		Text:      t,
		UpdatedAt: time.Now().Unix(),
	}
	s.nextID++
	s.entries = append(s.entries, entry)
	if err := s.saveLocked(); err != nil {
		return SiteMemory{}, err
	}
	return entry, nil
}

func (s *siteMemoryStore) delete(id int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries = append(s.entries[:i], s.entries[i+1:]...)
			return s.saveLocked()
		}
	}
	return errors.New("site memory not found")
}

// match returns up to max notes matching exact host first, then parent domains
// (e.g., app.github.com matches app.github.com and github.com).
func (s *siteMemoryStore) match(rawURLOrHost string, max int) []SiteMemory {
	h := normalizeSiteHost(rawURLOrHost)
	if h == "" {
		return nil
	}
	if max <= 0 {
		max = maxSiteMemoriesInject
	}

	// Build candidate host chain: ["app.github.com", "github.com"]
	candidates := []string{h}
	parts := strings.Split(h, ".")
	for i := 1; i+1 < len(parts); i++ {
		parent := strings.Join(parts[i:], ".")
		candidates = append(candidates, parent)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.loadLocked()

	var matched []SiteMemory
	for _, cand := range candidates {
		for _, e := range s.entries {
			if e.Host == cand {
				matched = append(matched, e)
				if len(matched) >= max {
					return matched
				}
			}
		}
	}
	return matched
}

// claimForTurn returns matching site memories only once per (sessionKey, host)
// within a turn window, preventing repeated token overhead on subsequent actions
// on the same page.
func (s *siteMemoryStore) claimForTurn(sessionKey, rawURL string) []SiteMemory {
	h := normalizeSiteHost(rawURL)
	if h == "" {
		return nil
	}
	notes := s.match(h, maxSiteMemoriesInject)
	if len(notes) == 0 {
		return nil
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := s.turnSeen[sessionKey]
	if seen == nil {
		seen = map[string]time.Time{}
		s.turnSeen[sessionKey] = seen
	}
	if prev, ok := seen[h]; ok && now.Sub(prev) < siteTurnInjectTTL {
		return nil
	}
	seen[h] = now
	return notes
}

func (s *siteMemoryStore) resetTurn(sessionKey string) {
	s.mu.Lock()
	delete(s.turnSeen, sessionKey)
	s.mu.Unlock()
}

// Registry public Site Memory methods (used by Desk API and browser tool).

func (r *Registry) ListSiteMemories() []SiteMemory {
	if r == nil {
		return nil
	}
	return r.siteMem().list()
}

func (r *Registry) AddSiteMemory(host, text string) (SiteMemory, error) {
	if r == nil {
		return SiteMemory{}, errors.New("registry is nil")
	}
	return r.siteMem().add(host, text)
}

func (r *Registry) DeleteSiteMemory(id int) error {
	if r == nil {
		return errors.New("registry is nil")
	}
	return r.siteMem().delete(id)
}

func (r *Registry) ResetBrowserTurnSiteMemories(sessionID string) {
	if r == nil {
		return
	}
	r.siteMem().resetTurn(browserSessionKey(sessionID))
}
