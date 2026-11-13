package desk

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/tui"
)

//go:embed web
var webFiles embed.FS

const cookieName = "macron_desk"

type Server struct {
	host   Host
	token  string
	hub    *hub
	Notify func(title, body string)

	mu       sync.Mutex
	projects map[string]*project
	mode     string
	addr     string
}

func New(host Host) *Server {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	s := &Server{host: host, token: hex.EncodeToString(b), hub: newHub(), projects: map[string]*project{}, mode: "build"}
	paths := host.Projects()
	if d := host.DefaultProject(); d != "" {
		paths = append([]string{d}, paths...)
	}
	for _, path := range paths {
		s.addProject(path)
	}
	return s
}

func (s *Server) Token() string { return s.token }

func (s *Server) notify(title, body string) {
	if s.Notify != nil {
		s.Notify(title, body)
	}
}

func (s *Server) addProject(path string) *project {
	path = filepath.Clean(path)
	id := projectID(path)
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.projects[id]; ok {
		return p
	}
	p := &project{srv: s, id: id, path: path, mode: s.mode}
	s.projects[id] = p
	return p
}

func (s *Server) saveProjects() {
	s.mu.Lock()
	var paths []string
	def := filepath.Clean(s.host.DefaultProject())
	for _, p := range s.projects {
		if p.path != def {
			paths = append(paths, p.path)
		}
	}
	s.mu.Unlock()
	sort.Strings(paths)
	_ = s.host.SaveProjects(paths)
}

func (s *Server) project(id string) *project {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.projects[id]
}

func (s *Server) Listen(addr string) (net.Listener, string, error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, "", err
	}
	s.addr = ln.Addr().String()
	return ln, "http://" + s.addr + "/?t=" + s.token, nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	static, _ := fs.Sub(webFiles, "web")
	files := http.FileServer(http.FS(static))
	mux.HandleFunc("GET /{$}", s.index)
	mux.Handle("GET /assets/", http.StripPrefix("/assets/", files))
	mux.HandleFunc("GET /code.css", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = w.Write(chromaCSS())
	})
	mux.HandleFunc("GET /api/state", s.state)
	mux.HandleFunc("GET /api/events", s.events)
	mux.HandleFunc("POST /api/projects", s.addProjectAPI)
	mux.HandleFunc("DELETE /api/projects/{id}", s.removeProject)
	mux.HandleFunc("GET /api/projects/{id}", s.projectAPI)
	mux.HandleFunc("POST /api/projects/{id}/send", s.sendAPI)
	mux.HandleFunc("POST /api/projects/{id}/stop", s.stopAPI)
	mux.HandleFunc("POST /api/projects/{id}/new", s.newAPI)
	mux.HandleFunc("POST /api/projects/{id}/compact", s.compactAPI)
	mux.HandleFunc("POST /api/projects/{id}/answer", s.answerAPI)
	mux.HandleFunc("GET /api/models", s.modelsAPI)
	mux.HandleFunc("POST /api/model", s.setModelAPI)
	mux.HandleFunc("GET /api/providers", s.providersAPI)
	mux.HandleFunc("POST /api/provider", s.setProviderAPI)
	mux.HandleFunc("POST /api/mode", s.modeAPI)
	mux.HandleFunc("POST /api/approvals", s.approvalsAPI)
	mux.HandleFunc("POST /api/yolo", s.yoloAPI)
	mux.HandleFunc("GET /api/connections", s.connectionsAPI)
	mux.HandleFunc("GET /api/fs", s.fsAPI)
	return s.guard(mux)
}

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data: https:; style-src 'self' 'unsafe-inline'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'")
		if r.URL.Path == "/" && r.URL.Query().Get("t") != "" {
			if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("t")), []byte(s.token)) == 1 {
				http.SetCookie(w, &http.Cookie{Name: cookieName, Value: s.token, Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode})
				http.Redirect(w, r, "/", http.StatusFound)
				return
			}
		}
		if !s.authorized(r) {
			http.Error(w, "open Macron from its own window or link", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Macron") != "1" {
			http.Error(w, "missing request header", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if c, err := r.Cookie(cookieName); err == nil && subtle.ConstantTimeCompare([]byte(c.Value), []byte(s.token)) == 1 {
		return true
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	return auth != "" && subtle.ConstantTimeCompare([]byte(auth), []byte(s.token)) == 1
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	data, err := webFiles.ReadFile("web/index.html")
	if err != nil {
		http.Error(w, "missing page", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	title, hint := explain(err)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": title, "hint": hint, "detail": err.Error()})
}

func readJSON(r *http.Request, v any) error {
	return json.NewDecoder(http.MaxBytesReader(nil, r.Body, 1<<20)).Decode(v)
}

func explain(err error) (string, string) { return tui.ExplainError(err) }

func (s *Server) info() map[string]any {
	i := s.host.Info()
	if i.Platform == "" {
		i.Platform = runtime.GOOS
	}
	s.mu.Lock()
	mode := s.mode
	s.mu.Unlock()
	return map[string]any{"info": i, "mode": mode}
}

func (s *Server) summaries() []Summary {
	s.mu.Lock()
	list := make([]*project, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, p)
	}
	def := filepath.Clean(s.host.DefaultProject())
	s.mu.Unlock()
	out := make([]Summary, 0, len(list))
	for _, p := range list {
		out = append(out, p.summary())
	}
	sort.SliceStable(out, func(i, j int) bool {
		if (out[i].Path == def) != (out[j].Path == def) {
			return out[i].Path == def
		}
		return out[i].Updated > out[j].Updated
	})
	return out
}

func (s *Server) state(w http.ResponseWriter, r *http.Request) {
	st := s.info()
	st["projects"] = s.summaries()
	st["default"] = projectID(filepath.Clean(s.host.DefaultProject()))
	st["home"], _ = os.UserHomeDir()
	writeJSON(w, st)
}

func (s *Server) addProjectAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Path) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("a folder path is needed"))
		return
	}
	path, err := filepath.Abs(strings.TrimSpace(in.Path))
	if err == nil {
		var fi os.FileInfo
		if fi, err = os.Stat(path); err == nil && !fi.IsDir() {
			err = errors.New("that is a file, not a folder")
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p := s.addProject(path)
	s.saveProjects()
	s.hub.publish(map[string]any{"type": "projects"})
	writeJSON(w, p.summary())
}

func (s *Server) removeProject(w http.ResponseWriter, r *http.Request) {
	p := s.project(r.PathValue("id"))
	if p == nil || filepath.Clean(p.path) == filepath.Clean(s.host.DefaultProject()) {
		writeErr(w, http.StatusBadRequest, errors.New("this conversation cannot be removed"))
		return
	}
	p.stop()
	s.mu.Lock()
	delete(s.projects, p.id)
	s.mu.Unlock()
	s.saveProjects()
	s.hub.publish(map[string]any{"type": "projects"})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) withProject(w http.ResponseWriter, r *http.Request) *project {
	p := s.project(r.PathValue("id"))
	if p == nil {
		writeErr(w, http.StatusNotFound, errors.New("no such project"))
	}
	return p
}

func (s *Server) projectAPI(w http.ResponseWriter, r *http.Request) {
	if p := s.withProject(w, r); p != nil {
		writeJSON(w, p.snapshot())
	}
}

func (s *Server) sendAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := p.send(in.Text); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) stopAPI(w http.ResponseWriter, r *http.Request) {
	if p := s.withProject(w, r); p != nil {
		p.stop()
		writeJSON(w, map[string]bool{"ok": true})
	}
}

func (s *Server) newAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	p.mu.Lock()
	running := p.live.Running
	p.mu.Unlock()
	if running {
		writeErr(w, http.StatusConflict, errors.New("stop the current job first"))
		return
	}
	sess, err := p.session()
	if err == nil {
		err = sess.New(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	p.mu.Lock()
	p.loadLocked()
	b := p.appendLocked(Block{Kind: "divider", Text: "New conversation"})
	p.mu.Unlock()
	p.emit("block", map[string]any{"block": b})
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) compactAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()
	text, err := sess.Compact(ctx)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	p.mu.Lock()
	b := p.appendLocked(Block{Kind: "note", Text: text})
	p.mu.Unlock()
	p.emit("block", map[string]any{"block": b})
	writeJSON(w, map[string]string{"text": text})
}

func (s *Server) answerAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		ID     string `json:"id"`
		Choice string `json:"choice"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := p.answer(in.ID, in.Choice); err != nil {
		writeErr(w, http.StatusConflict, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) modelsAPI(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	cur, list, err := s.host.Models(ctx)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, map[string]any{"current": cur, "models": list})
}

func (s *Server) setModelAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := readJSON(r, &in); err != nil || strings.TrimSpace(in.Name) == "" {
		writeErr(w, http.StatusBadRequest, errors.New("a model name is needed"))
		return
	}
	if err := s.host.SetModel(r.Context(), strings.TrimSpace(in.Name)); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, s.info())
}

func (s *Server) providersAPI(w http.ResponseWriter, r *http.Request) {
	list, err := s.host.Providers(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, map[string]any{"providers": list})
}

func (s *Server) setProviderAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ID int64 `json:"id"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.host.SetProvider(r.Context(), in.ID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, s.info())
}

func (s *Server) modeAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil || (in.Mode != "build" && in.Mode != "plan") {
		writeErr(w, http.StatusBadRequest, errors.New("mode is build or plan"))
		return
	}
	s.mu.Lock()
	s.mode = in.Mode
	list := make([]*project, 0, len(s.projects))
	for _, p := range s.projects {
		list = append(list, p)
	}
	s.mu.Unlock()
	for _, p := range list {
		p.mu.Lock()
		p.mode = in.Mode
		sess := p.sess
		p.mu.Unlock()
		if sess != nil {
			_ = sess.SetMode(in.Mode)
		}
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, s.info())
}

func (s *Server) approvalsAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Mode string `json:"mode"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := s.host.SetApprovals(in.Mode); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, s.info())
}

func (s *Server) yoloAPI(w http.ResponseWriter, r *http.Request) {
	var in struct {
		On bool `json:"on"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	s.host.Yolo(in.On)
	s.hub.publish(map[string]any{"type": "info"})
	writeJSON(w, s.info())
}

func (s *Server) connectionsAPI(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"connections": s.host.Connections()})
}

func (s *Server) fsAPI(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSpace(r.URL.Query().Get("path"))
	if path == "" {
		path, _ = os.UserHomeDir()
	}
	path, _ = filepath.Abs(path)
	entries, err := os.ReadDir(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
			dirs = append(dirs, e.Name())
		}
	}
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	parent := filepath.Dir(path)
	if parent == path {
		parent = ""
	}
	home, _ := os.UserHomeDir()
	writeJSON(w, map[string]any{"path": path, "parent": parent, "dirs": dirs, "home": home, "roots": roots(), "sep": string(filepath.Separator)})
}

func roots() []string {
	if runtime.GOOS != "windows" {
		return []string{"/"}
	}
	var out []string
	for c := 'C'; c <= 'Z'; c++ {
		d := string(c) + `:\`
		if _, err := os.Stat(d); err == nil {
			out = append(out, d)
		}
	}
	return out
}

func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	ch, drop := s.hub.subscribe()
	defer drop()
	_, _ = w.Write([]byte("retry: 1500\n\n"))
	flusher.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			_, _ = w.Write([]byte(": ping\n\n"))
			flusher.Flush()
		case ev := <-ch:
			_, _ = w.Write([]byte("data: "))
			_, _ = w.Write(ev)
			_, _ = w.Write([]byte("\n\n"))
			flusher.Flush()
		}
	}
}

type hub struct {
	mu   sync.Mutex
	subs map[chan []byte]bool
	seq  int64
}

func newHub() *hub { return &hub{subs: map[chan []byte]bool{}} }

func (h *hub) subscribe() (chan []byte, func()) {
	ch := make(chan []byte, 512)
	h.mu.Lock()
	h.subs[ch] = true
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		delete(h.subs, ch)
		h.mu.Unlock()
	}
}

func (h *hub) publish(ev map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.seq++
	ev["seq"] = h.seq
	raw, err := json.Marshal(ev)
	if err != nil {
		return
	}
	for ch := range h.subs {
		select {
		case ch <- raw:
		default:
		}
	}
}
