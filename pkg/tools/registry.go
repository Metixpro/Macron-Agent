// Package tools implements the functions the agent can call.
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

// MaxToolOutput bounds what one tool call can add to the model context;
// longer outputs keep their head and tail (see textutil.HeadTail).
const MaxToolOutput = 15000

// ToolHandler runs one tool. Expected failures should be returned as a
// message (string, nil) so the model can read them and adapt; a non-nil error
// means something unexpected broke.
type ToolHandler func(ctx context.Context, sessionID string, args map[string]any) (string, error)

// Options carries optional dependencies.
type Options struct {
	HTTP         *http.Client  // client without an overall timeout (nil = default)
	GitHubToken  string        // optional, raises the GitHub API rate limit
	ImageAPI     string        // generate_image's first engine (the txt2img studio); "" = the fallbacks only
	ImageKey     func() string // its api_key, read per call
	ImageEditAPI string        // the Nano Banana task service (edit_image, generate_image's second engine); "" = off
	ImageEditKey func() string
	MusicAPI     string        // the song service (generate_song); "" = no songs
	MusicKey     func() string // its X-API-Key, read per call
	PiperModel   string        // the local Persian voice of text_to_speech (a Piper .onnx); "" = Edge TTS only
	Skills       *skills.Store
	SkillWrite   func(sessionID string) bool // who may save skills (nil = nobody)
}

type Registry struct {
	// Gate decides whether a shell command may run on this machine (nil:
	// every command runs; see pkg/cmdgate). CheckpointDir is where the
	// snapshots it asks for go before a destructive command.
	Gate          *cmdgate.Gate
	CheckpointDir string

	Machine     *machine.Machine
	Search      *search.Engine
	LLM         llm.ChatClient
	HTTP        *http.Client
	GitHubToken string

	tools   map[string]llm.Tool
	handler map[string]ToolHandler

	langs      map[string]language // run_code languages installed
	syntax     map[string]bool     // the programs of the syntax checks here
	syntaxOnce sync.Once           // finds them for CheckSyntax when the dev tools did not
	market     marketCache         // the latest gold and currency rates
	docs       *docCache           // indexes of long documents (document_reader)
	dl         downloader          // yt-dlp and gallery-dl found on the server
	cookiePool CookiePool          // cookies.txt for sites that ask for a login
	// userCookies are each user's own cookies, tried first for their downloads.
	userCookies func(sessionID string) []Cookie
	rotations   rotationLimiter
	browser     *browserRuntime // headless Chromium (nil when not installed)
	imageAPI    string
	imageKey    func() string
	styles      styleCache // the studio's styles and shapes
	// imageEditAPI is the Nano Banana task service; its key is read per call.
	imageEditAPI string
	imageEditKey func() string
	// musicAPI is the song service; its key is read per call.
	musicAPI   string
	musicKey   func() string
	piper      piperVoice
	skills     *skills.Store
	skillWrite func(string) bool

	plugins atomic.Pointer[pluginSource] // the admins' plugins (SetPlugins)
}

func NewRegistry(sb *machine.Machine, se *search.Engine, client llm.ChatClient, opts Options) *Registry {
	if opts.HTTP == nil {
		opts.HTTP = netclient.NewClient(netclient.Options{})
	}
	r := &Registry{
		Machine:      sb,
		Search:       se,
		LLM:          client,
		HTTP:         opts.HTTP,
		GitHubToken:  opts.GitHubToken,
		tools:        make(map[string]llm.Tool),
		handler:      make(map[string]ToolHandler),
		docs:         newDocCache(),
		imageAPI:     opts.ImageAPI,
		imageKey:     opts.ImageKey,
		imageEditAPI: opts.ImageEditAPI,
		imageEditKey: opts.ImageEditKey,
		musicAPI:     opts.MusicAPI,
		musicKey:     opts.MusicKey,
		piper:        piperVoice{model: opts.PiperModel},
		skills:       opts.Skills,
		skillWrite:   opts.SkillWrite,
	}
	r.registerCoreTools()
	r.registerMediaTools()
	r.registerInfoTools()
	r.registerDocumentTools()
	r.registerDataTools()
	r.registerDownloadTools()
	r.registerBrowserTools()
	r.registerMusicTool()
	r.registerProjectTools()
	if r.skills != nil {
		r.registerSkillTools()
	}
	if r.imageEditAPI != "" {
		r.registerImageEditTool()
	}
	if r.musicAPI != "" {
		r.registerSongTool()
	}
	r.registerDevTools()
	return r
}

func (r *Registry) Register(tool llm.Tool, handler ToolHandler) {
	r.tools[tool.Function.Name] = tool
	r.handler[tool.Function.Name] = handler
}

// add registers a tool from a compact description.
func (r *Registry) add(name, description string, props map[string]any, required []string, h ToolHandler) {
	r.Register(llm.Tool{Type: "function", Function: llm.ToolDef{
		Name:        name,
		Description: description,
		Parameters:  schema(props, required...),
	}}, h)
}

// GetTools returns the tool definitions in a stable (alphabetical) order, which
// keeps the prompt prefix identical between requests and so cache-friendly.
func (r *Registry) GetTools() []llm.Tool {
	list := make([]llm.Tool, 0, len(r.tools))
	for _, t := range r.tools {
		list = append(list, t)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Function.Name < list[j].Function.Name })
	return r.withPlugins(list)
}

// Has reports whether a tool is registered (a plugin's that is on included).
func (r *Registry) Has(name string) bool {
	if _, ok := r.handler[name]; ok {
		return true
	}
	p := r.pluginTools()
	return p != nil && p.Has(name)
}

// Execute runs a tool. It never panics: a crashing handler becomes an error.
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
	// Models sometimes send "" or "null" for tools without parameters.
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
	return r.KeepWhole(sessionID, name, out), nil // a longer one is kept whole in a file
}

// runScript executes an embedded python script here. The input is
// passed as JSON on stdin, so user data is never interpolated into code.
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

// failure turns a failed run into a short message for the model.
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
