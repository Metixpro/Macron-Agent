package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/desk"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

const (
	settingDeskProjects = "desk_projects"
	settingDeskServers  = "desk_servers"
	desktopLog          = "desktop.log"
	desktopLock         = "desktop.json"
)

type deskHost struct {
	w        *workspace
	mu       sync.Mutex
	sessions map[string]*terminal
	home     string
}

func (h *deskHost) term(dir string) (*terminal, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if t, ok := h.sessions[dir]; ok {
		return t, nil
	}
	t, err := h.w.open(dir)
	if err != nil {
		return nil, err
	}
	h.sessions[dir] = t
	return t, nil
}

func (h *deskHost) Open(dir string) (desk.Session, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return h.term(dir)
}

func (h *deskHost) any() *terminal {
	t, err := h.term(h.home)
	if err != nil {
		return &terminal{st: h.w.st, prov: h.w.prov, app: h.w.app, bot: h.w.bot, window: h.w.window, gate: h.w.gate, mem: h.w.mem}
	}
	return t
}

func (h *deskHost) Info() desk.Info {
	host, _ := os.Hostname()
	i := desk.Info{Version: app.Version, Platform: runtime.GOOS, Host: host, Approvals: h.w.bot.gateMode(), ContextWindow: h.w.window()}
	i.Yolo, _ = h.w.gate.Yolo()
	if p, ok := h.w.prov.Active(); ok {
		i.Model, i.Provider, i.Effort = p.Model, p.Name, p.Effort
	}
	for _, spec := range tools.ConnectionSpecs() {
		i.Services++
		if tools.ConnectionReady(spec) {
			i.Connections++
		}
	}
	return i
}

func (h *deskHost) Models(ctx context.Context) (string, []string, error) { return h.any().Models(ctx) }
func (h *deskHost) SetModel(ctx context.Context, name string) error {
	return h.any().SetModel(ctx, name)
}
func (h *deskHost) SetApprovals(mode string) error { return h.any().SetApprovals(mode) }
func (h *deskHost) Yolo(on bool) bool              { return h.any().Yolo(on) }

func (h *deskHost) Providers(ctx context.Context) ([]desk.ProviderChoice, error) {
	list, err := h.any().Providers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]desk.ProviderChoice, len(list))
	for i, p := range list {
		out[i] = desk.ProviderChoice{ID: p.ID, Name: p.Name, Model: p.Model, Active: p.Active, Enabled: p.Enabled}
	}
	return out, nil
}

func (h *deskHost) SetProvider(ctx context.Context, id int64) error {
	return h.any().SetProvider(ctx, id)
}

func (h *deskHost) Connections() []desk.Connection {
	var out []desk.Connection
	for _, spec := range tools.ConnectionSpecs() {
		out = append(out, desk.Connection{ID: spec.ID, Name: spec.Name, Capability: spec.Capability, Ready: tools.ConnectionReady(spec)})
	}
	return out
}

func (h *deskHost) Projects() []string {
	raw, _ := h.w.st.Setting(settingDeskProjects)
	var out []string
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (h *deskHost) SaveProjects(paths []string) error {
	raw, err := json.Marshal(paths)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.w.st.SetSetting(ctx, settingDeskProjects, string(raw))
}

func (h *deskHost) Servers() []desk.ServerSpec {
	raw, _ := h.w.st.Setting(settingDeskServers)
	var out []desk.ServerSpec
	_ = json.Unmarshal([]byte(raw), &out)
	return out
}

func (h *deskHost) SaveServers(specs []desk.ServerSpec) error {
	raw, err := json.Marshal(specs)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return h.w.st.SetSetting(ctx, settingDeskServers, string(raw))
}

func (h *deskHost) ConnectModel(ctx context.Context, name, endpoint, key, model string) error {
	p := store.Provider{Name: name, Enabled: true, Vision: true, APIKey: key, Model: model}
	p.BaseURL, p.API = llm.CleanBaseURL(endpoint)
	if _, err := h.w.prov.Test(ctx, p); err != nil {
		return fmt.Errorf("the model did not answer: %w", err)
	}
	if _, err := providers.Configure(ctx, h.w.st, name, endpoint, key, model); err != nil {
		return err
	}
	return h.w.prov.Reload(ctx)
}

func (h *deskHost) Tuning() desk.Tuning {
	t := h.any().Tuning()
	support := map[int]string{0: "maybe", 1: "yes", 2: "no"}[t.EffortSupport]
	limit := 2.0
	if t.API == llm.APIAnthropic {
		limit = 1
	}
	return desk.Tuning{Model: t.Model, Effort: t.Effort, EffortParam: t.EffortParam, EffortValue: t.EffortValue, EffortSupport: support,
		Window: t.Window, AutoWindow: t.AutoWindow, Temperature: t.Temperature, TempLimit: limit}
}

func deskPlugin(p tui.Plugin) desk.Plugin {
	tools := p.Tools
	if tools == nil {
		tools = []string{}
	}
	return desk.Plugin{Key: p.Key, Name: p.Name, Kind: p.Kind, Source: p.Source, Target: p.Target, Enabled: p.Enabled, Status: p.Status,
		Error: p.Error, Removable: p.Removable, Tools: tools}
}

func (h *deskHost) Plugins() []desk.Plugin {
	var out []desk.Plugin
	for _, p := range h.any().Plugins() {
		out = append(out, deskPlugin(p))
	}
	return out
}

func (h *deskHost) AddMCP(name, target string, args []string, env map[string]string) (desk.Plugin, error) {
	p, err := h.any().AddMCP(name, target, args, env)
	return deskPlugin(p), err
}

func (h *deskHost) SetPluginEnabled(key string, on bool) (desk.Plugin, error) {
	p, err := h.any().SetPluginEnabled(key, on)
	return deskPlugin(p), err
}

func (h *deskHost) RemovePlugin(key string) error { return h.any().RemovePlugin(key) }

func (h *deskHost) SetEffort(level string) error      { return h.any().SetEffort(level) }
func (h *deskHost) SetContextWindow(tokens int) error { return h.any().SetContextWindow(tokens) }
func (h *deskHost) SetTemperature(v *float64) error   { return h.any().SetTemperature(v) }

func (h *deskHost) ListModels(ctx context.Context, endpoint, key string) ([]string, error) {
	p := store.Provider{Name: "probe", Enabled: true, APIKey: key, Model: "probe"}
	p.BaseURL, p.API = llm.CleanBaseURL(endpoint)
	return h.w.prov.Models(ctx, p)
}

func (h *deskHost) DataDir() string        { return config.Home() }
func (h *deskHost) DefaultProject() string { return h.home }

func defaultDeskFolder() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return config.HomePath("chat")
	}
	return filepath.Join(home, "Macron")
}

var launch string

func deskFail(format string, a ...any) int {
	msg := fmt.Sprintf(format, a...)
	if launch == "desktop" {
		alert("Macron", msg)
		return 1
	}
	return fail("%s", msg)
}

type deskLock struct {
	URL string `json:"url"`
	PID int    `json:"pid"`
}

func lockPath() string { return config.HomePath(desktopLock) }

func runningDesktop() (string, bool) {
	raw, err := os.ReadFile(lockPath())
	if err != nil {
		return "", false
	}
	var l deskLock
	if json.Unmarshal(raw, &l) != nil || l.URL == "" {
		return "", false
	}
	u, err := url.Parse(l.URL)
	if err != nil || u.Hostname() != "127.0.0.1" {
		return "", false
	}
	req, err := http.NewRequest(http.MethodGet, u.Scheme+"://"+u.Host+"/api/state", nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("Authorization", "Bearer "+u.Query().Get("t"))
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Do(req)
	if err != nil {
		return "", false
	}
	res.Body.Close()
	return l.URL, res.StatusCode == http.StatusOK
}

func writeLock(address string) {
	raw, _ := json.Marshal(deskLock{URL: address, PID: os.Getpid()})
	_ = os.WriteFile(lockPath(), raw, 0o600)
}

func removeLock(address string) {
	raw, err := os.ReadFile(lockPath())
	if err != nil {
		return
	}
	var l deskLock
	if json.Unmarshal(raw, &l) == nil && l.URL == address {
		_ = os.Remove(lockPath())
	}
}

func runDesktop(args []string) int {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:0", "where the desktop engine listens (keep it on 127.0.0.1)")
	noOpen := fs.Bool("no-open", false, "do not open a window or a browser")
	serve := fs.Bool("serve", false, "run only the engine (for a server the desktop app connects to)")
	asJSON := fs.Bool("json", false, "with --serve: print the address and token as JSON and stop when stdin closes")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = config.LoadDefaultDotEnv()
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return deskFail("%v", err)
	}
	if f, err := os.OpenFile(config.HomePath(desktopLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if !*serve && !*noOpen {
		if address, ok := runningDesktop(); ok {
			if !showWindow(ctx, address) {
				if err := openAppBrowser(address); err != nil {
					return deskFail("Macron is already running at %s", address)
				}
			}
			return 0
		}
	}
	folder := defaultDeskFolder()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return deskFail("%v", err)
	}
	userHome, _ := os.UserHomeDir()
	w, err := setupWorkspace(userHome)
	if err != nil {
		return deskFail("%v", err)
	}
	defer w.close()
	host := &deskHost{w: w, sessions: map[string]*terminal{}, home: folder}
	srv := desk.New(host)
	srv.Notify = desktopNotify
	ln, address, err := srv.Listen(*addr)
	if err != nil {
		return deskFail("%v", err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("desktop server: %v", err)
		}
	}()
	switch {
	case *serve && *asJSON:
		_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"addr": ln.Addr().String(), "token": srv.Token(), "version": app.Version})
		go func() {
			_, _ = io.Copy(io.Discard, os.Stdin)
			stop()
		}()
	case *serve:
		fmt.Printf("Macron engine on %s (token %s)\n", ln.Addr(), srv.Token())
	default:
		writeLock(address)
		defer removeLock(address)
		fmt.Println("Macron desktop:", address)
		if !*noOpen {
			if showWindow(ctx, address) {
				stop()
			} else if err := openAppBrowser(address); err != nil {
				fmt.Fprintln(os.Stderr, "open it in your browser:", address)
			}
		}
	}
	<-ctx.Done()
	srv.Close()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	host.mu.Lock()
	for _, t := range host.sessions {
		w.app.Machine.StopShell(t.session)
	}
	host.mu.Unlock()
	return 0
}
