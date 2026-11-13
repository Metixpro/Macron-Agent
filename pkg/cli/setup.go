package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	neturl "net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	keyToken    = "TELEGRAM_BOT_TOKEN"
	keyOwners   = "MACRON_OWNERS"
	keyAPIURL   = "MACRON_API_URL"
	keyAPIKey   = "MACRON_API_KEY"
	keyModel    = "MACRON_MODEL"
	keyTimezone = "MACRON_TIMEZONE"
)

var presets = []struct{ name, url, model string }{
	{"OpenAI-compatible (any address)", "", ""},
	{"OpenAI", "https://api.openai.com/v1", "gpt-5"},
	{"Anthropic (Claude)", "https://api.anthropic.com", "claude-sonnet-5-5"},
	{"OpenRouter", "https://openrouter.ai/api/v1", ""},
	{"kie.ai (Responses API)", "https://api.kie.ai/openai/v1/responses", ""},
	{"A model on this machine (Ollama)", "http://127.0.0.1:11434/v1", ""},
}

func telegramBase() string {
	v := os.Getenv(config.EnvTelegramAPI)
	if strings.TrimSpace(v) == "" {
		if env, err := loadEnvFile(config.EnvFilePath()); err == nil {
			v = getEnv(env, config.EnvTelegramAPI)
		}
	}
	if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
		return v
	}
	return "https://api.telegram.org"
}

func setup(io *IO, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	token := fs.String("token", "", "also connect this Telegram bot (the token from @BotFather)")
	owner := fs.String("owner", "", "your Telegram numeric id, for the bot (comma separated for several owners)")
	apiURL := fs.String("api-url", "", "the model API address")
	apiKey := fs.String("api-key", "", "the model API key (prefer the prompt: a flag shows in the process list)")
	providerName := fs.String("provider", "", "create or update this named model provider (default: the active provider)")
	model := fs.String("model", "", "the model name")
	tz := fs.String("timezone", "", "your time zone, e.g. Asia/Tehran")
	yes := fs.Bool("yes", false, "ask nothing: use the flags and what config.env has already")
	noService := fs.Bool("no-service", false, "do not start the Telegram bot in the background")
	noTelegram := fs.Bool("no-telegram", false, "do not offer to connect Telegram")
	skipTest := fs.Bool("skip-test", false, "do not call the model to test it")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	interactive := io.Interactive && !*yes
	ctx := context.Background()

	envPath := config.EnvFilePath()
	env, err := loadEnvFile(envPath)
	if err != nil {
		io.printf("❌ cannot read %s: %v\n", envPath, err)
		return 1
	}
	if err := loadSetupProvider(ctx, env, *providerName); err != nil {
		io.printf("❌ Cannot read provider settings: %v\n", err)
		return 1
	}
	io.printf("\n✻ %sMacron Agent setup%s  %shome folder %s%s\n\n", cBold, cReset, cDim, config.Home(), cReset)
	if code := setupModel(ctx, io, env, interactive, *apiURL, *apiKey, *model, *skipTest, true); code != 0 {
		return code
	}
	if code := setupZone(io, env, *tz, interactive); code != 0 {
		return code
	}
	if err := env.Save(); err != nil {
		io.printf("❌ cannot save %s: %v\n", envPath, err)
		return 1
	}
	if getEnv(env, keyAPIURL) != "" {
		if err := saveSetupProvider(ctx, env, *providerName); err != nil {
			io.printf("❌ Config saved, but the provider could not be updated: %v\n", err)
			return 1
		}
	}
	io.printf("✅ Saved %s (only you can read it).\n", envPath)

	switch {
	case *token != "" || *owner != "":
		return connectTelegram(io, *token, *owner, interactive, *noService)
	case interactive && !*noTelegram && getEnv(env, keyToken) == "":
		io.println("\nMacron can also live in Telegram, so you reach it from your phone (you can do this later: macron telegram).")
		if io.yes("Connect a Telegram bot now?", false) {
			return connectTelegram(io, "", "", true, *noService)
		}
	}
	if os.Getenv("MACRON_INSTALLER") == "" {
		io.println("\nStart: run «macron» in any project folder.")
	}
	return 0
}

func FirstRun(io *IO) int {
	envPath := config.EnvFilePath()
	env, err := loadEnvFile(envPath)
	if err != nil {
		io.printf("❌ cannot read %s: %v\n", envPath, err)
		return 1
	}
	io.printf("\n✻ Welcome to Macron Agent\n\n  Macron needs a model to think with. This takes a minute and is saved in %s.\n\n", envPath)
	if code := setupModel(context.Background(), io, env, io.Interactive, "", "", "", false, false); code != 0 {
		return code
	}
	if getEnv(env, keyAPIURL) == "" {
		return 1
	}
	if getEnv(env, keyTimezone) == "" {
		env.Set(keyTimezone, defaultZone())
	}
	if err := env.Save(); err != nil {
		io.printf("❌ cannot save %s: %v\n", envPath, err)
		return 1
	}
	io.println("✅ Saved. Starting Macron…")
	return 0
}

func setupZone(io *IO, env *envFile, flagZone string, interactive bool) int {
	zone := firstNonEmpty(flagZone, getEnv(env, keyTimezone))
	if zone == "" {
		zone = defaultZone()
		if interactive {
			io.println("Your time zone (reminders and schedules follow it).")
			zone = io.ask("     Time zone", zone)
		}
	}
	if _, err := time.LoadLocation(zone); err != nil {
		io.printf("❌ unknown time zone %q (examples: Asia/Tehran, Europe/Berlin, America/New_York)\n", zone)
		return 1
	}
	env.Set(keyTimezone, zone)
	io.printf("     ✓ time zone: %s\n\n", zone)
	return 0
}

func setupModel(ctx context.Context, io *IO, env *envFile, interactive bool, flagURL, flagKey, flagModel string, skipTest, later bool) int {
	url := firstNonEmpty(flagURL, getEnv(env, keyAPIURL))
	key := firstNonEmpty(flagKey, getEnv(env, keyAPIKey))
	model := firstNonEmpty(flagModel, getEnv(env, keyModel))
	if url == "" && interactive {
		io.printf("%s %sthe model Macron thinks with%s\n", question("Which service?"), cDim, cReset)
		for i, p := range presets {
			io.printf("       %s%d%s  %s\n", cAccent, i+1, cReset, p.name)
		}
		if later {
			io.printf("       %s%d%s  %sLater%s\n", cAccent, len(presets)+1, cReset, cDim, cReset)
		}
		n, _ := strconv.Atoi(io.ask("     Choose", "1"))
		switch {
		case later && n == len(presets)+1:
			io.println("     ↷ skipped: Macron asks for it the first time you run «macron».")
			return 0
		case n >= 1 && n <= len(presets):
			p := presets[n-1]
			url = io.ask("     API address", p.url)
			if model == "" {
				model = p.model
			}
		default:
			io.println("❌ choose a number from the list")
			return 1
		}
	}
	if interactive && url != "" {
		if flagURL == "" && getEnv(env, keyAPIURL) != "" {
			url = io.ask("     API address", url)
		}
		before, _ := neturl.Parse(getEnv(env, keyAPIURL))
		after, _ := neturl.Parse(url)
		if flagKey == "" && before != nil && after != nil && before.Host != after.Host {
			key = ""
		}
		if flagKey == "" {
			prompt := "     API key (hidden; blank = no key): "
			if key != "" {
				prompt = "     API key (hidden; blank = keep, - = clear): "
			}
			k, err := io.Secret(prompt)
			if err != nil {
				return 1
			}
			if k == "-" {
				key = ""
			} else if k != "" {
				key = k
			}
		}
	}
	if !interactive && flagKey == "" {
		before, _ := neturl.Parse(getEnv(env, keyAPIURL))
		after, _ := neturl.Parse(url)
		if before != nil && after != nil && before.Host != after.Host {
			key = ""
		}
	}
	if key == "-" {
		key = ""
	}
	if url == "" {
		io.println("The model: none given yet; Macron asks for it the first time you run «macron».")
		return 0
	}
	base, api := llm.CleanBaseURL(url)
	if err := providers.ValidateBaseURL(base); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	c := llm.NewClient(base, key, model)
	if api != "" {
		c.API = api
	}
	if interactive && flagModel == "" {
		lctx, cancel := context.WithTimeout(ctx, 25*time.Second)
		models, err := c.Models(lctx)
		cancel()
		if err == nil && len(models) > 0 {
			io.printf("     The service offers %d models, for example:\n", len(models))
			for i, m := range models {
				if i == 15 {
					io.printf("       … and %d more\n", len(models)-15)
					break
				}
				io.printf("       - %s\n", m)
			}
		}
		model = io.ask("     Model name", model)
	}
	if model == "" {
		io.println("❌ no model name: pass --model")
		return 1
	}
	c.Model = model
	if !skipTest {
		tctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		reply, err := c.Chat(tctx, []llm.Message{{Role: "user", Content: "Reply with the single word: ok"}}, nil)
		cancel()
		if err != nil {
			io.printf("     ✗ the model did not answer: %v\n", shorten(err))
			if !interactive || !io.yes("     Save these settings anyway?", false) {
				return 1
			}
		} else {
			io.printf("     ✓ %s answers (%q)\n\n", model, strings.TrimSpace(reply.Content))
		}
	}
	env.Set(keyAPIURL, url)
	env.Set(keyAPIKey, key)
	env.Set(keyModel, model)
	return 0
}

func askOwner(ctx context.Context, io *IO, token string) string {
	bot := telegram.NewBotWithBase(token, telegramBase())
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_ = bot.DeleteWebhook(dctx)
	cancel()
	var offset int64
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		if io.In.Buffered() > 0 || inputReady(200*time.Millisecond) {
			s, err := io.In.ReadString('\n')
			if s = strings.TrimSpace(s); s != "" {
				return s
			}
			if err != nil {
				return ""
			}
			io.print("     (waiting for your message to the bot…) ")
		}
		uctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ups, err := bot.GetUpdates(uctx, offset, 2)
		cancel()
		if err != nil {
			time.Sleep(time.Second)
			continue
		}
		for _, u := range ups {
			offset = u.UpdateID + 1
			if m := u.Message; m != nil && m.From != nil && m.Chat.Type == "private" && !m.From.IsBot {
				name := strings.TrimSpace(m.From.FirstName + " " + m.From.LastName)
				io.printf("     I saw a message from %s (id %d).\n", name, m.From.ID)
				if io.yes("     Is that you?", true) {
					return strconv.FormatInt(m.From.ID, 10)
				}
			}
		}
	}
	return ""
}

func (io *IO) print(a ...any) { fmt.Fprint(io.Out, a...) }

func getEnv(env *envFile, key string) string {
	if v, ok := env.Get(key); ok {
		return v
	}
	return ""
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func shorten(err error) string {
	s := strings.Join(strings.Fields(err.Error()), " ")
	if r := []rune(s); len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}

func defaultZone() string {
	if z := systemZone(); z != "UTC" && z != "Etc/UTC" {
		return z
	}
	return "Asia/Tehran"
}

func systemZone() string {
	if tz := os.Getenv("TZ"); tz != "" {
		if _, err := time.LoadLocation(tz); err == nil {
			return tz
		}
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil {
		if tz := strings.TrimSpace(string(b)); tz != "" {
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	if out, err := exec.Command("timedatectl", "show", "-p", "Timezone", "--value").Output(); err == nil {
		if tz := strings.TrimSpace(string(out)); tz != "" {
			if _, err := time.LoadLocation(tz); err == nil {
				return tz
			}
		}
	}
	if name := time.Local.String(); name != "" && name != "Local" {
		return name
	}
	return "UTC"
}

var errNoService = errors.New("services are supported with systemd (Linux) and launchd (macOS)")

func saveSetupProvider(ctx context.Context, env *envFile, name string) error {
	key, err := config.SecretKey()
	if err != nil {
		return err
	}
	path := config.ToolEnvValue(config.EnvDB)
	if path == "" {
		path = config.HomePath(config.FileDB)
	}
	st, err := store.Open(ctx, path, store.Options{SecretKey: key})
	if err != nil {
		return err
	}
	defer st.Close()
	_, err = providers.Configure(ctx, st, name, getEnv(env, keyAPIURL), getEnv(env, keyAPIKey), getEnv(env, keyModel))
	return err
}

func loadSetupProvider(ctx context.Context, env *envFile, name string) error {
	path := config.ToolEnvValue(config.EnvDB)
	if path == "" {
		path = config.HomePath(config.FileDB)
	}
	if !fileExists(path) {
		return nil
	}
	key, err := config.SecretKey()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, path, store.Options{SecretKey: key})
	if err != nil {
		return err
	}
	defer st.Close()
	list, err := st.Providers(ctx)
	if err != nil {
		return err
	}
	active, _ := st.Setting(store.SettingActiveProvider)
	var selected *store.Provider
	for _, p := range list {
		p := p
		if name != "" && p.Name == name || name == "" && fmt.Sprint(p.ID) == active {
			selected = &p
			break
		}
	}
	if selected == nil && name == "" {
		for _, p := range list {
			p := p
			if p.Enabled {
				selected = &p
				break
			}
		}
	}
	if selected == nil {
		return nil
	}
	p := *selected
	endpoint := strings.TrimRight(p.BaseURL, "/")
	switch providers.API(p) {
	case llm.APIResponses:
		endpoint += "/responses"
	case llm.APIAnthropic:
		if !strings.HasSuffix(endpoint, "/v1") {
			endpoint += "/v1"
		}
		endpoint += "/messages"
	}
	env.Set(keyAPIURL, endpoint)
	env.Set(keyAPIKey, p.APIKey)
	env.Set(keyModel, p.Model)
	return nil
}
