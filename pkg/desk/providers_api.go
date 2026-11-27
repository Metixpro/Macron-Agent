package desk

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type ProviderDetail struct {
	ID            int64    `json:"id"`
	Name          string   `json:"name"`
	BaseURL       string   `json:"baseUrl"`
	APIKeySet     bool     `json:"apiKeySet"`
	APIKeyMasked  string   `json:"apiKeyMasked"`
	Model         string   `json:"model"`
	API           string   `json:"api"`
	ResolvedAPI   string   `json:"resolvedApi"`
	Priority      int      `json:"priority"`
	Enabled       bool     `json:"enabled"`
	Active        bool     `json:"active"`
	Vision        bool     `json:"vision"`
	NoStream      bool     `json:"noStream"`
	ContextWindow int      `json:"contextWindow"`
	AutoWindow    int      `json:"autoWindow"`
	Temperature   *float64 `json:"temperature"`
	TempLimit     float64  `json:"tempLimit"`
	Effort        string   `json:"effort"`
	Requests      int64    `json:"requests"`
	Failures      int64    `json:"failures"`
	AvgLatencyMS  int      `json:"avgLatencyMs"`
	LastOKAt      string   `json:"lastOkAt,omitempty"`
	LastError     string   `json:"lastError,omitempty"`
	LastErrorAt   string   `json:"lastErrorAt,omitempty"`
	Cooling       bool     `json:"cooling"`
}

type ProviderUpsert struct {
	Name          string   `json:"name"`
	BaseURL       string   `json:"baseUrl"`
	APIKey        string   `json:"apiKey"`
	ClearKey      bool     `json:"clearKey"`
	Model         string   `json:"model"`
	API           string   `json:"api"`
	Priority      *int     `json:"priority"`
	Enabled       *bool    `json:"enabled"`
	Vision        *bool    `json:"vision"`
	NoStream      *bool    `json:"noStream"`
	ContextWindow *int     `json:"contextWindow"`
	Temperature   *float64 `json:"temperature"`
	ClearTemp     bool     `json:"clearTemp"`
	Effort        *string  `json:"effort"`
	Activate      bool     `json:"activate"`
	SkipTest      bool     `json:"skipTest"`
}

type ProviderTestResult struct {
	OK        bool   `json:"ok"`
	Reply     string `json:"reply,omitempty"`
	LatencyMS int64  `json:"latencyMs"`
	Error     string `json:"error,omitempty"`
}

type ProviderManagerHost interface {
	ProviderDetails(ctx context.Context) ([]ProviderDetail, error)
	AddProvider(ctx context.Context, req ProviderUpsert) (ProviderDetail, error)
	UpdateProvider(ctx context.Context, id int64, req ProviderUpsert) (ProviderDetail, error)
	DeleteProvider(ctx context.Context, id int64) error
	TestProvider(ctx context.Context, id int64) (ProviderTestResult, error)
	ProviderModels(ctx context.Context, id int64) ([]string, error)
	Failover() bool
	SetFailover(ctx context.Context, on bool) error
}

type SoulHost interface {
	Soul() (text string, path string)
	SaveSoul(text string) error
	ResetSoul() (string, error)
}

type ShellSession interface {
	Shell(ctx context.Context, command string) string
}

type StatusSession interface {
	Status(ctx context.Context) string
}

func (s *Server) providersDetailAPI(w http.ResponseWriter, r *http.Request) {
	if pm, ok := s.host.(ProviderManagerHost); ok {
		list, err := pm.ProviderDetails(r.Context())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		if list == nil {
			list = []ProviderDetail{}
		}
		writeJSON(w, map[string]any{"providers": list, "failover": pm.Failover()})
		return
	}
	list, err := s.host.Providers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"providers": list, "failover": true})
}

func (s *Server) addProviderAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("provider management is not available"))
		return
	}
	var req ProviderUpsert
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.BaseURL = strings.TrimSpace(req.BaseURL)
	req.Model = strings.TrimSpace(req.Model)
	if req.Name == "" || req.BaseURL == "" || req.Model == "" {
		writeErr(w, http.StatusBadRequest, errors.New("provider name, API address, and model are required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	p, err := pm.AddProvider(ctx, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, map[string]any{"provider": p, "info": s.info()})
}

func (s *Server) updateProviderAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("provider management is not available"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid provider id"))
		return
	}
	var req ProviderUpsert
	if err := readJSON(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	p, err := pm.UpdateProvider(ctx, id, req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, map[string]any{"provider": p, "info": s.info()})
}

func (s *Server) deleteProviderAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("provider management is not available"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid provider id"))
		return
	}
	if err := pm.DeleteProvider(r.Context(), id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, map[string]any{"ok": true, "info": s.info()})
}

func (s *Server) testProviderAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("provider management is not available"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid provider id"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Second)
	defer cancel()
	res, err := pm.TestProvider(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) providerModelsAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("provider management is not available"))
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid provider id"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	models, err := pm.ProviderModels(ctx, id)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	if models == nil {
		models = []string{}
	}
	writeJSON(w, map[string]any{"models": models})
}

func (s *Server) failoverAPI(w http.ResponseWriter, r *http.Request) {
	pm, ok := s.host.(ProviderManagerHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("failover setting is not available"))
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := pm.SetFailover(r.Context(), in.On); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, map[string]any{"failover": pm.Failover(), "info": s.info()})
}

func (s *Server) soulGetAPI(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.host.(SoulHost)
	if !ok {
		writeJSON(w, map[string]any{"text": "", "path": ""})
		return
	}
	text, path := sh.Soul()
	writeJSON(w, map[string]any{"text": text, "path": path})
}

func (s *Server) soulSaveAPI(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.host.(SoulHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("SOUL editor is not available"))
		return
	}
	var in struct {
		Text  string `json:"text"`
		Reset bool   `json:"reset"`
	}
	if err := readJSONLimit(r, &in, 256<<10); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if in.Reset {
		text, err := sh.ResetSoul()
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		_, path := sh.Soul()
		writeJSON(w, map[string]any{"text": text, "path": path})
		return
	}
	if err := sh.SaveSoul(in.Text); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	text, path := sh.Soul()
	writeJSON(w, map[string]any{"text": text, "path": path})
}

func (s *Server) shellAPI(w http.ResponseWriter, r *http.Request) {
	p := s.project(r.PathValue("id"))
	if p == nil {
		writeErr(w, http.StatusNotFound, errors.New("unknown project"))
		return
	}
	p.mu.Lock()
	busy := p.live.Running || p.starting
	p.mu.Unlock()
	if busy {
		writeErr(w, http.StatusConflict, errors.New("stop the current job before running a shell command"))
		return
	}
	var in struct {
		Command string `json:"command"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Command) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("a shell command is required"))
		return
	}
	cmd := strings.TrimSpace(in.Command)
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	sh, ok := sess.(ShellSession)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("inline shell is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out := sh.Shell(ctx, cmd)

	p.mu.Lock()
	uBlk := p.appendLocked(Block{Kind: "user", Text: "! " + cmd})
	tBlk := p.appendLocked(Block{
		Kind:    "tool",
		Name:    "bash_execute",
		Label:   "Shell",
		Arg:     cmd,
		Summary: lastLine(out),
		Output:  out,
	})
	p.mu.Unlock()
	p.emit("block", map[string]any{"block": uBlk})
	p.emit("block", map[string]any{"block": tBlk})
	writeJSON(w, map[string]any{"output": out, "blocks": []Block{uBlk, tBlk}})
}

func (s *Server) projectStatusAPI(w http.ResponseWriter, r *http.Request) {
	p := s.project(r.PathValue("id"))
	if p == nil {
		writeErr(w, http.StatusNotFound, errors.New("unknown project"))
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	text := ""
	if st, ok := sess.(StatusSession); ok {
		text = st.Status(r.Context())
	}
	writeJSON(w, map[string]any{"status": text, "info": s.info()})
}
