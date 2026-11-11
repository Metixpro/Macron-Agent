package plugins

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
)

type State struct {
	Enabled *bool             `json:"enabled,omitempty"`
	Access  string            `json:"access,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

type StateStore interface {
	PluginState(key string) State
	SetPluginState(key string, st State) error
	PanelServers() []ServerConfig
	SetPanelServers(list []ServerConfig) error
}

type MemoryStates struct {
	mu      sync.Mutex
	states  map[string]State
	servers []ServerConfig
}

func (s *MemoryStates) PluginState(key string) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.states[key]
}

func (s *MemoryStates) SetPluginState(key string, st State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = map[string]State{}
	}
	s.states[key] = st
	return nil
}

func (s *MemoryStates) PanelServers() []ServerConfig {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]ServerConfig(nil), s.servers...)
}

func (s *MemoryStates) SetPanelServers(list []ServerConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.servers = append([]ServerConfig(nil), list...)
	return nil
}

const (
	KindScript = "script"
	KindMCP    = "mcp"
)

const (
	SourceFolder = "folder"
	SourceFile   = "file"
	SourcePanel  = "panel"
)

const (
	StatusOff        = "off"
	StatusConnecting = "connecting"
	StatusReady      = "ready"
	StatusError      = "error"
)

const (
	maxPluginTools = 1000
	maxZipFiles    = 2000
	maxZipBytes    = 100 << 20
	maxZipFile     = 50 << 20
	maxZipPlugins  = 10
	depsTimeout    = 10 * time.Minute
	testSession    = "plugin-test"
	buildSession   = "plugin-build"
)

type ToolInfo struct {
	Name        string
	Description string
	Conflict    bool
}

type Info struct {
	Key, ID, Name, Version, Author, Description string
	Kind, Source                                string
	Runtime, Entry                              string
	Command, URL                                string
	Enabled                                     bool
	Access                                      string
	Net                                         bool
	TimeoutSeconds, MemoryMB                    int
	Tools                                       []ToolInfo
	EnvNames                                    []string
	EnvSet                                      map[string]bool
	EnvOwn                                      map[string]bool
	EnvHelp                                     map[string]string
	Status, Error, ServerName                   string
	Deps                                        bool
}

func (i Info) Ready() bool {
	return i.Enabled && i.Error == "" && (i.Kind == KindScript || i.Status == StatusReady)
}

type plugin struct {
	key    string
	kind   string
	source string
	dir    string
	man    *Manifest
	srv    *ServerConfig
	entry  string
	err    string
	state  State
	mcp    *mcpSession
}

func (p *plugin) enabled() bool {
	switch {
	case p.state.Enabled != nil:
		return *p.state.Enabled
	case p.man != nil && p.man.Enabled != nil:
		return *p.man.Enabled
	case p.srv != nil && p.srv.Enabled != nil:
		return *p.srv.Enabled
	}
	return true
}

func (p *plugin) access() string {
	switch {
	case p.state.Access != "":
		return p.state.Access
	case p.man != nil:
		return p.man.Access
	case p.srv != nil:
		return p.srv.Access
	}
	return AccessAdmins
}

type toolRef struct {
	key, name string
	def       llm.Tool
	readOnly  bool
}

type Manager struct {
	Dir     string
	MCPFile string
	Machine *machine.Machine
	HTTP    *http.Client
	States  StateStore
	Version string

	IsAdmin func(sessionID string) bool

	Reserved func(name string) bool

	mu      sync.RWMutex
	plugins map[string]*plugin
	index   map[string]toolRef
	order   []string
	life    context.Context
	stop    context.CancelFunc
}

func (m *Manager) init() {
	if m.plugins == nil {
		m.plugins = map[string]*plugin{}
		m.index = map[string]toolRef{}
		m.life, m.stop = context.WithCancel(context.Background())
	}
	if m.States == nil {
		m.States = &MemoryStates{}
	}
	if m.HTTP == nil {
		m.HTTP = &http.Client{}
	}
}

func (m *Manager) states() StateStore {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.init()
	return m.States
}

func (m *Manager) canRun() error {
	if m.Machine == nil {
		return errors.New("no machine to run programs on")
	}
	return nil
}

func (m *Manager) Reload(ctx context.Context) {
	m.mu.Lock()
	m.init()
	old := m.plugins
	next := map[string]*plugin{}
	for _, p := range m.scan() {
		next[p.key] = p
	}
	for _, c := range m.servers() {
		cfg := c.ServerConfig
		key := "@" + cfg.ID
		p := &plugin{key: key, kind: KindMCP, srv: &cfg, source: SourceFile}
		if c.panel {
			p.source = SourcePanel
		}
		next[key] = p
	}
	var connect []*plugin
	for key, p := range next {
		p.state = m.States.PluginState(key)
		if p.kind != KindMCP {
			continue
		}
		prev, ok := old[key]
		if ok && prev.mcp != nil && sameServer(prev, p) && p.enabled() {
			p.mcp = prev.mcp
			continue
		}
		p.mcp = &mcpSession{status: StatusOff}
		if p.enabled() && p.err == "" {
			p.mcp.status = StatusConnecting
			connect = append(connect, p)
		}
	}
	for key, p := range old {
		if p.mcp != nil && (next[key] == nil || next[key].mcp != p.mcp) {
			go p.mcp.close()
		}
	}
	m.plugins = next
	m.rebuildLocked()
	m.mu.Unlock()
	for _, p := range connect {
		go m.connect(p)
	}
}

func sameServer(a, b *plugin) bool {
	x, _ := json.Marshal(struct {
		S *ServerConfig
		E map[string]string
	}{a.srv, a.state.Env})
	y, _ := json.Marshal(struct {
		S *ServerConfig
		E map[string]string
	}{b.srv, b.state.Env})
	return bytes.Equal(x, y)
}

func (m *Manager) scan() []*plugin {
	if m.Dir == "" {
		return nil
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			log.Printf("plugins: %v", err)
		}
		return nil
	}
	var out []*plugin
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if !idRe.MatchString(e.Name()) {
			log.Printf("plugins: skipping folder %q: name it like its id (lowercase letters, digits, - or _)", e.Name())
			continue
		}
		dir := filepath.Join(m.Dir, e.Name())
		p := &plugin{key: e.Name(), kind: KindScript, source: SourceFolder, dir: dir}
		data, err := os.ReadFile(filepath.Join(dir, manifestName))
		if err != nil {
			continue
		}
		man, err := ParseManifest(data)
		switch {
		case err != nil:
			p.err = err.Error()
			p.man = &Manifest{ID: e.Name(), Name: e.Name()}
		case man.ID != e.Name():
			p.err = fmt.Sprintf("its id %q is not its folder's name %q", man.ID, e.Name())
			p.man = man
		default:
			p.man = man
			if p.entry = findEntry(dir, man.Entry); p.entry == "" {
				p.err = "no program to run: add run.py, run.sh, run.js or run (or name it in \"entry\")"
			}
		}
		out = append(out, p)
	}
	return out
}

func findEntry(dir, entry string) string {
	list := defaultEntries
	if entry != "" {
		list = []string{entry}
	}
	for _, e := range list {
		full := filepath.Join(dir, filepath.FromSlash(e))
		if fi, err := os.Lstat(full); err == nil && fi.Mode().IsRegular() && strings.HasPrefix(full, dir+string(filepath.Separator)) {
			return e
		}
	}
	return ""
}

func (m *Manager) servers() []namedServerConfig {
	byID := map[string]namedServerConfig{}
	var order []string
	if m.MCPFile != "" {
		if data, err := os.ReadFile(m.MCPFile); err == nil {
			list, err := ParseServers(data)
			if err != nil {
				log.Printf("plugins: %s: %v", m.MCPFile, err)
			}
			for _, c := range list {
				if _, ok := byID[c.ID]; !ok {
					order = append(order, c.ID)
				}
				byID[c.ID] = namedServerConfig{ServerConfig: c}
			}
		}
	}
	for _, c := range m.States.PanelServers() {
		if c.Check() != nil {
			continue
		}
		if _, ok := byID[c.ID]; !ok {
			order = append(order, c.ID)
		}
		byID[c.ID] = namedServerConfig{ServerConfig: c, panel: true}
	}
	out := make([]namedServerConfig, 0, len(order))
	for _, id := range order {
		out = append(out, byID[id])
	}
	return out
}

type namedServerConfig = struct {
	ServerConfig
	panel bool
}

func (m *Manager) rebuildLocked() {
	index := map[string]toolRef{}
	keys := make([]string, 0, len(m.plugins))
	for k := range m.plugins {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		p := m.plugins[k]
		if !p.enabled() || p.err != "" {
			continue
		}
		for _, t := range m.toolsOf(p) {
			name := t.Function.Name
			if _, taken := index[name]; taken || (m.Reserved != nil && m.Reserved(name)) || len(index) >= maxPluginTools {
				continue
			}
			index[name] = toolRef{key: k, name: t.orig, def: t.Tool, readOnly: t.readOnly}
		}
	}
	m.index = index
	m.order = m.order[:0]
	for name := range index {
		m.order = append(m.order, name)
	}
	sort.Strings(m.order)
}

type exposedTool struct {
	llm.Tool
	orig     string
	readOnly bool
}

func (m *Manager) toolsOf(p *plugin) []exposedTool {
	var out []exposedTool
	switch p.kind {
	case KindScript:
		for _, t := range p.man.Tools {
			out = append(out, exposedTool{Tool: llm.Tool{Type: "function", Function: llm.ToolDef{Name: t.Name, Description: t.Description,
				Parameters: t.Parameters}}, orig: t.Name})
		}
	case KindMCP:
		if p.mcp == nil {
			return nil
		}
		p.mcp.mu.Lock()
		tools := p.mcp.tools
		p.mcp.mu.Unlock()
		label := p.srv.Name
		if label == "" {
			label = p.srv.ID
		}
		for _, t := range tools {
			schema, err := cleanSchema(t.InputSchema)
			if err != nil || !mcpNameRe.MatchString(t.Name) {
				continue
			}
			desc := strings.TrimSpace(t.Description)
			if r := []rune(desc); len(r) > maxDescRunes {
				desc = string(r[:maxDescRunes])
			}
			out = append(out, exposedTool{Tool: llm.Tool{Type: "function", Function: llm.ToolDef{Name: mcpToolName(p.srv.ID, t.Name),
				Description: "[" + label + "] " + desc, Parameters: schema}}, orig: t.Name, readOnly: t.Annotations.ReadOnlyHint})
		}
	}
	return out
}

func mcpToolName(server, tool string) string {
	clean := func(s string) string {
		var b strings.Builder
		for _, r := range s {
			switch {
			case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
				b.WriteRune(r)
			default:
				b.WriteRune('_')
			}
		}
		return b.String()
	}
	name := clean(server) + "__" + clean(tool)
	if len(name) > 64 {
		name = name[:64]
	}
	return name
}

func (m *Manager) Tools() []llm.Tool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]llm.Tool, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.index[name].def)
	}
	return out
}

func (m *Manager) Has(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.index[name]
	return ok
}

// Trusted reports whether calling the tool needs no approval: its MCP server marks it read-only (readOnlyHint), or the
// owner opened its plugin to everyone. A script never marks itself: the agent can write one.
func (m *Manager) Trusted(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ref, ok := m.index[name]
	if !ok {
		return false
	}
	p := m.plugins[ref.key]
	return ref.readOnly || p != nil && p.access() == AccessEveryone
}

func (m *Manager) Allowed(name, sessionID string) bool {
	m.mu.RLock()
	ref, ok := m.index[name]
	var p *plugin
	if ok {
		p = m.plugins[ref.key]
	}
	m.mu.RUnlock()
	if p == nil {
		return false
	}
	if p.access() == AccessAdmins {
		return m.IsAdmin != nil && m.IsAdmin(machine.ParentSession(sessionID))
	}
	return true
}

func (m *Manager) Call(ctx context.Context, sessionID, name string, args map[string]any) (string, error) {
	m.mu.RLock()
	ref, ok := m.index[name]
	p := m.plugins[ref.key]
	m.mu.RUnlock()
	if !ok || p == nil {
		return "", fmt.Errorf("tool %q is not available", name)
	}
	if !m.Allowed(name, sessionID) {
		return "", fmt.Errorf("tool %q is not available", name)
	}
	if p.kind == KindMCP {
		return m.callMCP(ctx, p, sessionID, ref.name, args)
	}
	return m.runScript(ctx, p, sessionID, ref.name, args)
}

func (p *plugin) command(root string) []string {
	entry := root + "/" + p.entry
	switch runtimeOf(p.entry) {
	case runtimePython:
		return []string{"python3", "-s", "-E", "-c", pythonBoot, entry}
	case runtimeBash:
		return []string{"bash", entry}
	case runtimeNode:
		return []string{"node", entry}
	}
	return []string{entry}
}

func (p *plugin) memoryMB() int {
	if p.man.MemoryMB != nil {
		return *p.man.MemoryMB
	}
	if runtimeOf(p.entry) == runtimeNode {
		return 0
	}
	return defaultMemoryMB
}

func (m *Manager) runScript(ctx context.Context, p *plugin, sessionID, tool string, args map[string]any) (string, error) {
	if err := m.canRun(); err != nil {
		return "", err
	}
	if args == nil {
		args = map[string]any{}
	}
	workspace := machine.MountPoint
	if w, err := m.Machine.Workspace(sessionID); err == nil {
		workspace = w
	}
	input, err := json.Marshal(map[string]any{"tool": tool, "session_id": sessionID, "args": args, "workspace": workspace})
	if err != nil {
		return "", err
	}
	root := p.dir
	env := map[string]string{"MACRON_PLUGIN": p.man.ID, "MACRON_TOOL": tool, "MACRON_SESSION": sessionID}
	for k, v := range p.state.Env {
		if _, declared := p.man.Env[k]; declared {
			env[k] = v
		}
	}
	timeout := time.Duration(p.man.TimeoutSeconds) * time.Second
	opts := machine.RunOptions{
		Args: p.command(root), Stdin: input, Timeout: timeout, Env: env,
	}
	res := m.Machine.Run(ctx, sessionID, opts)
	out := strings.TrimSpace(res.Stdout)
	switch {
	case res.ExitCode == 124:
		return fmt.Sprintf("The %s tool took longer than %ds and was stopped.", tool, p.man.TimeoutSeconds), nil
	case res.ExitCode != 0:
		detail := strings.TrimSpace(res.Stderr)
		if detail == "" {
			detail = out
		}
		if res.Error != "" {
			detail = strings.TrimSpace(res.Error + " " + detail)
		}
		return fmt.Sprintf("The %s tool failed (exit %d): %s", tool, res.ExitCode, tail(detail, 1500)), nil
	}
	if out == "" {
		return "(no output)", nil
	}
	if res.Truncated {
		out += "\n[output cut: it was too long]"
	}
	return out, nil
}

type mcpSession struct {
	mu         sync.Mutex
	t          transport
	tools      []mcpTool
	status     string
	err        string
	serverName string
	dial       sync.Mutex
}

func (s *mcpSession) close() {
	s.mu.Lock()
	t := s.t
	s.t, s.status = nil, StatusOff
	s.mu.Unlock()
	if t != nil {
		t.close()
	}
}

func (m *Manager) connect(p *plugin) {
	defer guard.Recover("connecting an MCP server")
	ctx, cancel := context.WithTimeout(m.life, mcpStartTimeout)
	defer cancel()
	_ = m.reconnect(ctx, p)
}

func (m *Manager) reconnect(ctx context.Context, p *plugin) error {
	s := p.mcp
	s.dial.Lock()
	defer s.dial.Unlock()
	s.mu.Lock()
	if s.t != nil && s.t.alive() && s.status == StatusReady {
		s.mu.Unlock()
		return nil
	}
	old := s.t
	s.t, s.status, s.err = nil, StatusConnecting, ""
	s.mu.Unlock()
	if old != nil {
		old.close()
	}
	t, tools, name, err := m.dial(ctx, p)
	s.mu.Lock()
	if err != nil {
		s.status, s.err = StatusError, tail(err.Error(), 400)
		log.Printf("plugins: MCP server %s: %v", p.srv.ID, err)
	} else {
		s.t, s.tools, s.serverName, s.status = t, tools, name, StatusReady
	}
	s.mu.Unlock()
	m.mu.Lock()
	m.rebuildLocked()
	m.mu.Unlock()
	return err
}

func (m *Manager) dial(ctx context.Context, p *plugin) (transport, []mcpTool, string, error) {
	c := p.srv
	if c.URL != "" {
		headers := map[string]string{}
		for k, v := range c.Headers {
			headers[k] = v
		}
		for k, v := range p.state.Env {
			headers[k] = v
		}
		client := m.HTTP
		sse := c.Transport == "sse" || (c.Transport == "" && strings.HasSuffix(strings.TrimRight(c.URL, "/"), "/sse"))
		if !sse {
			t := &httpConn{url: c.URL, headers: headers, client: client}
			tools, name, err := handshake(ctx, t, m.Version)
			var hs *httpStatusError
			if err == nil {
				return t, tools, name, nil
			}
			if c.Transport == "http" || !errors.As(err, &hs) || (hs.status != 400 && hs.status != 404 && hs.status != 405) {
				return nil, nil, "", err
			}
		}
		t, err := startSSE(ctx, client, c.URL, headers)
		if err != nil {
			return nil, nil, "", err
		}
		tools, name, err := handshake(ctx, t, m.Version)
		if err != nil {
			t.close()
			return nil, nil, "", err
		}
		return t, tools, name, nil
	}
	if err := m.canRun(); err != nil {
		return nil, nil, "", err
	}
	env := map[string]string{}
	for k, v := range c.Env {
		env[k] = v
	}
	for k, v := range p.state.Env {
		env[k] = v
	}

	for k, v := range map[string]string{"npm_config_yes": "true", "npm_config_update_notifier": "false", "npm_config_fund": "false",
		"npm_config_audit": "false", "NO_UPDATE_NOTIFIER": "1"} {
		if _, set := env[k]; !set {
			env[k] = v
		}
	}
	session := "mcp-" + c.ID
	opts := machine.RunOptions{Args: append([]string{c.Command}, c.Args...), Env: env}
	t, err := startStdio(m.life, m.Machine, session, opts)
	if err != nil {
		return nil, nil, "", err
	}
	tools, name, err := handshake(ctx, t, m.Version)
	if err != nil {
		t.close()
		return nil, nil, "", err
	}
	return t, tools, name, nil
}

func (m *Manager) callMCP(ctx context.Context, p *plugin, sessionID, tool string, args map[string]any) (string, error) {
	s := p.mcp
	call := func() (json.RawMessage, error) {
		s.mu.Lock()
		t := s.t
		s.mu.Unlock()
		if t == nil || !t.alive() {
			if err := m.reconnect(ctx, p); err != nil {
				return nil, err
			}
			s.mu.Lock()
			t = s.t
			s.mu.Unlock()
		}
		cctx, cancel := context.WithTimeout(ctx, p.srv.callTimeout())
		defer cancel()
		return t.request(cctx, "tools/call", map[string]any{"name": tool, "arguments": args})
	}
	raw, err := call()
	var rpcErr *rpcError
	if err != nil && !errors.As(err, &rpcErr) && ctx.Err() == nil {
		s.mu.Lock()
		if s.t != nil && !s.t.alive() {
			s.status = StatusConnecting
		}
		s.mu.Unlock()
		raw, err = call()
	}
	if err != nil {
		if errors.As(err, &rpcErr) {
			return fmt.Sprintf("The %s tool refused the call: %s", tool, rpcErr.Message), nil
		}
		return fmt.Sprintf("The %s server did not answer: %v", p.srv.ID, err), nil
	}
	n := 0
	save := func(ext string, data []byte) (string, error) {
		n++
		name := fmt.Sprintf("%s-%s-%d%s", p.srv.ID, time.Now().Format("150405"), n, ext)
		return name, m.Machine.WriteFile(sessionID, name, data)
	}
	return callResult(raw, save), nil
}

func (m *Manager) List() []Info {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Info, 0, len(m.plugins))
	for _, p := range m.plugins {
		out = append(out, m.infoLocked(p))
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind == KindScript
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func (m *Manager) Get(key string) (Info, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	p, ok := m.plugins[key]
	if !ok {
		return Info{}, false
	}
	return m.infoLocked(p), true
}

func (m *Manager) infoLocked(p *plugin) Info {
	i := Info{Key: p.key, Kind: p.kind, Source: p.source, Enabled: p.enabled(), Access: p.access(), Error: p.err,
		EnvSet: map[string]bool{}, EnvOwn: map[string]bool{}, EnvHelp: map[string]string{}}
	for k, v := range p.state.Env {
		i.EnvSet[k], i.EnvOwn[k] = v != "", v != ""
	}
	switch p.kind {
	case KindScript:
		i.ID, i.Name, i.Version, i.Author, i.Description = p.man.ID, p.man.Name, p.man.Version, p.man.Author, p.man.Description
		i.Entry, i.Runtime = p.entry, runtimeOf(p.entry)
		i.Net, i.TimeoutSeconds = p.man.RequiresNet, p.man.TimeoutSeconds
		if p.entry != "" {
			i.MemoryMB = p.memoryMB()
		}
		for k, help := range p.man.Env {
			i.EnvNames = append(i.EnvNames, k)
			i.EnvHelp[k] = help
		}
		for _, f := range []string{"requirements.txt", "package.json"} {
			if _, err := os.Stat(filepath.Join(p.dir, f)); err == nil {
				i.Deps = true
			}
		}
	case KindMCP:
		i.ID, i.Name = p.srv.ID, p.srv.Name
		if i.Name == "" {
			i.Name = p.srv.ID
		}
		i.Command = strings.TrimSpace(p.srv.Command + " " + strings.Join(p.srv.Args, " "))
		i.URL, i.Net = p.srv.URL, true
		for k := range p.srv.Env {
			i.EnvNames = append(i.EnvNames, k)
			i.EnvSet[k] = true
		}
		for k := range p.state.Env {
			if !contains(i.EnvNames, k) {
				i.EnvNames = append(i.EnvNames, k)
			}
		}
		p.mcp.mu.Lock()
		i.Status, i.ServerName = p.mcp.status, p.mcp.serverName
		if p.mcp.err != "" && i.Error == "" {
			i.Error = p.mcp.err
		}
		p.mcp.mu.Unlock()
		if !i.Enabled {
			i.Status = StatusOff
		}
	}
	sort.Strings(i.EnvNames)
	for _, t := range m.toolsOf(p) {
		ref, ok := m.index[t.Function.Name]
		i.Tools = append(i.Tools, ToolInfo{Name: t.Function.Name, Description: t.Function.Description,
			Conflict: i.Enabled && (!ok || ref.key != p.key)})
	}
	return i
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func (m *Manager) SetEnabled(ctx context.Context, key string, on bool) error {
	return m.update(ctx, key, func(st *State) { st.Enabled = &on })
}

func (m *Manager) SetAccess(ctx context.Context, key, access string) error {
	if access != AccessEveryone && access != AccessAdmins {
		return errors.New("unknown access")
	}
	return m.update(ctx, key, func(st *State) { st.Access = access })
}

func (m *Manager) SetEnv(ctx context.Context, key, name, value string) error {
	m.mu.RLock()
	p := m.plugins[key]
	m.mu.RUnlock()
	if p == nil {
		return errors.New("no such plugin")
	}
	if p.kind == KindScript {
		if _, declared := p.man.Env[name]; !declared && value != "" {
			names := make([]string, 0, len(p.man.Env))
			for k := range p.man.Env {
				names = append(names, k)
			}
			sort.Strings(names)
			if len(names) == 0 {
				return fmt.Errorf("%s declares no variables (add them to \"env\" in its plugin.json)", p.man.ID)
			}
			return fmt.Errorf("%s does not use %q; it uses: %s", p.man.ID, name, strings.Join(names, ", "))
		}
	} else if p.srv.URL == "" && (!envNameRe.MatchString(name) || reservedEnv(name)) {
		return fmt.Errorf("%q: use UPPER_CASE names (not PATH, HOME, PYTHON…, MACRON…)", name)
	} else if p.srv.URL != "" && !headerRe.MatchString(name) {
		return fmt.Errorf("%q is not an HTTP header name", name)
	}
	if len(value) > maxEnvValue {
		return errors.New("the value is too long")
	}
	return m.update(ctx, key, func(st *State) {
		if st.Env == nil {
			st.Env = map[string]string{}
		}
		if value == "" {
			delete(st.Env, name)
		} else if len(st.Env) < maxEnvVars || st.Env[name] != "" {
			st.Env[name] = value
		}
	})
}

func (m *Manager) update(ctx context.Context, key string, change func(*State)) error {
	m.mu.RLock()
	_, ok := m.plugins[key]
	m.mu.RUnlock()
	if !ok {
		return errors.New("no such plugin")
	}
	st := m.states().PluginState(key)
	change(&st)
	if err := m.states().SetPluginState(key, st); err != nil {
		return err
	}
	m.Reload(ctx)
	return nil
}

func (m *Manager) AddServers(ctx context.Context, list []ServerConfig) ([]string, error) {
	panel := m.states().PanelServers()
	taken := map[string]bool{}
	m.mu.RLock()
	for k := range m.plugins {
		taken[strings.TrimPrefix(k, "@")] = true
	}
	m.mu.RUnlock()
	var keys []string
	for _, c := range list {
		if err := c.Check(); err != nil {
			return nil, err
		}
		base := c.ID
		for n := 2; taken[c.ID]; n++ {
			c.ID = fmt.Sprintf("%s-%d", base, n)
		}
		taken[c.ID] = true
		panel = append(panel, c)
		keys = append(keys, "@"+c.ID)
	}
	if err := m.states().SetPanelServers(panel); err != nil {
		return nil, err
	}
	m.Reload(ctx)
	return keys, nil
}

func (m *Manager) Wait(key string, d time.Duration) Info {
	deadline := time.Now().Add(d)
	for {
		i, ok := m.Get(key)
		if !ok || i.Kind != KindMCP || i.Status != StatusConnecting || time.Now().After(deadline) {
			return i
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func (m *Manager) Delete(ctx context.Context, key string) error {
	m.mu.RLock()
	p := m.plugins[key]
	m.mu.RUnlock()
	if p == nil {
		return errors.New("no such plugin")
	}
	switch p.source {
	case SourceFile:
		return fmt.Errorf("it is defined in %s: remove it there (it can be turned off here)", filepath.Base(m.MCPFile))
	case SourcePanel:
		var keep []ServerConfig
		for _, c := range m.states().PanelServers() {
			if c.ID != p.srv.ID {
				keep = append(keep, c)
			}
		}
		if err := m.states().SetPanelServers(keep); err != nil {
			return err
		}
	case SourceFolder:
		dir := filepath.Join(m.Dir, p.key)
		if !idRe.MatchString(p.key) || filepath.Dir(dir) != filepath.Clean(m.Dir) {
			return errors.New("not a plugin folder")
		}
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	_ = m.states().SetPluginState(key, State{})
	m.Reload(ctx)
	return nil
}

func (m *Manager) Close() {
	m.mu.Lock()
	if m.stop != nil {
		m.stop()
	}
	plugins := m.plugins
	m.mu.Unlock()
	for _, p := range plugins {
		if p.mcp != nil {
			p.mcp.close()
		}
	}
}

type Installed struct {
	Keys []string
	Deps string
}

func (m *Manager) InstallScript(ctx context.Context, fileName string, code []byte) (Installed, error) {
	if len(code) > maxZipFile {
		return Installed{}, errors.New("the file is too large")
	}
	man, err := ManifestForScript(fileName, code)
	if err != nil {
		return Installed{}, err
	}
	data, _ := json.MarshalIndent(man, "", "  ")
	files := map[string]fileData{manifestName: {data: data}, man.Entry: {data: code, mode: 0o755}}
	if err := m.writePlugin(man.ID, files); err != nil {
		return Installed{}, err
	}
	m.Reload(ctx)
	return Installed{Keys: []string{man.ID}}, nil
}

type fileData struct {
	data []byte
	mode os.FileMode
}

func (m *Manager) InstallZip(ctx context.Context, archive []byte) (Installed, error) {
	zr, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		return Installed{}, errors.New("not a zip file")
	}
	if len(zr.File) > maxZipFiles {
		return Installed{}, fmt.Errorf("more than %d files", maxZipFiles)
	}
	files := map[string]*zip.File{}
	var roots []string
	for _, f := range zr.File {
		name := strings.ReplaceAll(f.Name, "\\", "/")
		if f.FileInfo().IsDir() || f.Mode()&os.ModeSymlink != 0 || strings.HasPrefix(name, "__MACOSX/") || strings.Contains(name, "/.git/") {
			continue
		}
		clean := path.Clean(name)
		if path.IsAbs(clean) || clean == "." || strings.HasPrefix(clean, "../") || clean == ".." {
			return Installed{}, fmt.Errorf("unsafe path in the zip: %q", f.Name)
		}
		files[clean] = f
		if path.Base(clean) == manifestName && strings.Count(clean, "/") <= 3 {
			roots = append(roots, path.Dir(clean))
		}
	}
	if len(roots) == 0 {
		return Installed{}, errors.New("no plugin.json in the zip")
	}
	sort.Strings(roots)
	if len(roots) > maxZipPlugins {
		return Installed{}, fmt.Errorf("more than %d plugins in one zip", maxZipPlugins)
	}
	var total int64
	var out Installed
	for _, root := range roots {
		prefix := ""
		if root != "." {
			prefix = root + "/"
		}
		raw, err := readZip(files[prefix+manifestName], maxManifest)
		if err != nil {
			return out, err
		}
		man, err := ParseManifest(raw)
		if err != nil {
			return out, fmt.Errorf("%s: %w", prefix+manifestName, err)
		}
		set := map[string]fileData{}
		for name, f := range files {
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			rel := strings.TrimPrefix(name, prefix)
			if other := nestedRoot(rel, roots, root); other {
				continue
			}
			data, err := readZip(f, maxZipFile)
			if err != nil {
				return out, fmt.Errorf("%s: %w", name, err)
			}
			if total += int64(len(data)); total > maxZipBytes {
				return out, fmt.Errorf("the plugins are larger than %d MB unpacked", maxZipBytes>>20)
			}
			mode := os.FileMode(0o644)
			if f.Mode()&0o111 != 0 || rel == man.Entry || contains(defaultEntries, rel) {
				mode = 0o755
			}
			set[rel] = fileData{data: data, mode: mode}
		}
		if err := m.writePlugin(man.ID, set); err != nil {
			return out, err
		}
		out.Keys = append(out.Keys, man.ID)
	}
	m.Reload(ctx)
	var logs []string
	for _, key := range out.Keys {
		if s, err := m.InstallDeps(ctx, key); err != nil {
			logs = append(logs, key+": "+err.Error())
		} else if s != "" {
			logs = append(logs, key+": "+s)
		}
	}
	out.Deps = strings.Join(logs, "\n")
	return out, nil
}

func nestedRoot(rel string, roots []string, root string) bool {
	for _, r := range roots {
		if r == root {
			continue
		}
		prefix := r + "/"
		if root != "." {
			if !strings.HasPrefix(r, root+"/") {
				continue
			}
			prefix = strings.TrimPrefix(r, root+"/") + "/"
		}
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func readZip(f *zip.File, limit int64) ([]byte, error) {
	if f == nil {
		return nil, errors.New("missing file")
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("larger than %d MB", limit>>20)
	}
	return data, nil
}

func (m *Manager) writePlugin(id string, files map[string]fileData) error {
	if m.Dir == "" {
		return errors.New("no plugins folder is set (MACRON_PLUGINS_DIR)")
	}
	if !idRe.MatchString(id) {
		return fmt.Errorf("bad plugin id %q", id)
	}
	if err := os.MkdirAll(m.Dir, 0o755); err != nil {
		return err
	}
	suffix := make([]byte, 4)
	_, _ = rand.Read(suffix)
	tmp := filepath.Join(m.Dir, ".new-"+id+"-"+hex.EncodeToString(suffix))
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	for rel, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(rel))
		if !strings.HasPrefix(dst, tmp+string(filepath.Separator)) {
			return fmt.Errorf("unsafe path %q", rel)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		mode := f.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(dst, f.data, mode); err != nil {
			return err
		}
		_ = os.Chmod(dst, mode)
	}
	final := filepath.Join(m.Dir, id)
	old := final + ".old-" + hex.EncodeToString(suffix)
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, old); err != nil {
			return err
		}
		defer os.RemoveAll(old)
	}
	return os.Rename(tmp, final)
}

func (m *Manager) InstallDeps(ctx context.Context, key string) (string, error) {
	m.mu.RLock()
	p := m.plugins[key]
	m.mu.RUnlock()
	if p == nil || p.kind != KindScript {
		return "", errors.New("no such plugin")
	}
	var cmds [][]string
	root := p.dir
	if _, err := os.Stat(filepath.Join(p.dir, "requirements.txt")); err == nil {
		cmds = append(cmds, []string{"python3", "-m", "pip", "install", "--disable-pip-version-check", "--no-cache-dir", "--no-input",
			"--target", root + "/" + depsDir, "-r", root + "/requirements.txt"})
	}
	if _, err := os.Stat(filepath.Join(p.dir, "package.json")); err == nil {
		cmds = append(cmds, []string{"npm", "install", "--omit=dev", "--no-audit", "--no-fund", "--prefix", root})
	}
	if len(cmds) == 0 {
		return "", nil
	}
	if err := m.canRun(); err != nil {
		return "", err
	}
	var logs []string
	for _, argv := range cmds {
		opts := machine.RunOptions{Args: argv, Timeout: depsTimeout, MergeOutput: true, Heavy: true}
		res := m.Machine.Run(ctx, buildSession, opts)
		out := strings.TrimSpace(res.Stdout)
		if res.ExitCode != 0 {
			return "", fmt.Errorf("%s failed (exit %d): %s", argv[0], res.ExitCode, tail(strings.TrimSpace(res.Error+" "+out), 600))
		}
		logs = append(logs, tail(lastLine(out), 160))
	}
	return strings.Join(logs, " · "), nil
}

func lastLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		return strings.TrimSpace(s[i+1:])
	}
	return s
}

type TestResult struct {
	Tool    string
	OK      bool
	Skipped string
	Took    time.Duration
	Output  string
}

func (m *Manager) Test(ctx context.Context, key string) ([]TestResult, error) {
	m.mu.RLock()
	p := m.plugins[key]
	m.mu.RUnlock()
	if p == nil {
		return nil, errors.New("no such plugin")
	}
	if p.err != "" {
		return nil, errors.New(p.err)
	}
	if p.kind == KindMCP {
		start := time.Now()
		p.mcp.close()
		cctx, cancel := context.WithTimeout(ctx, mcpStartTimeout)
		defer cancel()
		err := m.reconnect(cctx, p)
		r := TestResult{Tool: p.srv.ID, OK: err == nil, Took: time.Since(start)}
		if err != nil {
			r.Output = err.Error()
		} else {
			p.mcp.mu.Lock()
			r.Output = fmt.Sprintf("%d tools", len(p.mcp.tools))
			p.mcp.mu.Unlock()
		}
		return []TestResult{r}, nil
	}
	var out []TestResult
	for _, t := range p.man.Tools {
		r := TestResult{Tool: t.Name}
		args := t.TestArgs
		if args == nil && len(requiredOf(t.Parameters)) > 0 {
			r.Skipped = "needs test_args in plugin.json"
			out = append(out, r)
			continue
		}
		start := time.Now()
		res, err := m.runScript(ctx, p, testSession, t.Name, args)
		r.Took = time.Since(start)
		switch {
		case err != nil:
			r.Output = err.Error()
		case strings.HasPrefix(res, "The "+t.Name+" tool failed") || strings.HasPrefix(res, "The "+t.Name+" tool took longer"):
			r.Output = res
		default:
			r.OK, r.Output = true, res
		}
		out = append(out, r)
	}
	return out, nil
}
