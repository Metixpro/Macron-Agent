package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/desk"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	settingDeskProjects = "desk_projects"
	desktopLog          = "desktop.log"
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

func (h *deskHost) DataDir() string        { return config.Home() }
func (h *deskHost) DefaultProject() string { return h.home }

func defaultDeskFolder() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return config.HomePath("chat")
	}
	return filepath.Join(home, "Macron")
}

func runDesktop(args []string) int {
	fs := flag.NewFlagSet("desktop", flag.ContinueOnError)
	addr := fs.String("addr", "127.0.0.1:0", "where the desktop engine listens (keep it on 127.0.0.1)")
	noOpen := fs.Bool("no-open", false, "do not open a window or a browser")
	serve := fs.Bool("serve", false, "run only the engine (for a server the desktop app connects to)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	_ = config.LoadDefaultDotEnv()
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return fail("%v", err)
	}
	if f, err := os.OpenFile(config.HomePath(desktopLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	folder := defaultDeskFolder()
	if err := os.MkdirAll(folder, 0o755); err != nil {
		return fail("%v", err)
	}
	userHome, _ := os.UserHomeDir()
	w, err := setupWorkspace(userHome)
	if err != nil {
		return fail("%v", err)
	}
	defer w.close()
	host := &deskHost{w: w, sessions: map[string]*terminal{}, home: folder}
	srv := desk.New(host)
	srv.Notify = desktopNotify
	ln, url, err := srv.Listen(*addr)
	if err != nil {
		return fail("%v", err)
	}
	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("desktop server: %v", err)
		}
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *serve {
		fmt.Printf("Macron engine on %s (token %s)\n", ln.Addr(), srv.Token())
	} else {
		fmt.Println("Macron desktop:", url)
	}
	if !*noOpen && !*serve {
		if err := openWindow(ctx, url); err != nil {
			fmt.Fprintln(os.Stderr, "open it in your browser:", url)
		} else if windowOwnsLife {
			stop()
		}
	}
	<-ctx.Done()
	sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = hs.Shutdown(sctx)
	h := host
	h.mu.Lock()
	for _, t := range h.sessions {
		w.app.Machine.StopShell(t.session)
	}
	h.mu.Unlock()
	return 0
}

var windowOwnsLife = false

func openWindow(_ context.Context, url string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	return cmd.Start()
}

func desktopNotify(title, body string) {}
