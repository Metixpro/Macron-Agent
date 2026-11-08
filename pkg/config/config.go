// Package config loads settings from flags and environment variables.
//
// Secrets (LLM key, Telegram token, server token) should come from the
// environment or a .env file — values passed as flags are visible to every
// user of the machine through `ps`.
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
	_ "time/tzdata" // time zones even on minimal servers
)

// Environment variable names.
const (
	EnvAPIURL        = "WAZE_API_URL"
	EnvAPIKey        = "WAZE_API_KEY"
	EnvModel         = "WAZE_MODEL"
	EnvWorkspaceDir  = "WAZE_WORKSPACE_DIR"
	EnvSnapKey       = "WAZE_SNAP_KEY" // SubQ's key for the fast classifier (the admin panel's wins; see cmd/bot/snap.go)
	EnvSnapURL       = "WAZE_SNAP_URL" // SubQ's API address (default https://api.subq.ai/v1)
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
	EnvTregToken     = "WAZE_TREG_TOKEN"     // treg.to token: web search goes there first
	EnvReaderURL     = "WAZE_READER_URL"     // reader service for pages that block bots (default r.jina.ai; "off" disables it)
	EnvReaderKey     = "WAZE_READER_KEY"     // its API key (optional; a Jina key raises its rate limit)
	EnvImageAPI      = "WAZE_IMAGE_API_URL"  // AI image engine of generate_image (default: the txt2img service; "off" = the fallbacks only)
	EnvImageKey      = "WAZE_IMAGE_API_KEY"  // its api_key (the bot's admin panel can set it too)
	EnvImageEditURL  = "WAZE_IMAGE_EDIT_URL" // the Nano Banana task service of edit_image ("off" disables editing)
	EnvImageEditKey  = "WAZE_IMAGE_EDIT_KEY" // its X-API-Key (the bot's admin panel can set it too)
	EnvMusicURL      = "WAZE_MUSIC_API_URL"  // the song service of generate_song ("off" disables songs)
	EnvMusicKey      = "WAZE_MUSIC_API_KEY"  // its X-API-Key (the bot's admin panel can set it too)
	EnvPiperModel    = "WAZE_PIPER_MODEL"    // the local Persian voice of text_to_speech (a Piper .onnx; "off" = Edge TTS only)
	EnvSTTURL        = "WAZE_STT_URL"        // OpenAI-compatible speech to text (…/v1); default: the providers; "off" disables it
	EnvSTTKey        = "WAZE_STT_KEY"        // its API key
	EnvSTTModel      = "WAZE_STT_MODEL"      // its model (default whisper-1; Groq: whisper-large-v3-turbo)
	EnvSTTTimeout    = "WAZE_STT_TIMEOUT"    // one recording's time limit, e.g. 8m (default 5m; a local whisper on a small server needs more)
	EnvContextWindow = "WAZE_CONTEXT_WINDOW" // the model's context window in tokens (default: guessed from the model's name)
	EnvSkillsDir     = "WAZE_SKILLS_DIR"     // procedural skills on disk (default /etc/waze/skills, else ./skills)
	EnvSkillsWrite   = "WAZE_SKILLS_WRITE"   // the REST API may save skills
	EnvPluginsDir    = "WAZE_PLUGINS_DIR"    // the admins' plugins, one folder each (default /etc/waze/plugins, else beside the workspaces)
	EnvMCPConfig     = "WAZE_MCP_CONFIG"     // MCP servers in mcp_servers.json form (default /etc/waze/mcp_servers.json)
	EnvTelegramAPI   = "WAZE_TELEGRAM_API"   // a local Bot API server (or a proxy of api.telegram.org)
	EnvTelegramLocal = "WAZE_TELEGRAM_LOCAL" // 1: that server runs with --local (files up to 2000 MB); 0: it does not; unset: guessed
)

const DefaultModel = "claude-sonnet-5"

// DefaultImageAPI is the txt2img service: generate_image's first engine (76
// styles, ten picture shapes). Without its key the fallbacks make the picture.
const DefaultImageAPI = "https://nanobanana.irjh.top/api/txt2img/v1/index.php"

// DefaultImageEditAPI is the Nano Banana task service: edit_image, and
// generate_image's second engine.
const DefaultImageEditAPI = "https://nanobanana.irjh.top/v2/index.php"

// DefaultMusicAPI is the song service: generate_song makes songs with a
// singer from lyrics. Without its key the tool is not offered.
const DefaultMusicAPI = "https://nanobanana.irjh.top/music/index.php"

// DefaultPiperModel is the Persian voice the installer puts on the server:
// Mana (MahtaFetrat/Mana-Persian-Piper, a Piper medium voice trained on
// the ManaTTS corpus; natural, 63 MB).
const DefaultPiperModel = "/opt/piper-voices/fa_IR-mana-medium.onnx"

// olderPiperModels are the voices earlier installers put there: a server
// that has one of them and not Mana keeps its local voice.
var olderPiperModels = []string{"/opt/piper-voices/fa_IR-mantatts-par.onnx"}

// defaultPiper is Mana, or an older voice on a server that has only that.
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

// Config is shared by the CLI, the Telegram bot and the REST server.
type Config struct {
	APIURL        string
	APIKey        string
	Model         string
	WorkspaceDir  string
	WorkspaceTTL  time.Duration
	SnapKey       string // SubQ's API key from the environment ("" = the panel's, or none)
	SnapURL       string
	UseEnvProxy   bool
	GitHubToken   string
	TregToken     string
	ReaderURL     string // "" = off
	ReaderKey     string
	ImageAPI      string // "" = Flux only
	ImageKey      string
	ImageEditAPI  string // "" = no AI editing
	ImageEditKey  string
	MusicAPI      string // "" = no songs
	MusicKey      string
	PiperModel    string // the local Persian voice (Piper); "" = Edge TTS only
	STTURL        string // speech to text; "" = the providers
	STTKey        string
	STTModel      string
	STTOff        bool
	STTTimeout    time.Duration
	ContextWindow int    // tokens; 0 = guessed from the model's name
	SkillsDir     string // procedural skills (SKILL.md folders), outside the workspaces
	SkillsWrite   bool   // the REST API may save skills (the bot lets its admins, the CLI always)
	PluginsDir    string // the admins' plugins (plugin.json folders)
	MCPConfig     string // mcp_servers.json
	CookiesDir    string // cookies.txt files for sites that ask for a login
	MaxSteps      int
	Slots         int // commands that run at the same time (0 = 8, or twice the CPUs)
	RunTimeout    time.Duration
	// OptionalAPIURL lets front-ends that manage providers elsewhere (the
	// Telegram bot keeps them in its database) start without WAZE_API_URL.
	OptionalAPIURL bool
}

// LoadDotEnv reads KEY=VALUE lines from path into the process environment.
// Variables that are already set win. A missing file is not an error.
func LoadDotEnv(path string) error {
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

// Bind registers the shared flags; defaults come from the environment.
func (c *Config) Bind(fs *flag.FlagSet) {
	fs.StringVar(&c.APIURL, "api-url", envOr(EnvAPIURL, ""), "OpenAI-compatible base URL, e.g. https://host/v1 (env "+EnvAPIURL+")")
	fs.StringVar(&c.APIKey, "api-key", os.Getenv(EnvAPIKey), "LLM API key — prefer the "+EnvAPIKey+" environment variable")
	fs.StringVar(&c.Model, "model", envOr(EnvModel, DefaultModel), "model id (env "+EnvModel+")")
	fs.StringVar(&c.WorkspaceDir, "workspace-dir", envOr(EnvWorkspaceDir, HomePath("workspaces")), "base directory of the per-user workspaces (env "+EnvWorkspaceDir+")")
	fs.DurationVar(&c.WorkspaceTTL, "workspace-ttl", 0, "delete work-folder files (uploads and results) older than this (0 = never, the default)")
	c.SnapKey = strings.TrimSpace(os.Getenv(EnvSnapKey))
	c.SnapURL = strings.TrimSpace(os.Getenv(EnvSnapURL))
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
	c.ImageAPI = strings.TrimSpace(os.Getenv(EnvImageAPI))
	switch strings.ToLower(c.ImageAPI) {
	case "", "on", "default":
		c.ImageAPI = DefaultImageAPI
	case "off", "none", "disabled":
		c.ImageAPI = ""
	}
	if strings.Contains(strings.ToLower(c.ImageAPI), "nestapi.ir") {
		c.ImageAPI = DefaultImageAPI // the engine this variable used to name is gone
	}
	c.ImageKey = strings.TrimSpace(os.Getenv(EnvImageKey))
	c.ImageEditAPI = strings.TrimSpace(os.Getenv(EnvImageEditURL))
	switch strings.ToLower(c.ImageEditAPI) {
	case "", "on", "default":
		c.ImageEditAPI = DefaultImageEditAPI
	case "off", "none", "disabled":
		c.ImageEditAPI = ""
	}
	c.ImageEditKey = strings.TrimSpace(os.Getenv(EnvImageEditKey))
	c.MusicAPI = strings.TrimSpace(os.Getenv(EnvMusicURL))
	switch strings.ToLower(c.MusicAPI) {
	case "", "on", "default":
		c.MusicAPI = DefaultMusicAPI
	case "off", "none", "disabled":
		c.MusicAPI = ""
	}
	c.MusicKey = strings.TrimSpace(os.Getenv(EnvMusicKey))
	c.PiperModel = strings.TrimSpace(os.Getenv(EnvPiperModel))
	switch strings.ToLower(c.PiperModel) {
	case "", "on", "default":
		c.PiperModel = defaultPiper()
	case "off", "none", "disabled":
		c.PiperModel = ""
	}
	c.STTURL = strings.TrimRight(strings.TrimSpace(os.Getenv(EnvSTTURL)), "/")
	switch strings.ToLower(c.STTURL) {
	case "off", "none", "disabled":
		c.STTURL, c.STTOff = "", true
	case "on", "default", "auto":
		c.STTURL = ""
	}
	c.STTKey = strings.TrimSpace(os.Getenv(EnvSTTKey))
	c.STTModel = strings.TrimSpace(os.Getenv(EnvSTTModel))
	if v := strings.TrimSpace(os.Getenv(EnvSTTTimeout)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			c.STTTimeout = d
		} else if n, err := strconv.Atoi(v); err == nil && n > 0 {
			c.STTTimeout = time.Duration(n) * time.Second
		}
	}
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(EnvContextWindow))); err == nil && n >= 8000 {
		c.ContextWindow = n
	}
	c.SkillsWrite = envBool(EnvSkillsWrite)
}

// DefaultSkillsDir is /etc/waze/skills when it exists, else ./skills.
func DefaultSkillsDir() string { return HomePath("skills") }

// MaxAgentSteps and MaxRunMinutes bound the agent's budget per request.
const (
	MaxAgentSteps = 1000
	MaxRunMinutes = 24 * 60
)

// Validate checks the configuration after fs.Parse. Warnings are advice, not errors.
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

// LoadDefaultDotEnv loads $MACRON_ENV_FILE, or config.env in the home folder.
func LoadDefaultDotEnv() error {
	err := LoadDotEnv(EnvFilePath())
	AliasEnv()
	return err
}

// ParseIDs reads a list of Telegram ids separated by commas, spaces or semicolons.
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

// Location loads WAZE_TIMEZONE (default Asia/Tehran).
func Location() (*time.Location, error) {
	name := envOr(EnvTimezone, "Asia/Tehran")
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("%s=%q: %w", EnvTimezone, name, err)
	}
	return loc, nil
}
