package tools

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"log"
	"net/http"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
)

//go:embed scripts/*.py
var scriptFS embed.FS

const MaxToolOutput = 15000

type ToolHandler func(ctx context.Context, sessionID string, args map[string]any) (string, error)

type Options struct {
	HTTP        *http.Client
	GitHubToken string
	PiperModel  string
	Skills      *skills.Store
	SkillWrite  func(sessionID string) bool
}

type Registry struct {
	Gate          *cmdgate.Gate
	CheckpointDir string

	Machine     *machine.Machine
	Search      *search.Engine
	LLM         llm.ChatClient
	HTTP        *http.Client
	GitHubToken string

	tools   map[string]llm.Tool
	handler map[string]ToolHandler

	langs      map[string]language
	syntax     map[string]bool
	syntaxOnce sync.Once
	market     marketCache
	docs       *docCache
	dl         downloader
	cookiePool CookiePool

	userCookies func(sessionID string) []Cookie
	rotations   rotationLimiter
	browser     *browserRuntime
	piper       piperVoice
	skills      *skills.Store
	skillWrite  func(string) bool

	plugins atomic.Pointer[pluginSource]
}

func NewRegistry(sb *machine.Machine, se *search.Engine, client llm.ChatClient, opts Options) *Registry {
	if opts.HTTP == nil {
		opts.HTTP = netclient.NewClient(netclient.Options{})
	}
	r := &Registry{
		Machine:     sb,
		Search:      se,
		LLM:         client,
		HTTP:        opts.HTTP,
		GitHubToken: opts.GitHubToken,
		tools:       make(map[string]llm.Tool),
		handler:     make(map[string]ToolHandler),
		docs:        newDocCache(),
		piper:       piperVoice{model: opts.PiperModel},
		skills:      opts.Skills,
		skillWrite:  opts.SkillWrite,
	}
	r.registerCoreTools()
	r.registerMediaTools()
	r.registerInfoTools()
	r.registerDocumentTools()
	r.registerDataTools()
	r.registerDownloadTools()
	r.registerBrowserTools()
	r.registerProjectTools()
	if r.skills != nil {
		r.registerSkillTools()
	}
	r.registerDevTools()
	return r
}

func (r *Registry) Register(tool llm.Tool, handler ToolHandler) {
	r.tools[tool.Function.Name] = tool
	r.handler[tool.Function.Name] = handler
}

func (r *Registry) add(name, description string, props map[string]any, required []string, h ToolHandler) {
	r.Register(llm.Tool{Type: "function", Function: llm.ToolDef{
		Name:        name,
		Description: description,
		Parameters:  schema(props, required...),
	}}, h)
}

func (r *Registry) GetTools() []llm.Tool {
	list := make([]llm.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Function.Name < list[j].Function.Name })
	return r.withPlugins(list)
}

func (r *Registry) Has(name string) bool {
	if _, ok := r.handler[name]; ok {
		return true
	}
	p := r.pluginTools()
	return p != nil && p.Has(name)
}

func (r *Registry) Execute(ctx context.Context, sessionID, name, argsJSON string) (result string, err error) {
	defer func() {
		if p := recover(); p != nil {
			log.Printf("tool %q panicked: %v\n%s", name, p, debug.Stack())
			result, err = "", fmt.Errorf("tool %q crashed unexpectedly", name)
		}
	}()

	h, ok := r.handler[name]
	if !ok {
		p := r.pluginTools()
		if p == nil || !p.Has(name) || !p.Allowed(name, sessionID) {
			return "", fmt.Errorf("tool %q not found", name)
		}
		h = func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			return p.Call(ctx, sessionID, name, args)
		}
	}

	args := map[string]any{}

	if s := strings.TrimSpace(argsJSON); s != "" && s != "null" {
		if err := json.Unmarshal([]byte(s), &args); err != nil {
			return "", fmt.Errorf("invalid json arguments: %w", err)
		}
		if args == nil {
			args = map[string]any{}
		}
	}

	out, err := h(ctx, sessionID, args)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		out = "(no output)"
	}
	return r.KeepWhole(sessionID, name, out), nil
}

func (r *Registry) runScript(ctx context.Context, sessionID, script string, input any, timeout time.Duration) (*machine.ExecutionResult, error) {
	code, err := scriptFS.ReadFile("scripts/" + script)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return r.Machine.Run(ctx, sessionID, machine.RunOptions{
		Args:    []string{"python3", "-I", "-c", string(code)},
		Stdin:   payload,
		Timeout: timeout,
	}), nil
}

func failure(what string, res *machine.ExecutionResult) string {
	detail := strings.TrimSpace(res.Stderr)
	if detail == "" {
		detail = strings.TrimSpace(res.Stdout)
	}
	if res.Error != "" {
		detail = res.Error + " " + detail
	}
	return fmt.Sprintf("%s failed (exit %d): %s", what, res.ExitCode, tail(detail, 1200))
}

func tail(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func (r *Registry) Usable(name, sessionID string) bool {
	switch name {
	case ToolSaveSkill:
		return r.SkillWritable(sessionID)
	}
	if r.Builtin(name) {
		return true
	}
	p := r.pluginTools()
	return p != nil && p.Has(name) && p.Allowed(name, sessionID)
}
