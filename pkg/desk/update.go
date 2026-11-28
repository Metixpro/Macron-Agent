package desk

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

type UpdateStatus struct {
	Current    string `json:"current"`
	Latest     string `json:"latest,omitempty"`
	Tag        string `json:"tag,omitempty"`
	URL        string `json:"url,omitempty"`
	Notes      string `json:"notes,omitempty"`
	Available  bool   `json:"available"`
	Installing bool   `json:"installing,omitempty"`
	Installed  bool   `json:"installed,omitempty"`
	Step       string `json:"step,omitempty"`
	Phase      string `json:"phase,omitempty"`
	Downloaded int64  `json:"downloaded,omitempty"`
	Total      int64  `json:"total,omitempty"`
	Percent    int    `json:"percent,omitempty"`
	SpeedBps   int64  `json:"speedBps,omitempty"`
	AssetSize  int64  `json:"assetSize,omitempty"`
	Error      string `json:"error,omitempty"`
	CheckedAt  int64  `json:"checkedAt,omitempty"`
}

type updateState struct {
	mu         sync.Mutex
	rel        *updater.Release
	status     UpdateStatus
	checkedAt  time.Time
	installing bool
	revision   uint64
}

const updateCacheTTL = 30 * time.Minute

func releaseAssetSize(rel *updater.Release) int64 {
	if rel == nil {
		return 0
	}
	want := updater.AssetName(rel.Version())
	for _, a := range rel.Assets {
		if a.Name == want {
			return a.Size
		}
	}
	return 0
}

func (s *Server) checkUpdate(ctx context.Context, force bool) UpdateStatus {
	return s.checkUpdateWith(ctx, force, updater.Latest)
}

func (s *Server) checkUpdateWith(ctx context.Context, force bool, latest func(context.Context, string) (*updater.Release, error)) UpdateStatus {
	s.upd.mu.Lock()
	if s.upd.installing || s.upd.status.Installed {
		st := s.upd.status
		s.upd.mu.Unlock()
		return st
	}
	if !force && !s.upd.checkedAt.IsZero() && time.Since(s.upd.checkedAt) < updateCacheTTL {
		st := s.upd.status
		s.upd.mu.Unlock()
		return st
	}
	s.upd.revision++
	revision := s.upd.revision
	s.upd.mu.Unlock()

	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	rel, err := latest(cctx, "")

	s.upd.mu.Lock()
	defer s.upd.mu.Unlock()
	if s.upd.revision != revision {
		return s.upd.status
	}
	now := time.Now()
	st := UpdateStatus{
		Current:   app.Version,
		CheckedAt: now.UnixMilli(),
	}
	if err != nil {
		st.Error = err.Error()
		s.upd.status = st
		return st
	}
	version := rel.Version()
	st.Latest = version
	st.Tag = rel.TagName
	st.URL = rel.HTMLURL
	st.Notes = clipRunes(strings.TrimSpace(rel.Body), 2000)
	st.AssetSize = releaseAssetSize(rel)
	st.Available = updater.Newer(version, app.Version)
	s.upd.rel = rel
	s.upd.status = st
	s.upd.checkedAt = now
	return st
}

func (s *Server) updateStatusAPI(w http.ResponseWriter, r *http.Request) {
	force := r.URL.Query().Get("force") == "1"
	st := s.checkUpdate(r.Context(), force)
	writeJSON(w, st)
}

func (s *Server) updateInstallAPI(w http.ResponseWriter, r *http.Request) {
	s.upd.mu.Lock()
	if s.upd.installing || s.upd.status.Installed {
		st := s.upd.status
		s.upd.mu.Unlock()
		writeJSON(w, st)
		return
	}
	rel := s.upd.rel
	s.upd.revision++
	s.upd.installing = true
	s.upd.status.Installing = true
	s.upd.status.Error = ""
	s.upd.status.Phase = "checking"
	s.upd.status.Step = "Checking release"
	s.upd.status.Percent = 1
	s.upd.status.Downloaded = 0
	s.upd.status.Total = releaseAssetSize(rel)
	s.upd.status.SpeedBps = 0
	initSt := s.upd.status
	s.upd.mu.Unlock()

	publish := func(st UpdateStatus) {
		if s.hub != nil {
			s.hub.publish(map[string]any{"type": "update", "update": st})
		}
	}
	publish(initSt)

	if rel == nil {
		cctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		var err error
		rel, err = updater.Latest(cctx, "")
		cancel()
		if err != nil {
			s.upd.mu.Lock()
			s.upd.installing = false
			s.upd.status.Installing = false
			s.upd.status.Phase = ""
			s.upd.status.Step = ""
			s.upd.status.Percent = 0
			s.upd.status.Error = err.Error()
			st := s.upd.status
			s.upd.mu.Unlock()
			publish(st)
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}

	if !updater.Newer(rel.Version(), app.Version) {
		s.upd.mu.Lock()
		s.upd.installing = false
		s.upd.status = UpdateStatus{
			Current:   app.Version,
			Latest:    rel.Version(),
			Tag:       rel.TagName,
			URL:       rel.HTMLURL,
			Available: false,
			CheckedAt: time.Now().UnixMilli(),
		}
		st := s.upd.status
		s.upd.mu.Unlock()
		publish(st)
		writeJSON(w, st)
		return
	}

	uctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	err := updater.InstallWithProgress(uctx, rel, func(p updater.Progress) {
		s.upd.mu.Lock()
		s.upd.status.Phase = p.Phase
		s.upd.status.Step = p.Label
		s.upd.status.Downloaded = p.Downloaded
		s.upd.status.Total = p.Total
		s.upd.status.Percent = p.Percent
		s.upd.status.SpeedBps = p.SpeedBps
		st := s.upd.status
		s.upd.mu.Unlock()
		publish(st)
	})
	if err != nil {
		s.upd.mu.Lock()
		s.upd.installing = false
		s.upd.status.Installing = false
		s.upd.status.Phase = ""
		s.upd.status.Step = ""
		s.upd.status.Percent = 0
		s.upd.status.SpeedBps = 0
		s.upd.status.Error = err.Error()
		st := s.upd.status
		s.upd.mu.Unlock()
		publish(st)
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	s.upd.mu.Lock()
	s.upd.installing = false
	s.upd.status = UpdateStatus{
		Current:   app.Version,
		Latest:    rel.Version(),
		Tag:       rel.TagName,
		URL:       rel.HTMLURL,
		Available: false,
		Installed: true,
		Phase:     "done",
		Step:      "Ready to restart",
		Percent:   100,
		CheckedAt: time.Now().UnixMilli(),
	}
	st := s.upd.status
	s.upd.mu.Unlock()
	publish(st)
	writeJSON(w, st)
}
