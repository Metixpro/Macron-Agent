package desk

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type MemoryEntry struct {
	ID    int    `json:"id"`
	Scope string `json:"scope"` // "memory" or "user"
	Text  string `json:"text"`
}

type SkillView struct {
	Name        string   `json:"name"`
	Category    string   `json:"category"`
	Description string   `json:"description"`
	Version     string   `json:"version,omitempty"`
	Enabled     bool     `json:"enabled"`
	Uses        int      `json:"uses"`
	Files       []string `json:"files,omitempty"`
}

type ConnectionField struct {
	Key     string `json:"key"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
	Default string `json:"default,omitempty"`
	Secret  bool   `json:"secret"`
	Set     bool   `json:"set"`
}

type ConnectionDetail struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	Capability string            `json:"capability"`
	Ready      bool              `json:"ready"`
	Restart    bool              `json:"restart,omitempty"`
	Fields     []ConnectionField `json:"fields"`
}

type memoryHost interface {
	Memories() ([]MemoryEntry, error)
	AddMemory(scope, text string) (MemoryEntry, error)
	DeleteMemory(scope string, id int) error
}

type skillHost interface {
	Skills() []SkillView
	SetSkillEnabled(name string, on bool) error
	DeleteSkill(name string) error
}

type connectionConfigHost interface {
	ConnectionDetails() []ConnectionDetail
	SaveConnectionValues(values map[string]string) error
}

type connectionTestHost interface {
	TestConnection(ctx context.Context, service, resource string) (string, error)
}

func (s *Server) memoriesAPI(w http.ResponseWriter, _ *http.Request) {
	mh, ok := s.host.(memoryHost)
	if !ok {
		writeJSON(w, map[string]any{"memories": []MemoryEntry{}})
		return
	}
	list, err := mh.Memories()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if list == nil {
		list = []MemoryEntry{}
	}
	writeJSON(w, map[string]any{"memories": list})
}

func (s *Server) addMemoryAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(memoryHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("memory management is not supported"))
		return
	}
	var in struct {
		Scope string `json:"scope"`
		Text  string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		writeErr(w, http.StatusBadRequest, errors.New("memory text is required"))
		return
	}
	if in.Scope != "user" {
		in.Scope = "memory"
	}
	entry, err := mh.AddMemory(in.Scope, in.Text)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, entry)
}

func (s *Server) deleteMemoryAPI(w http.ResponseWriter, r *http.Request) {
	mh, ok := s.host.(memoryHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("memory management is not supported"))
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("invalid memory id"))
		return
	}
	scope := r.URL.Query().Get("scope")
	if scope != "user" {
		scope = "memory"
	}
	if err := mh.DeleteMemory(scope, id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) skillsAPI(w http.ResponseWriter, _ *http.Request) {
	sh, ok := s.host.(skillHost)
	if !ok {
		writeJSON(w, map[string]any{"skills": []SkillView{}})
		return
	}
	list := sh.Skills()
	if list == nil {
		list = []SkillView{}
	}
	writeJSON(w, map[string]any{"skills": list})
}

func (s *Server) enableSkillAPI(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.host.(skillHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("skill management is not supported"))
		return
	}
	var in struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	name := r.PathValue("name")
	if err := sh.SetSkillEnabled(name, in.On); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "skills": sh.Skills()})
}

func (s *Server) deleteSkillAPI(w http.ResponseWriter, r *http.Request) {
	sh, ok := s.host.(skillHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("skill management is not supported"))
		return
	}
	name := r.PathValue("name")
	if err := sh.DeleteSkill(name); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "skills": sh.Skills()})
}

func (s *Server) connectionDetailsAPI(w http.ResponseWriter, _ *http.Request) {
	ch, ok := s.host.(connectionConfigHost)
	if !ok {
		writeJSON(w, map[string]any{"connections": []ConnectionDetail{}})
		return
	}
	list := ch.ConnectionDetails()
	if list == nil {
		list = []ConnectionDetail{}
	}
	writeJSON(w, map[string]any{"connections": list})
}

func (s *Server) saveConnectionAPI(w http.ResponseWriter, r *http.Request) {
	ch, ok := s.host.(connectionConfigHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("connection configuration is not supported"))
		return
	}
	var in struct {
		Values map[string]string `json:"values"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(in.Values) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("no settings supplied"))
		return
	}
	if err := ch.SaveConnectionValues(in.Values); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if s.hub != nil {
		s.hub.publish(map[string]any{"type": "info"})
	}
	writeJSON(w, map[string]any{
		"connections": ch.ConnectionDetails(),
		"info":        s.host.Info(),
	})
}

func (s *Server) testConnectionAPI(w http.ResponseWriter, r *http.Request) {
	th, ok := s.host.(connectionTestHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("connection testing is not supported"))
		return
	}
	var in struct {
		Service  string `json:"service"`
		Resource string `json:"resource"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	service := strings.TrimSpace(in.Service)
	if service == "" {
		writeErr(w, http.StatusBadRequest, errors.New("service is required"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	msg, err := th.TestConnection(ctx, service, strings.TrimSpace(in.Resource))
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "message": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true, "message": msg})
}
