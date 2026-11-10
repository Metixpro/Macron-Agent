package app

import (
	"context"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const Version = "1.5.0"

type App struct {
	Config   *config.Config
	LLM      llm.ChatClient
	Machine  *machine.Machine
	Search   *search.Engine
	Registry *tools.Registry
	Agent    *agent.Agent

	Skills *skills.Store
}

func New(ctx context.Context, cfg *config.Config, channel agent.Channel) (*App, error) {
	return NewWith(ctx, cfg, channel, llm.NewClient(cfg.APIURL, cfg.APIKey, cfg.Model), agent.Options{})
}

func NewWith(ctx context.Context, cfg *config.Config, channel agent.Channel, client llm.ChatClient, opts agent.Options) (*App, error) {

	sb, err := machine.New(machine.Config{
		BaseDir:       cfg.WorkspaceDir,
		MaxConcurrent: cfg.Slots,
	})
	if err != nil {
		return nil, err
	}
	sb.StartJanitor(ctx, cfg.WorkspaceTTL, 10*time.Minute)

	var searchHTTP, toolsHTTP *http.Client = netclient.NewClient(netclient.Options{Timeout: 25 * time.Second, UseEnvProxy: cfg.UseEnvProxy}),
		netclient.NewClient(netclient.Options{UseEnvProxy: cfg.UseEnvProxy})
	engine := search.NewEngine(searchHTTP)
	engine.Treg = search.NewTreg(func() string { return cfg.TregToken })
	engine.ReaderURL, engine.ReaderKey = cfg.ReaderURL, cfg.ReaderKey
	sk := openSkills(cfg.SkillsDir)
	var skillWrite func(string) bool
	if channel == agent.ChannelCLI || (channel == agent.ChannelAPI && cfg.SkillsWrite) {
		skillWrite = func(string) bool { return true }
	}
	reg := tools.NewRegistry(sb, engine, client, tools.Options{
		HTTP: toolsHTTP, GitHubToken: cfg.GitHubToken,
		PiperModel: cfg.PiperModel,
		Skills:     sk, SkillWrite: skillWrite,
	})
	if opts.Skills == nil {
		opts.Skills = sk
	}
	if opts.ContextWindow == nil {
		opts.ContextWindow = func() int {
			if cfg.ContextWindow > 0 {
				return cfg.ContextWindow
			}
			return agent.ContextWindow(cfg.Model)
		}
	}
	if opts.MaxSteps == 0 {
		opts.MaxSteps = cfg.MaxSteps
	}
	if opts.Channel == "" {
		opts.Channel = channel
	}
	ag := agent.NewAgent(client, reg, opts)

	return &App{Config: cfg, LLM: client, Machine: sb, Search: engine, Registry: reg, Agent: ag, Skills: sk}, nil
}

func openSkills(dir string) *skills.Store {
	if dir == "" {
		return nil
	}
	st, err := skills.New(dir)
	if err != nil {
		log.Printf("⚠️  skills are off: %v", err)
		return nil
	}
	if added, err := st.Seed(); err != nil {
		log.Printf("⚠️  skills: adding the built-in skills: %v", err)
	} else if len(added) > 0 {
		log.Printf("🧩 added the built-in skills: %s", strings.Join(added, ", "))
	}
	var names []string
	for _, s := range st.Snapshot() {
		name := s.Category + "/" + s.Name
		if !s.Enabled {
			name += " (off)"
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		log.Printf("🧩 no skills in %s", st.Dir())
	} else {
		log.Printf("🧩 %d skills loaded from %s: %s", len(names), st.Dir(), strings.Join(names, ", "))
	}
	return st
}
