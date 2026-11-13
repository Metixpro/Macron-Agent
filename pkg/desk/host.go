package desk

import (
	"context"

	"github.com/Metixpro/macron-agent/pkg/agent"
)

type Info struct {
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	Host          string `json:"host"`
	Model         string `json:"model"`
	Provider      string `json:"provider"`
	Effort        string `json:"effort"`
	Approvals     string `json:"approvals"`
	Yolo          bool   `json:"yolo"`
	ContextWindow int    `json:"contextWindow"`
	Connections   int    `json:"connections"`
	Services      int    `json:"services"`
}

type Tuning struct {
	Model         string   `json:"model"`
	Effort        string   `json:"effort"`
	EffortParam   string   `json:"effortParam"`
	EffortValue   string   `json:"effortValue"`
	EffortSupport string   `json:"effortSupport"`
	Window        int      `json:"window"`
	AutoWindow    int      `json:"autoWindow"`
	Temperature   *float64 `json:"temperature"`
	TempLimit     float64  `json:"tempLimit"`
}

type Plugin struct {
	Key       string   `json:"key"`
	Name      string   `json:"name"`
	Kind      string   `json:"kind"`
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	Enabled   bool     `json:"enabled"`
	Status    string   `json:"status"`
	Error     string   `json:"error,omitempty"`
	Removable bool     `json:"removable"`
	Tools     []string `json:"tools"`
}

type ProviderChoice struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Model   string `json:"model"`
	Active  bool   `json:"active"`
	Enabled bool   `json:"enabled"`
}

type Connection struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Capability string `json:"capability"`
	Ready      bool   `json:"ready"`
}

type Session interface {
	Turn(ctx context.Context, text string, l *agent.EventListener, inbox func() []string, yield func() <-chan struct{}) (*agent.Result, error)
	New(ctx context.Context) error
	Compact(ctx context.Context) (string, error)
	SetMode(mode string) error
}

type Host interface {
	Info() Info
	Open(dir string) (Session, error)
	Models(ctx context.Context) (string, []string, error)
	SetModel(ctx context.Context, name string) error
	Providers(ctx context.Context) ([]ProviderChoice, error)
	SetProvider(ctx context.Context, id int64) error
	SetApprovals(mode string) error
	Yolo(on bool) bool
	Connections() []Connection
	Projects() []string
	SaveProjects(paths []string) error
	DataDir() string
	DefaultProject() string
	Servers() []ServerSpec
	SaveServers(specs []ServerSpec) error
	ConnectModel(ctx context.Context, name, endpoint, key, model string) error
	Tuning() Tuning
	Plugins() []Plugin
	AddMCP(name, target string, args []string, env map[string]string) (Plugin, error)
	SetPluginEnabled(key string, on bool) (Plugin, error)
	RemovePlugin(key string) error
	SetEffort(level string) error
	SetContextWindow(tokens int) error
	SetTemperature(t *float64) error
	ListModels(ctx context.Context, endpoint, key string) ([]string, error)
}
