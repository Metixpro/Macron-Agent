package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
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

var telegramAPI = "https://api.telegram.org"

func setup(io *IO, args []string) int {
	fs := flag.NewFlagSet("setup", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	token := fs.String("token", "", "the Telegram bot token from @BotFather")
	owner := fs.String("owner", "", "your Telegram numeric id (comma separated for several owners)")
	apiURL := fs.String("api-url", "", "the model API address")
	apiKey := fs.String("api-key", "", "the model API key (prefer the prompt: a flag shows in the process list)")
	model := fs.String("model", "", "the model name")
	tz := fs.String("timezone", "", "your time zone, e.g. Asia/Tehran")
	yes := fs.Bool("yes", false, "ask nothing: use the flags and what config.env has already")
	noService := fs.Bool("no-service", false, "do not offer to install the service")
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
	io.printf("\n🤖 Macron Agent setup · home folder: %s\n\n", config.Home())

	tok := firstNonEmpty(*token, getEnv(env, keyToken))
	var me *telegram.User
	for attempt := 0; attempt < 3; attempt++ {
		if tok == "" {
			if !interactive {
				io.println("❌ no bot token: pass --token (create a bot with @BotFather in Telegram and copy its token)")
				return 1
			}
			io.println("1/4  The Telegram bot. Open @BotFather in Telegram, send /newbot, and paste the token it gives you.")
			t, err := io.Secret("     Bot token (hidden): ")
			if err != nil {
				return 1
			}
			tok = t
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		u, err := telegram.NewBotWithBase(tok, telegramAPI).GetMe(cctx)
		cancel()
		if err == nil {
			me = u
			break
		}
		io.printf("     ✗ Telegram does not accept that token (%v)\n", shorten(err))
		if !interactive {
			return 1
		}
		tok = ""
	}
	if me == nil {
		return 1
	}
	io.printf("     ✓ bot @%s verified\n\n", me.Username)
	env.Set(keyToken, tok)

	owners := firstNonEmpty(*owner, getEnv(env, keyOwners))
	if owners == "" {
		if !interactive {
			io.println("❌ no owner: pass --owner with your Telegram numeric id")
			return 1
		}
		io.println("2/4  You. The agent answers its owner and nobody else. Send any message to @" + me.Username + " now and I will see your id,")
		io.println("     or type your numeric id here (the number @userinfobot shows) and press Enter.")
		owners = askOwner(ctx, io, tok)
		if owners == "" {
			io.println("❌ no owner")
			return 1
		}
	}
	if _, err := config.ParseIDs(owners); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	io.printf("     ✓ owner: %s\n\n", owners)
	env.Set(keyOwners, owners)

	zone := firstNonEmpty(*tz, getEnv(env, keyTimezone))
	if zone == "" {
		zone = defaultZone()
		if interactive {
			io.println("3/4  Your time zone (reminders and schedules follow it).")
			zone = io.ask("     Time zone", zone)
		}
	}
	if _, err := time.LoadLocation(zone); err != nil {
		io.printf("❌ unknown time zone %q (examples: Asia/Tehran, Europe/Berlin, America/New_York)\n", zone)
		return 1
	}
	env.Set(keyTimezone, zone)
	io.printf("     ✓ time zone: %s\n\n", zone)

	if code := setupModel(ctx, io, env, interactive, *apiURL, *apiKey, *model, *skipTest); code != 0 {
		return code
	}

	if err := env.Save(); err != nil {
		io.printf("❌ cannot save %s: %v\n", envPath, err)
		return 1
	}
	io.printf("\n✅ Saved %s (only you can read it).\n", envPath)

	switch {
	case *noService:
		io.printf("Then send /start to @%s.\n", me.Username)
		return 0
	case interactive && serviceSupported() && io.yes("\nKeep the agent running in the background and start it at boot?", true):
		if code := service(io, []string{"install"}); code != 0 {
			io.println("   (you can run it by hand with: macron)")
		}
		return 0
	}
	io.println("\nStart it now:   macron")
	io.println("As a service:   macron service install")
	io.println("Check:          macron doctor")
	io.printf("Then send /start to @%s.\n", me.Username)
	return 0
}

func setupModel(ctx context.Context, io *IO, env *envFile, interactive bool, flagURL, flagKey, flagModel string, skipTest bool) int {
	url := firstNonEmpty(flagURL, getEnv(env, keyAPIURL))
	key := firstNonEmpty(flagKey, getEnv(env, keyAPIKey))
	model := firstNonEmpty(flagModel, getEnv(env, keyModel))
	if url == "" && interactive {
		io.println("4/4  The model. Which service will the agent think with?")
		for i, p := range presets {
			io.printf("       %d) %s\n", i+1, p.name)
		}
		io.printf("       %d) Later: I will add it in Telegram (/settings → providers)\n", len(presets)+1)
		n, _ := strconv.Atoi(io.ask("     Choose", "1"))
		switch {
		case n == len(presets)+1:
			io.println("     ↷ skipped: nothing will answer until you add a provider in /settings.")
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
		if !strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") && key == "" {
			k, err := io.Secret("     API key (hidden): ")
			if err != nil {
				return 1
			}
			key = k
		}
	}
	if url == "" {
		io.println("4/4  The model: none given, so it is left for /settings → providers in Telegram.")
		return 0
	}
	base, api := llm.CleanBaseURL(url)
	c := llm.NewClient(base, key, model)
	if api != "" {
		c.API = api
	}
	if model == "" && interactive {
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
		model = io.ask("     Model name", "")
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
	if key != "" {
		env.Set(keyAPIKey, key)
	}
	env.Set(keyModel, model)
	return 0
}

func askOwner(ctx context.Context, io *IO, token string) string {
	typed := make(chan string, 1)
	go func() {
		s, _ := io.In.ReadString('\n')
		typed <- strings.TrimSpace(s)
	}()
	bot := telegram.NewBotWithBase(token, telegramAPI)
	dctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	_ = bot.DeleteWebhook(dctx)
	cancel()
	var offset int64
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		select {
		case s := <-typed:
			if s != "" {
				return s
			}
			io.print("     (waiting for your message to the bot…) ")
			go func() {
				s, _ := io.In.ReadString('\n')
				typed <- strings.TrimSpace(s)
			}()
		default:
		}
		uctx, cancel := context.WithTimeout(ctx, 8*time.Second)
		ups, err := bot.GetUpdates(uctx, offset, 2)
		cancel()
		if err != nil {
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
