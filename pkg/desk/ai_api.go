package desk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

type RoutingConfig struct {
	Enabled            bool     `json:"enabled"`
	Active             bool     `json:"active"`
	LightProviderID    int64    `json:"lightProviderId"`
	LightProviderName  string   `json:"lightProviderName"`
	StrongProviderID   int64    `json:"strongProviderId"`
	StrongProviderName string   `json:"strongProviderName"`
	Sensitivity        string   `json:"sensitivity"`
	Escalate           bool     `json:"escalate"`
	Kinds              []string `json:"kinds"`
	AllKinds           []string `json:"allKinds"`
}

type RoutingUpdate struct {
	Enabled          *bool    `json:"enabled"`
	StrongProviderID *int64   `json:"strongProviderId"`
	Sensitivity      *string  `json:"sensitivity"`
	Escalate         *bool    `json:"escalate"`
	Kinds            []string `json:"kinds"`
	KindsSet         bool     `json:"kindsSet"`
}

type RoutingTestResult struct {
	Strong         bool     `json:"strong"`
	Kind           string   `json:"kind"`
	Hard           bool     `json:"hard"`
	TargetProvider string   `json:"targetProvider"`
	Signals        []string `json:"signals"`
}

type RoutingHost interface {
	RoutingConfig() RoutingConfig
	SaveRoutingConfig(ctx context.Context, req RoutingUpdate) (RoutingConfig, error)
	TestRouting(text string) RoutingTestResult
}

type MoaSlotView struct {
	Provider string `json:"provider"`
	Effort   string `json:"effort,omitempty"`
}

type MoaPresetView struct {
	Name          string        `json:"name"`
	Aggregator    MoaSlotView   `json:"aggregator"`
	References    []MoaSlotView `json:"references"`
	Fanout        string        `json:"fanout"`
	PrivacyFilter string        `json:"privacyFilter"`
	Enabled       bool          `json:"enabled"`
	Problems      []string      `json:"problems,omitempty"`
}

type MoaState struct {
	Presets       []MoaPresetView `json:"presets"`
	Active        string          `json:"active"`
	DefaultPreset string          `json:"defaultPreset"`
	PrivacyFilter string          `json:"privacyFilter"`
}

type MoaConfigUpdate struct {
	Active        *string `json:"active"`
	DefaultPreset *string `json:"defaultPreset"`
	PrivacyFilter *string `json:"privacyFilter"`
}

type MoaHost interface {
	MoaState() MoaState
	SaveMoaPreset(ctx context.Context, p MoaPresetView) (MoaState, error)
	DeleteMoaPreset(ctx context.Context, name string) (MoaState, error)
	UpdateMoaConfig(ctx context.Context, req MoaConfigUpdate) (MoaState, error)
}

type SearchHit struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	ThreadID    string `json:"threadId"`
	ThreadTitle string `json:"threadTitle"`
	Role        string `json:"role"`
	Snippet     string `json:"snippet"`
	At          int64  `json:"at"`
}

func (s *Server) routingGetAPI(w http.ResponseWriter, r *http.Request) {
	rh, ok := s.host.(RoutingHost)
	if !ok {
		writeJSON(w, RoutingConfig{})
		return
	}
	writeJSON(w, rh.RoutingConfig())
}

func (s *Server) routingSaveAPI(w http.ResponseWriter, r *http.Request) {
	rh, ok := s.host.(RoutingHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("smart routing is not available"))
		return
	}
	var req RoutingUpdate
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg, err := rh.SaveRoutingConfig(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, cfg)
}

func (s *Server) routingTestAPI(w http.ResponseWriter, r *http.Request) {
	rh, ok := s.host.(RoutingHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("smart routing is not available"))
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Text) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("sample text is required"))
		return
	}
	writeJSON(w, rh.TestRouting(strings.TrimSpace(in.Text)))
}

func (s *Server) moaGetAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(MoaHost)
	if !ok {
		writeJSON(w, MoaState{Presets: []MoaPresetView{}})
		return
	}
	writeJSON(w, mh.MoaState())
}

func (s *Server) moaSavePresetAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(MoaHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("MoA is not available"))
		return
	}
	var p MoaPresetView
	if err := readJSON(r, &p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := mh.SaveMoaPreset(r.Context(), p)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, st)
}

func (s *Server) moaDeletePresetAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(MoaHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("MoA is not available"))
		return
	}
	name := strings.TrimSpace(r.PathValue("name"))
	if name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("preset name is required"))
		return
	}
	st, err := mh.DeleteMoaPreset(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, st)
}

func (s *Server) moaConfigAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(MoaHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("MoA is not available"))
		return
	}
	var req MoaConfigUpdate
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st, err := mh.UpdateMoaConfig(r.Context(), req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, st)
}

func (s *Server) searchSessionsAPI(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	limit := 30
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 100 {
			limit = n
		}
	}
	if q == "" {
		writeJSON(w, map[string]any{"hits": []SearchHit{}})
		return
	}
	terms := strings.Fields(strings.ToLower(q))
	s.mu.Lock()
	projs := make([]*project, 0, len(s.projects))
	for _, p := range s.projects {
		projs = append(projs, p)
	}
	defID := projectID(s.host.DefaultProject())
	s.mu.Unlock()

	var hits []SearchHit
	for _, p := range projs {
		p.mu.Lock()
		p.loadThreadsLocked()
		p.loadLocked()
		activeTID := p.activeThread
		if activeTID == "" {
			activeTID = defaultThreadID
		}
		activeBlocks := make([]Block, len(p.blocks))
		copy(activeBlocks, p.blocks)
		threads := make([]ThreadSummary, len(p.threads))
		copy(threads, p.threads)
		pName := filepath.Base(p.path)
		if p.id == defID {
			pName = "Home"
		}
		p.mu.Unlock()

		seenThread := map[string]bool{}
		for _, th := range threads {
			seenThread[th.ID] = true
			var blocks []Block
			if th.ID == activeTID {
				blocks = activeBlocks
			} else {
				blocks = readThreadBlocks(p.threadFile(th.ID))
			}
			hits = append(hits, matchBlocks(p.id, pName, th.ID, th.Title, blocks, terms)...)
		}
		if !seenThread[activeTID] {
			hits = append(hits, matchBlocks(p.id, pName, activeTID, "New conversation", activeBlocks, terms)...)
		}
	}

	sort.Slice(hits, func(i, j int) bool { return hits[i].At > hits[j].At })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	if hits == nil {
		hits = []SearchHit{}
	}
	writeJSON(w, map[string]any{"hits": hits})
}

func readThreadBlocks(path string) []Block {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []Block
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 2<<20)
	for sc.Scan() {
		var b Block
		if json.Unmarshal(sc.Bytes(), &b) == nil && b.Kind != "" {
			out = append(out, b)
		}
	}
	return out
}

func matchBlocks(projID, projName, threadID, threadTitle string, blocks []Block, terms []string) []SearchHit {
	var out []SearchHit
	for i := len(blocks) - 1; i >= 0; i-- {
		b := blocks[i]
		if b.Kind != "user" && b.Kind != "assistant" && b.Kind != "note" {
			continue
		}
		txt := strings.TrimSpace(b.Text)
		if txt == "" {
			continue
		}
		low := strings.ToLower(txt)
		ok := true
		for _, term := range terms {
			if !strings.Contains(low, term) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out = append(out, SearchHit{
			ProjectID:   projID,
			ProjectName: projName,
			ThreadID:    threadID,
			ThreadTitle: threadTitle,
			Role:        b.Kind,
			Snippet:     excerptMatch(txt, terms[0], 180),
			At:          b.At,
		})
		if len(out) >= 4 {
			break
		}
	}
	return out
}

func excerptMatch(text, term string, maxRunes int) string {
	flat := strings.Join(strings.Fields(text), " ")
	runes := []rune(flat)
	if len(runes) <= maxRunes {
		return flat
	}
	low := strings.ToLower(flat)
	idx := strings.Index(low, term)
	if idx < 0 {
		return string(runes[:maxRunes]) + "…"
	}
	runeIdx := min(utf8.RuneCountInString(low[:idx]), len(runes))
	start := runeIdx - maxRunes/3
	if start < 0 {
		start = 0
	}
	end := start + maxRunes
	if end > len(runes) {
		end = len(runes)
		start = max(0, end-maxRunes)
	}
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(runes) {
		suffix = "…"
	}
	return prefix + string(runes[start:end]) + suffix
}
