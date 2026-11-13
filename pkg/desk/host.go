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
	ListModels(ctx context.Context, endpoint, key string) ([]string, error)
}
