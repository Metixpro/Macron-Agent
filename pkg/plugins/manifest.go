package plugins

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"regexp"
	"strconv"
	"strings"
)

const (
	AccessEveryone = "everyone"
	AccessAdmins   = "admins"
)

const (
	maxTools        = 30
	maxSchemaBytes  = 16 << 10
	maxDescRunes    = 1000
	defaultTimeout  = 30
	maxTimeout      = 300
	defaultMemoryMB = 256
	maxMemoryMB     = 8192
	maxManifest     = 256 << 10
	maxEnvVars      = 20
	maxEnvValue     = 4096
	maxNetHosts     = 20
	pluginMount     = "/plugin"
	serversMount    = "/plugins"
	depsDir         = ".deps"
	manifestName    = "plugin.json"
	runtimePython   = "python"
	runtimeBash     = "bash"
	runtimeNode     = "node"
	runtimeBinary   = "binary"
)

var (
	idRe       = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,39}$`)
	toolNameRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,47}$`)
	envNameRe  = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	hostRe     = regexp.MustCompile(`^[a-z0-9.-]{1,253}$`)
	headerRe   = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	mcpNameRe  = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

type ToolSpec struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`

	TestArgs map[string]any `json:"test_args,omitempty"`
}

type Manifest struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Author      string `json:"author"`
	Description string `json:"description"`
	Enabled     *bool  `json:"enabled,omitempty"`

	Entry          string `json:"entry,omitempty"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty"`
	MemoryMB       *int   `json:"memory_mb,omitempty"`
	RequiresNet    bool   `json:"requires_net,omitempty"`

	NetHosts []string `json:"net_hosts,omitempty"`

	Access string `json:"access,omitempty"`

	Env   map[string]string `json:"env,omitempty"`
	Tools []ToolSpec        `json:"tools"`
}

func ParseManifest(data []byte) (*Manifest, error) {
	if len(data) > maxManifest {
		return nil, errors.New("plugin.json is too large")
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(bytes.TrimPrefix(data, []byte("\ufeff"))))
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json is not valid JSON: %w", err)
	}
	return &m, m.Check()
}

func (m *Manifest) Check() error {
	m.ID = strings.ToLower(strings.TrimSpace(m.ID))
	if !idRe.MatchString(m.ID) {
		return fmt.Errorf("id %q: use 1-40 lowercase letters, digits, - or _", m.ID)
	}
	m.Name = oneLine(m.Name, 80)
	if m.Name == "" {
		m.Name = m.ID
	}
	m.Version, m.Author, m.Description = oneLine(m.Version, 32), oneLine(m.Author, 80), oneLine(m.Description, 500)
	if m.Entry != "" {
		e := path.Clean(strings.TrimPrefix(m.Entry, "./"))
		if path.IsAbs(e) || e == "." || strings.HasPrefix(e, "../") || strings.Contains(e, "/../") || strings.HasPrefix(e, ".") {
			return fmt.Errorf("entry %q must be a file inside the plugin folder", m.Entry)
		}
		m.Entry = e
	}
	switch {
	case m.TimeoutSeconds == 0:
		m.TimeoutSeconds = defaultTimeout
	case m.TimeoutSeconds < 1 || m.TimeoutSeconds > maxTimeout:
		return fmt.Errorf("timeout_seconds must be between 1 and %d", maxTimeout)
	}
	if m.MemoryMB != nil && (*m.MemoryMB < 0 || *m.MemoryMB > maxMemoryMB || (*m.MemoryMB > 0 && *m.MemoryMB < 32)) {
		return fmt.Errorf("memory_mb must be 0 (no limit) or between 32 and %d", maxMemoryMB)
	}
	switch m.Access {
	case "":
		m.Access = AccessEveryone
	case AccessEveryone, AccessAdmins:
	default:
		return fmt.Errorf("access must be %q or %q", AccessEveryone, AccessAdmins)
	}
	if len(m.NetHosts) > maxNetHosts {
		return fmt.Errorf("at most %d net_hosts", maxNetHosts)
	}
	for i, h := range m.NetHosts {
		h = strings.ToLower(strings.TrimSpace(h))
		if !hostRe.MatchString(h) {
			return fmt.Errorf("net_hosts: %q is not a host name", h)
		}
		m.NetHosts[i] = h
	}
	if len(m.Env) > maxEnvVars {
		return fmt.Errorf("at most %d env variables", maxEnvVars)
	}
	for k := range m.Env {
		if !envNameRe.MatchString(k) || reservedEnv(k) {
			return fmt.Errorf("env %q: use UPPER_CASE names (not PATH, HOME, PYTHON…, WAZE…)", k)
		}
	}
	if len(m.Tools) == 0 || len(m.Tools) > maxTools {
		return fmt.Errorf("a plugin declares 1 to %d tools", maxTools)
	}
	seen := map[string]bool{}
	for i := range m.Tools {
		t := &m.Tools[i]
		if err := checkTool(t); err != nil {
			return err
		}
		if seen[t.Name] {
			return fmt.Errorf("tool %q is declared twice", t.Name)
		}
		seen[t.Name] = true
	}
	return nil
}

func checkTool(t *ToolSpec) error {
	t.Name = strings.TrimSpace(t.Name)
	if !toolNameRe.MatchString(t.Name) {
		return fmt.Errorf("tool name %q: a letter, then letters, digits or _ (at most 48)", t.Name)
	}
	t.Description = strings.TrimSpace(t.Description)
	if t.Description == "" {
		return fmt.Errorf("tool %q has no description (the model chooses tools by it)", t.Name)
	}
	if r := []rune(t.Description); len(r) > maxDescRunes {
		t.Description = string(r[:maxDescRunes])
	}
	params, err := cleanSchema(t.Parameters)
	if err != nil {
		return fmt.Errorf("tool %q: %w", t.Name, err)
	}
	t.Parameters = params
	return nil
}

func cleanSchema(s map[string]any) (map[string]any, error) {
	if s == nil {
		return map[string]any{"type": "object", "properties": map[string]any{}}, nil
	}
	delete(s, "$schema")
	if t, ok := s["type"]; ok && t != "object" {
		return nil, errors.New(`parameters must be a JSON schema of "type": "object"`)
	}
	s["type"] = "object"
	if _, ok := s["properties"]; !ok {
		s["properties"] = map[string]any{}
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	if len(raw) > maxSchemaBytes {
		return nil, fmt.Errorf("parameters schema is larger than %d KB", maxSchemaBytes>>10)
	}
	return s, nil
}

func requiredOf(s map[string]any) []string {
	var out []string
	if list, ok := s["required"].([]any); ok {
		for _, v := range list {
			if n, ok := v.(string); ok {
				out = append(out, n)
			}
		}
	}
	if list, ok := s["required"].([]string); ok {
		out = append(out, list...)
	}
	return out
}

func reservedEnv(k string) bool {
	switch k {
	case "PATH", "HOME", "TMPDIR", "LANG", "LC_ALL", "TERM", "NODE_PATH", "NODE_OPTIONS", "LD_PRELOAD", "LD_LIBRARY_PATH",
		"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "NO_PROXY":
		return true
	}
	return strings.HasPrefix(k, "PYTHON") || strings.HasPrefix(k, "WAZE") || strings.HasPrefix(k, "BASH_") || strings.HasPrefix(k, "LD_")
}

func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return s
}

func runtimeOf(entry string) string {
	switch strings.ToLower(path.Ext(entry)) {
	case ".py":
		return runtimePython
	case ".sh", ".bash":
		return runtimeBash
	case ".js", ".mjs", ".cjs":
		return runtimeNode
	}
	return runtimeBinary
}

var defaultEntries = []string{"run.py", "run.sh", "run.js", "run"}

const pythonBoot = `import runpy, sys
entry = sys.argv[1]
sys.argv = sys.argv[1:]
root = entry.rsplit("/", 1)[0]
sys.path[:] = [root, root + "/.deps"] + [p for p in sys.path if p not in ("", ".")]
runpy.run_path(entry, run_name="__main__")
`

func ManifestForScript(fileName string, code []byte) (*Manifest, error) {
	base := strings.ToLower(path.Base(strings.ReplaceAll(fileName, "\\", "/")))
	ext := path.Ext(base)
	switch ext {
	case ".py", ".sh", ".bash", ".js", ".mjs":
	default:
		return nil, errors.New("send a .py, .sh or .js file (or a .zip with plugin.json)")
	}
	stem := strings.TrimSuffix(base, ext)
	m := &Manifest{ID: slug(stem), Entry: "run" + ext}
	tool := ToolSpec{Name: strings.ReplaceAll(slug(stem), "-", "_")}
	props := map[string]any{}
	var required []string
	lines := strings.Split(string(code), "\n")
	for i, line := range lines {
		if i > 60 {
			break
		}
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "#!"):
			continue
		case strings.HasPrefix(t, "#"):
			t = strings.TrimSpace(strings.TrimPrefix(t, "#"))
		case strings.HasPrefix(t, "//"):
			t = strings.TrimSpace(strings.TrimPrefix(t, "//"))
		default:
			continue
		}
		key, val, ok := strings.Cut(t, ":")
		if !ok {
			continue
		}
		val = strings.TrimSpace(val)
		switch strings.ToLower(strings.TrimSpace(key)) {
		case "plugin", "waze-plugin", "id":
			m.ID = slug(val)
		case "name":
			m.Name = val
		case "tool":
			tool.Name = val
		case "description":
			tool.Description = val
			if m.Description == "" {
				m.Description = val
			}
		case "version":
			m.Version = val
		case "author":
			m.Author = val
		case "net":
			m.RequiresNet = val == "true" || val == "yes" || val == "1"
		case "timeout":
			if n, err := strconv.Atoi(val); err == nil {
				m.TimeoutSeconds = n
			}
		case "access":
			m.Access = strings.ToLower(val)
		case "env":
			name, desc, _ := strings.Cut(val, " ")
			if m.Env == nil {
				m.Env = map[string]string{}
			}
			m.Env[strings.TrimSpace(name)] = strings.TrimSpace(desc)
		case "param":
			f := strings.Fields(val)
			if len(f) < 2 {
				continue
			}
			p := map[string]any{"type": f[1]}
			rest := f[2:]
			if len(rest) > 0 && rest[0] == "required" {
				required = append(required, f[0])
				rest = rest[1:]
			}
			if d := strings.Join(rest, " "); d != "" {
				p["description"] = d
			}
			props[f[0]] = p
		}
	}
	if len(props) == 0 {
		props["input"] = map[string]any{"type": "string", "description": "What to do, or the text to work on"}
	}
	tool.Parameters = map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		tool.Parameters["required"] = required
	}
	if tool.Description == "" {
		tool.Description = "Runs the " + stem + " plugin."
	}
	m.Tools = []ToolSpec{tool}
	return m, m.Check()
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.':
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if len(out) > 40 {
		out = out[:40]
	}
	if out == "" {
		out = "plugin"
	}
	return out
}
