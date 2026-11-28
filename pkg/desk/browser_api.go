package desk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/netclient"
)

type BrowserSession interface {
	HasBrowser() bool
	Browse(ctx context.Context, args map[string]any) (string, error)
}

type BrowserTab struct {
	Index    int    `json:"index"`
	URL      string `json:"url"`
	Title    string `json:"title"`
	Active   bool   `json:"active"`
	Frozen   bool   `json:"frozen,omitempty"`
	Sleeping bool   `json:"sleeping,omitempty"`
	Crashed  bool   `json:"crashed,omitempty"`
}

type BrowserViewport struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type BrowserChallenge struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type BrowserDialog struct {
	ID           string `json:"id"`
	Type         string `json:"type"`
	Message      string `json:"message"`
	DefaultValue string `json:"defaultValue,omitempty"`
}

type BrowserProfileBadge struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

type BrowserTeachStep struct {
	Index       int    `json:"index"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	URL         string `json:"url,omitempty"`
	Sensitive   bool   `json:"sensitive,omitempty"`
	At          int64  `json:"at,omitempty"`
}

type BrowserProxyConfig struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Bypass   string `json:"bypass,omitempty"`
}

type BrowserProfileView struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Color         string              `json:"color"`
	Scope         string              `json:"scope"`
	BuiltIn       bool                `json:"builtIn,omitempty"`
	HasProxy      bool                `json:"hasProxy,omitempty"`
	ProxyPassword bool                `json:"proxyPasswordSet,omitempty"`
	Proxy         *BrowserProxyConfig `json:"proxy,omitempty"`
}

type BrowserProfileUpsert struct {
	ID                   string              `json:"id"`
	Name                 string              `json:"name"`
	Color                string              `json:"color"`
	Proxy                *BrowserProxyConfig `json:"proxy,omitempty"`
	KeepExistingPassword bool                `json:"keepExistingPassword,omitempty"`
}

type BrowserInput struct {
	Type       string  `json:"type"`
	X          float64 `json:"x,omitempty"`
	Y          float64 `json:"y,omitempty"`
	DeltaX     float64 `json:"deltaX,omitempty"`
	DeltaY     float64 `json:"deltaY,omitempty"`
	Button     string  `json:"button,omitempty"`
	ClickCount int     `json:"clickCount,omitempty"`
	Key        string  `json:"key,omitempty"`
	Text       string  `json:"text,omitempty"`
	Modifiers  int     `json:"modifiers,omitempty"`
}

type LiveBrowserSession interface {
	BrowserSession
	BrowserLiveState() BrowserState
	BrowserNav(ctx context.Context, action string, params map[string]any) (BrowserState, error)
	BrowserInput(ctx context.Context, ev BrowserInput) error
	SetBrowserTakeover(ctx context.Context, active bool, reason string) (BrowserState, error)
	SubscribeBrowserFrames() (initial []byte, ch <-chan []byte, unsub func())
	SetBrowserObserver(fn func())
	CloseBrowser()
}

type ExtendedBrowserSession interface {
	BrowserResize(ctx context.Context, width, height int) (BrowserState, error)
	BrowserHandleDialog(ctx context.Context, accept bool, promptText, dialogID string) (BrowserState, error)
	BrowserContextMenu(ctx context.Context, params map[string]any) (map[string]any, error)
	BrowserTeach(ctx context.Context, action string) (BrowserState, []BrowserTeachStep, error)
	SetBrowserProfile(profileID string) (BrowserProfileView, error)
	SetBrowserEventObserver(fn func(event string, payload map[string]any))
}

type browserProfilesHost interface {
	BrowserProfiles() []BrowserProfileView
	SaveBrowserProfile(in BrowserProfileUpsert) (BrowserProfileView, error)
	DeleteBrowserProfile(id string) error
}

type BrowserState struct {
	Available      bool                 `json:"available"`
	Open           bool                 `json:"open"`
	Live           bool                 `json:"live,omitempty"`
	AgentActive    bool                 `json:"agentActive,omitempty"`
	Takeover       bool                 `json:"takeover,omitempty"`
	TakeoverReason string               `json:"takeoverReason,omitempty"`
	URL            string               `json:"url,omitempty"`
	Title          string               `json:"title,omitempty"`
	Updated        int64                `json:"updated,omitempty"`
	HasScreenshot  bool                 `json:"hasScreenshot"`
	Output         string               `json:"output,omitempty"`
	Cursor         string               `json:"cursor,omitempty"`
	Challenge      *BrowserChallenge    `json:"challenge,omitempty"`
	Dialog         *BrowserDialog       `json:"dialog,omitempty"`
	Profile        *BrowserProfileBadge `json:"profile,omitempty"`
	Teaching       bool                 `json:"teaching,omitempty"`
	TeachStartedAt int64                `json:"teachStartedAt,omitempty"`
	TeachSteps     []BrowserTeachStep   `json:"teachSteps,omitempty"`
	Tabs           []BrowserTab         `json:"tabs,omitempty"`
	Viewport       *BrowserViewport     `json:"viewport,omitempty"`
}

func (s *Server) browserDir(p *project) (string, bool) {
	dir := filepath.Join(p.path, ".browser")
	fi, err := os.Lstat(dir)
	if err != nil {
		return dir, errors.Is(err, os.ErrNotExist)
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return "", false
	}
	return dir, true
}

func (s *Server) browserFile(p *project, name string) (string, bool) {
	if _, ok := s.browserDir(p); !ok {
		return "", false
	}
	abs, _, err := resolveInside(p.path, filepath.Join(".browser", name))
	if err != nil {
		return "", false
	}
	return abs, true
}

func (s *Server) browserState(p *project) BrowserState {
	st := BrowserState{}
	var lbs LiveBrowserSession
	if sess, err := p.session(); err == nil {
		if bs, ok := sess.(BrowserSession); ok {
			st.Available = bs.HasBrowser()
		}
		if liveSess, ok := sess.(LiveBrowserSession); ok {
			lbs = liveSess
		}
	}
	if previewPath, ok := s.browserFile(p, "preview.png"); ok {
		if fi, err := os.Stat(previewPath); err == nil && !fi.IsDir() && fi.Size() > 0 {
			st.HasScreenshot = true
			st.Updated = fi.ModTime().UnixMilli()
		}
	}
	if lbs != nil && lbs.HasBrowser() {
		lst := lbs.BrowserLiveState()
		st.Available = lst.Available
		st.Takeover = lst.Takeover
		st.TakeoverReason = lst.TakeoverReason
		st.Profile = lst.Profile
		if lst.Live {
			st.Live = true
			st.Open = lst.Open
			st.AgentActive = lst.AgentActive
			st.URL = lst.URL
			st.Title = lst.Title
			if lst.Updated != 0 {
				st.Updated = lst.Updated
			}
			st.Cursor = lst.Cursor
			st.Challenge = lst.Challenge
			st.Dialog = lst.Dialog
			st.Teaching = lst.Teaching
			st.TeachStartedAt = lst.TeachStartedAt
			st.TeachSteps = lst.TeachSteps
			st.Tabs = lst.Tabs
			st.Viewport = lst.Viewport
			return st
		}
	}
	if metaPath, ok := s.browserFile(p, "meta.json"); ok {
		if raw, err := os.ReadFile(metaPath); err == nil {
			var m struct {
				URL     string `json:"url"`
				Title   string `json:"title"`
				Updated int64  `json:"updated"`
			}
			if json.Unmarshal(raw, &m) == nil {
				st.URL = strings.TrimSpace(m.URL)
				st.Title = strings.TrimSpace(m.Title)
				if m.Updated != 0 {
					st.Updated = m.Updated
				}
			}
		}
	}
	if st.URL == "" {
		if urlPath, ok := s.browserFile(p, "url"); ok {
			if raw, err := os.ReadFile(urlPath); err == nil {
				st.URL = strings.TrimSpace(string(raw))
			}
		}
	}
	st.Open = st.URL != "" || st.HasScreenshot
	return st
}

func (s *Server) browserStateAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	writeJSON(w, s.browserState(p))
}

func (s *Server) browserScreenshotAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	shot, ok := s.browserFile(p, "preview.png")
	if !ok {
		writeErr(w, http.StatusForbidden, errors.New("path must stay inside the project folder"))
		return
	}
	f, err := os.Open(shot)
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("no browser screenshot yet"))
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		writeErr(w, http.StatusNotFound, errors.New("no browser screenshot yet"))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'")
	http.ServeContent(w, r, "preview.png", fi.ModTime(), f)
}

func (s *Server) browserStreamAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	lbs, ok := sess.(LiveBrowserSession)
	if !ok || !lbs.HasBrowser() {
		writeErr(w, http.StatusNotImplemented, errors.New("live browser stream is not available"))
		return
	}
	initial, ch, unsub := lbs.SubscribeBrowserFrames()
	defer unsub()
	if len(initial) == 0 && !lbs.BrowserLiveState().Live {
		writeErr(w, http.StatusNotFound, errors.New("no live browser stream yet"))
		return
	}

	w.Header().Set("Content-Type", "multipart/x-mixed-replace; boundary=macronframe")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if fl, ok := w.(http.Flusher); ok {
		fl.Flush()
	}

	writeFrame := func(frame []byte) bool {
		if len(frame) == 0 {
			return true
		}
		hdr := fmt.Sprintf("--macronframe\r\nContent-Type: image/jpeg\r\nContent-Length: %d\r\n\r\n", len(frame))
		if _, err := w.Write([]byte(hdr)); err != nil {
			return false
		}
		if _, err := w.Write(frame); err != nil {
			return false
		}
		if _, err := w.Write([]byte("\r\n")); err != nil {
			return false
		}
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		return true
	}

	if !writeFrame(initial) {
		return
	}
	for {
		select {
		case frame, open := <-ch:
			if !open {
				return
			}
			if !writeFrame(frame) {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func normalizeActionURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", errors.New("a URL is required")
	}
	if !strings.Contains(u, "://") && !strings.HasPrefix(u, "//") && !strings.Contains(u, ":") {
		u = "https://" + u
	}
	parsed, err := netclient.ValidateURL(u)
	if err != nil {
		return "", err
	}
	return parsed.String(), nil
}

func (s *Server) browserActionAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Action string `json:"action"`
		URL    string `json:"url"`
		Text   string `json:"text"`
		Key    string `json:"key"`
		Submit bool   `json:"submit"`
		X      int    `json:"x"`
		Y      int    `json:"y"`
		DeltaY int    `json:"deltaY"`
		Index  *int   `json:"index"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	act := strings.ToLower(strings.TrimSpace(in.Action))
	if act == "close" {
		if sess, err := p.session(); err == nil {
			if lbs, ok := sess.(LiveBrowserSession); ok {
				lbs.CloseBrowser()
			}
		}
		if dir, ok := s.browserDir(p); ok {
			for _, name := range []string{"preview.png", "meta.json", "url"} {
				if target, ok := s.browserFile(p, name); ok {
					_ = os.Remove(target)
				} else {
					_ = os.Remove(filepath.Join(dir, name))
				}
			}
		}
		st := s.browserState(p)
		p.emit("browser", map[string]any{"browser": st})
		writeJSON(w, st)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	bs, ok := sess.(BrowserSession)
	if !ok || !bs.HasBrowser() {
		writeErr(w, http.StatusNotImplemented, errors.New("browser runtime is not available on this machine"))
		return
	}

	if lbs, isLive := sess.(LiveBrowserSession); isLive {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		var actionErr error
		switch act {
		case "goto", "open", "new_tab":
			dest := strings.TrimSpace(in.URL)
			if act == "new_tab" && dest == "" {
				dest = "https://www.google.com"
			}
			dest, actionErr = normalizeActionURL(dest)
			if actionErr == nil {
				_, actionErr = lbs.BrowserNav(ctx, act, map[string]any{"url": dest})
			}
		case "switch_tab", "close_tab":
			params := map[string]any{}
			if in.Index != nil {
				params["index"] = *in.Index
			}
			_, actionErr = lbs.BrowserNav(ctx, act, params)
		case "reload", "back", "forward":
			_, actionErr = lbs.BrowserNav(ctx, act, nil)
		case "click":
			actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "click", X: float64(in.X), Y: float64(in.Y)})
		case "move", "hover":
			actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "mouse_move", X: float64(in.X), Y: float64(in.Y)})
		case "scroll":
			dy := in.DeltaY
			if dy == 0 {
				dy = 600
			}
			actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "wheel", X: 683, Y: 430, DeltaY: float64(dy)})
		case "type":
			if in.Text == "" {
				writeErr(w, http.StatusBadRequest, errors.New("text is required"))
				return
			}
			if in.X > 0 || in.Y > 0 {
				actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "click", X: float64(in.X), Y: float64(in.Y)})
			}
			if actionErr == nil {
				actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "insert_text", Text: in.Text})
			}
			if actionErr == nil && in.Submit {
				actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "press", Key: "Enter"})
			}
		case "press":
			key := strings.TrimSpace(in.Key)
			if key == "" {
				key = "Enter"
			}
			if in.X > 0 || in.Y > 0 {
				actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "click", X: float64(in.X), Y: float64(in.Y)})
			}
			if actionErr == nil {
				actionErr = lbs.BrowserInput(ctx, BrowserInput{Type: "press", Key: key})
			}
		default:
			writeErr(w, http.StatusBadRequest, errors.New("unknown browser action"))
			return
		}
		if actionErr != nil {
			writeErr(w, http.StatusBadRequest, actionErr)
			return
		}
		st := s.browserState(p)
		p.emit("browser", map[string]any{"browser": st})
		writeJSON(w, st)
		return
	}

	args := map[string]any{"screenshot": "false"}
	switch act {
	case "goto", "open":
		u, err := normalizeActionURL(in.URL)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		args["url"] = u
	case "reload":
		args["actions"] = []any{map[string]any{"action": "reload"}}
	case "back":
		args["actions"] = []any{map[string]any{"action": "back"}}
	case "forward":
		args["actions"] = []any{map[string]any{"action": "forward"}}
	case "click":
		args["actions"] = []any{map[string]any{"action": "click_at", "x": in.X, "y": in.Y}}
	case "move", "hover":
		args["actions"] = []any{map[string]any{"action": "move_at", "x": in.X, "y": in.Y}}
	case "type":
		if in.Text == "" {
			writeErr(w, http.StatusBadRequest, errors.New("text is required"))
			return
		}
		var acts []any
		if in.X > 0 || in.Y > 0 {
			acts = append(acts, map[string]any{"action": "click_at", "x": in.X, "y": in.Y})
		}
		acts = append(acts, map[string]any{"action": "type", "text": in.Text})
		if in.Submit {
			acts = append(acts, map[string]any{"action": "press", "key": "Enter"})
		}
		args["actions"] = acts
	case "press":
		key := strings.TrimSpace(in.Key)
		if key == "" {
			key = "Enter"
		}
		var acts []any
		if in.X > 0 || in.Y > 0 {
			acts = append(acts, map[string]any{"action": "click_at", "x": in.X, "y": in.Y})
		}
		acts = append(acts, map[string]any{"action": "press", "key": key})
		args["actions"] = acts
	case "scroll":
		dy := in.DeltaY
		if dy == 0 {
			dy = 600
		}
		args["actions"] = []any{map[string]any{"action": "scroll", "y": dy}}
	default:
		writeErr(w, http.StatusBadRequest, errors.New("unknown browser action"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	out, err := bs.Browse(ctx, args)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if strings.HasPrefix(out, "error:") {
		writeErr(w, http.StatusBadRequest, errors.New(strings.TrimSpace(strings.TrimPrefix(out, "error:"))))
		return
	}
	st := s.browserState(p)
	st.Output = out
	p.emit("browser", map[string]any{"browser": st})
	writeJSON(w, st)
}

var allowedBrowserInputTypes = map[string]bool{
	"wheel":       true,
	"mouse_move":  true,
	"mouse_down":  true,
	"mouse_up":    true,
	"click":       true,
	"insert_text": true,
	"press":       true,
	"key_down":    true,
	"key_up":      true,
}

func (s *Server) browserInputAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var ev BrowserInput
	if err := readJSON(r, &ev); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ev.Type = strings.ToLower(strings.TrimSpace(ev.Type))
	if !allowedBrowserInputTypes[ev.Type] {
		writeErr(w, http.StatusBadRequest, errors.New("unsupported browser input event"))
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	lbs, ok := sess.(LiveBrowserSession)
	if !ok || !lbs.HasBrowser() {
		writeErr(w, http.StatusNotImplemented, errors.New("live browser input is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if err := lbs.BrowserInput(ctx, ev); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]bool{"ok": true})
}

func (s *Server) browserTakeoverAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Active bool   `json:"active"`
		Reason string `json:"reason"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	lbs, ok := sess.(LiveBrowserSession)
	if !ok || !lbs.HasBrowser() {
		writeErr(w, http.StatusNotImplemented, errors.New("browser takeover is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := lbs.SetBrowserTakeover(ctx, in.Active, in.Reason); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := s.browserState(p)
	p.emit("browser", map[string]any{"browser": st})
	writeJSON(w, st)
}

func (s *Server) browserDialogAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Accept     bool   `json:"accept"`
		PromptText string `json:"promptText"`
		DialogID   string `json:"dialogId"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ebs, ok := sess.(ExtendedBrowserSession)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("browser dialog handling is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := ebs.BrowserHandleDialog(ctx, in.Accept, in.PromptText, in.DialogID); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := s.browserState(p)
	p.emit("browser", map[string]any{"browser": st})
	writeJSON(w, st)
}

func (s *Server) browserContextMenuAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in map[string]any
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ebs, ok := sess.(ExtendedBrowserSession)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("browser context menu is not available"))
		return
	}
	if act, _ := in["action"].(string); strings.EqualFold(act, "open_new_tab") {
		if rawURL, _ := in["url"].(string); rawURL != "" {
			u, err := normalizeActionURL(rawURL)
			if err != nil {
				writeErr(w, http.StatusBadRequest, err)
				return
			}
			in["url"] = u
		}
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, err := ebs.BrowserContextMenu(ctx, in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, res)
}

func (s *Server) browserResizeAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ebs, ok := sess.(ExtendedBrowserSession)
	if !ok {
		writeJSON(w, s.browserState(p))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if _, err := ebs.BrowserResize(ctx, in.Width, in.Height); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, s.browserState(p))
}

func (s *Server) browserTeachAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		Action string `json:"action"` // "start" | "stop" | "cancel"
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ebs, ok := sess.(ExtendedBrowserSession)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("Teach a Task is not available"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	_, steps, err := ebs.BrowserTeach(ctx, in.Action)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := s.browserState(p)
	p.emit("browser", map[string]any{"browser": st})
	writeJSON(w, map[string]any{"browser": st, "steps": steps})
}

func (s *Server) browserProfilesAPI(w http.ResponseWriter, _ *http.Request) {
	ph, ok := s.host.(browserProfilesHost)
	if !ok {
		writeJSON(w, map[string]any{"profiles": []BrowserProfileView{}})
		return
	}
	list := ph.BrowserProfiles()
	if list == nil {
		list = []BrowserProfileView{}
	}
	writeJSON(w, map[string]any{"profiles": list})
}

func (s *Server) saveBrowserProfileAPI(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.host.(browserProfilesHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("browser profiles are not supported"))
		return
	}
	var in BrowserProfileUpsert
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if pid := r.PathValue("id"); pid != "" && in.ID == "" {
		in.ID = pid
	}
	prof, err := ph.SaveBrowserProfile(in)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"profile": prof, "profiles": ph.BrowserProfiles()})
}

func (s *Server) deleteBrowserProfileAPI(w http.ResponseWriter, r *http.Request) {
	ph, ok := s.host.(browserProfilesHost)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("browser profiles are not supported"))
		return
	}
	id := r.PathValue("id")
	if err := ph.DeleteBrowserProfile(id); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "profiles": ph.BrowserProfiles()})
}

func (s *Server) setProjectBrowserProfileAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	var in struct {
		ProfileID string `json:"profileId"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	sess, err := p.session()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ebs, ok := sess.(ExtendedBrowserSession)
	if !ok {
		writeErr(w, http.StatusNotImplemented, errors.New("browser profiles are not available"))
		return
	}
	prof, err := ebs.SetBrowserProfile(in.ProfileID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	st := s.browserState(p)
	p.emit("browser", map[string]any{"browser": st})
	writeJSON(w, map[string]any{"profile": prof, "browser": st})
}
