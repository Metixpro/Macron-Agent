package config

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const (
	EnvAPIURL        = "WAZE_API_URL"
	EnvAPIKey        = "WAZE_API_KEY"
	EnvModel         = "WAZE_MODEL"
	EnvWorkspaceDir  = "WAZE_WORKSPACE_DIR"
	EnvEnvProxy      = "WAZE_USE_ENV_PROXY"
	EnvGitHubToken   = "GITHUB_TOKEN"
	EnvBotToken      = "TELEGRAM_BOT_TOKEN"
	EnvServerToken   = "WAZE_SERVER_TOKEN"
	EnvListen        = "WAZE_LISTEN"
	EnvCORS          = "WAZE_CORS_ORIGINS"
	EnvOwners        = "WAZE_OWNERS"
	EnvSecretKey     = "WAZE_SECRET_KEY"
	EnvTimezone      = "WAZE_TIMEZONE"
	EnvCookiesDir    = "WAZE_COOKIES_DIR"
	EnvTregToken     = "WAZE_TREG_TOKEN"
	EnvReaderURL     = "WAZE_READER_URL"
	EnvReaderKey     = "WAZE_READER_KEY"
	EnvPiperModel    = "WAZE_PIPER_MODEL"
	EnvContextWindow = "WAZE_CONTEXT_WINDOW"
	EnvSkillsDir     = "WAZE_SKILLS_DIR"
	EnvSkillsWrite   = "WAZE_SKILLS_WRITE"
	EnvPluginsDir    = "WAZE_PLUGINS_DIR"
	EnvMCPConfig     = "WAZE_MCP_CONFIG"
	EnvTelegramAPI   = "WAZE_TELEGRAM_API"
	EnvTelegramLocal = "WAZE_TELEGRAM_LOCAL"
)

const DefaultModel = "claude-sonnet-5"

const DefaultPiperModel = "/opt/piper-voices/fa_IR-mana-medium.onnx"

var olderPiperModels = []string{"/opt/piper-voices/fa_IR-mantatts-par.onnx"}

func defaultPiper() string {
	for _, m := range append([]string{DefaultPiperModel}, olderPiperModels...) {
		if fi, err := os.Stat(m); err == nil && fi.Size() > 0 {
			if fj, err := os.Stat(m + ".json"); err == nil && fj.Size() > 0 {
				return m
			}
		}
	}
	return DefaultPiperModel
}

type Config struct {
	APIURL        string
	APIKey        string
	Model         string
	WorkspaceDir  string
	WorkspaceTTL  time.Duration
	UseEnvProxy   bool
	GitHubToken   string
	TregToken     string
	ReaderURL     string
	ReaderKey     string
	PiperModel    string
	ContextWindow int
	SkillsDir     string
	SkillsWrite   bool
	PluginsDir    string
	MCPConfig     string
	CookiesDir    string
	MaxSteps      int
	Slots         int
	RunTimeout    time.Duration

	OptionalAPIURL bool
}

func LoadDotEnv(path string) error {
	if err := PruneRetiredEnv(path); err != nil {
		return err
	}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, val, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			continue
		}
		val = strings.TrimSpace(val)
		if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') && val[len(val)-1] == val[0] {
			val = val[1 : len(val)-1]
		} else if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if _, set := os.LookupEnv(key); !set {
			_ = os.Setenv(key, val)
		}
	}
	return sc.Err()
}

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key))); err == nil && n > 0 {
		return n
	}
	return def
}

func envBool(key string) bool {
	b, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(key)))
	return b
}

func (c *Config) Bind(fs *flag.FlagSet) {
	fs.StringVar(&c.APIURL, "api-url", envOr(EnvAPIURL, ""), "OpenAI-compatible base URL, e.g. https://host/v1 (env "+EnvAPIURL+")")
	fs.StringVar(&c.APIKey, "api-key", os.Getenv(EnvAPIKey), "LLM API key — prefer the "+EnvAPIKey+" environment variable")
	fs.StringVar(&c.Model, "model", envOr(EnvModel, DefaultModel), "model id (env "+EnvModel+")")
	fs.StringVar(&c.WorkspaceDir, "workspace-dir", envOr(EnvWorkspaceDir, HomePath("workspaces")), "base directory of the per-user workspaces (env "+EnvWorkspaceDir+")")
	fs.DurationVar(&c.WorkspaceTTL, "workspace-ttl", 0, "delete work-folder files (uploads and results) older than this (0 = never, the default)")
	fs.BoolVar(&c.UseEnvProxy, "use-env-proxy", envBool(EnvEnvProxy), "send outbound tool traffic through HTTP(S)_PROXY (weakens the SSRF guard)")
	fs.IntVar(&c.MaxSteps, "max-steps", 200, "maximum tool-calling rounds per request (the bot's admin panel can change it)")
	fs.DurationVar(&c.RunTimeout, "run-timeout", 3*time.Hour, "time limit for one request (the bot's admin panel can change it)")
	fs.IntVar(&c.Slots, "slots", 0, "commands (code, downloads, conversions) running at the same time; 0 = 8, or twice the CPUs")
	fs.StringVar(&c.CookiesDir, "cookies-dir", envOr(EnvCookiesDir, HomePath("cookies")), "folder of cookies.txt files used in turn by the downloaders (env "+EnvCookiesDir+")")
	fs.StringVar(&c.SkillsDir, "skills-dir", os.Getenv(EnvSkillsDir), "folder of procedural skills (<category>/<name>/SKILL.md); default ~/.macron/skills (env "+EnvSkillsDir+")")
	fs.StringVar(&c.PluginsDir, "plugins-dir", os.Getenv(EnvPluginsDir), "folder of plugins (<id>/plugin.json); default ~/.macron/plugins (env "+EnvPluginsDir+")")
	fs.StringVar(&c.MCPConfig, "mcp-config", envOr(EnvMCPConfig, HomePath("mcp_servers.json")), "MCP servers, in mcp_servers.json form (env "+EnvMCPConfig+")")
	c.GitHubToken = os.Getenv(EnvGitHubToken)
	c.TregToken = os.Getenv(EnvTregToken)
	c.ReaderURL = "https://r.jina.ai/"
	switch v := strings.TrimSpace(os.Getenv(EnvReaderURL)); {
	case strings.EqualFold(v, "off"), strings.EqualFold(v, "none"):
		c.ReaderURL = ""
	case v != "":
		c.ReaderURL = strings.TrimRight(v, "/") + "/"
	}
	c.ReaderKey = os.Getenv(EnvReaderKey)
	c.PiperModel = strings.TrimSpace(os.Getenv(EnvPiperModel))
	switch strings.ToLower(c.PiperModel) {
	case "", "on", "default":
		c.PiperModel = defaultPiper()
	case "off", "none", "disabled":
		c.PiperModel = ""
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvContextWindow))); err == nil && n >= 8000 {
		c.ContextWindow = n
	}
	c.SkillsWrite = envBool(EnvSkillsWrite)
}

func DefaultSkillsDir() string { return HomePath("skills") }

const (
	MaxAgentSteps = 1000
	MaxRunMinutes = 24 * 60
)

func (c *Config) Validate(fs *flag.FlagSet) (warnings []string, err error) {
	c.APIURL = strings.TrimRight(strings.TrimSpace(c.APIURL), "/")
	var u *url.URL
	if c.APIURL == "" {
		if !c.OptionalAPIURL {
			return nil, fmt.Errorf("the LLM endpoint is not configured: set %s (or -api-url), e.g. https://api.example.com/v1", EnvAPIURL)
		}
	} else {
		var perr error
		u, perr = url.Parse(c.APIURL)
		if perr != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, fmt.Errorf("invalid LLM endpoint %q (expected http(s)://host/v1)", c.APIURL)
		}
	}
	if strings.TrimSpace(c.Model) == "" {
		return nil, errors.New("model must not be empty")
	}
	if c.MaxSteps < 1 || c.MaxSteps > MaxAgentSteps {
		return nil, fmt.Errorf("-max-steps must be between 1 and %d", MaxAgentSteps)
	}
	if c.RunTimeout < 10*time.Second || c.RunTimeout > MaxRunMinutes*time.Minute {
		return nil, fmt.Errorf("-run-timeout must be between 10s and %dm", MaxRunMinutes)
	}
	if c.SkillsDir = strings.TrimSpace(c.SkillsDir); c.SkillsDir == "" {
		c.SkillsDir = DefaultSkillsDir()
	}
	if c.PluginsDir = strings.TrimSpace(c.PluginsDir); c.PluginsDir == "" {
		c.PluginsDir = HomePath("plugins")
	}
	if c.APIKey == "" && c.APIURL != "" {
		warnings = append(warnings, "no LLM API key configured ("+EnvAPIKey+"); requests will be sent without authentication")
	}
	flagSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { flagSet[f.Name] = true })
	if flagSet["api-key"] {
		warnings = append(warnings, "the API key was passed as a flag and is visible in the process list; use "+EnvAPIKey+" or a .env file instead")
	}
	if u != nil && u.Scheme == "http" && !isLocalHost(u.Hostname()) {
		warnings = append(warnings, "the LLM endpoint uses plain http: the API key and every conversation travel unencrypted; use https or a VPN")
	}
	if c.UseEnvProxy {
		warnings = append(warnings, "-use-env-proxy is on: every request of the tools goes through the proxy")
	}
	return warnings, nil
}

func isLocalHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

func LoadDefaultDotEnv() error {
	err := LoadDotEnv(EnvFilePath())
	AliasEnv()
	return err
}

func ParseIDs(s string) (map[int64]bool, error) {
	out := map[int64]bool{}
	for _, f := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' || r == ';' || r == '\n' }) {
		id, err := strconv.ParseInt(f, 10, 64)
		if err != nil || id <= 0 {
			return nil, fmt.Errorf("%q is not a valid Telegram user id (a positive number)", f)
		}
		out[id] = true
	}
	return out, nil
}

func Location() (*time.Location, error) {
	name := envOr(EnvTimezone, "Asia/Tehran")
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%s=%q: %w", EnvTimezone, name, err)
	}
	return loc, nil
}
