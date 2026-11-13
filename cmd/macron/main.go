package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func main() {
	config.AdoptLegacyEnv()
	config.ActivateToolEnv()
	args := os.Args[1:]
	switch {
	case len(args) > 0 && (args[0] == "bale" || args[0] == "rubika" || args[0] == "eitaa"):
		if len(args) < 2 || args[1] != "run" {
			fmt.Fprintf(os.Stderr, "usage: macron %s run [flags]\n", args[0])
			os.Exit(2)
		}
		if err := runMessenger(args[0], args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case len(args) == 0 && interactiveTerminal():
		os.Exit(runTerminal())
	case len(args) > 1 && (args[0] == "telegram" || args[0] == "tg") && args[1] == "run":
		args = args[2:]
	case len(args) > 0 && args[0] == "desktop":
		os.Exit(runDesktop(args[1:]))
	case len(args) > 0 && runCommand(args):
		return
	}
	runBot(args)
}

func runBot(args []string) {
	if startupCheck(args) {
		if err := runStartupCheck(args); err != nil {
			log.Fatal(err)
		}
		return
	}
	_ = config.LoadDefaultDotEnv()
	cleanCgroup()
	side := sideMessengers()
	if strings.TrimSpace(os.Getenv(config.EnvBotToken)) == "" {
		platform := strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_MESSENGER")))
		switch {
		case len(side) == 1:
			platform = side[0]
		case len(side) > 1:
			sup := superviseMessengers(side)
			ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			<-ctx.Done()
			stop()
			sup.stop()
			return
		}
		if platform != "" && platform != "telegram" {
			if err := runMessenger(platform, args); err != nil {
				log.Fatal(err)
			}
			return
		}
	} else if len(side) > 0 {
		sup := superviseMessengers(side)
		defer sup.stop()
	}
	cfg := &config.Config{OptionalAPIURL: true}
	cfg.Bind(flag.CommandLine)
	tokenFlag := flag.String("token", os.Getenv(config.EnvBotToken), "Telegram bot token — prefer env "+config.EnvBotToken)
	dsnFlag := flag.String("db", envOrDefault(config.EnvDB, config.HomePath(config.FileDB)), "the SQLite database file (env "+config.EnvDB+")")
	ownersFlag := flag.String("owners", os.Getenv(config.EnvOwners), "comma separated Telegram ids of the owners: the only people the agent answers (env "+config.EnvOwners+")")
	rate := flag.Int("rate-limit", 0, "max requests per minute per user (0 = no limit, the default: only the owner can write)")
	maxJobs := flag.Int("max-concurrent", 64, "max requests processed at the same time")
	check := flag.Bool("check", false, "validate local startup and print ok VERSION (use --check-online to test the messenger)")
	_ = flag.CommandLine.Parse(args)

	flag.Visit(func(f *flag.Flag) {
		if f.Name == "token" {
			log.Printf("⚠️  -%s was passed as a flag and is visible in the process list; prefer the environment or a .env file", f.Name)
		}
	})
	warnings, err := cfg.Validate(flag.CommandLine)
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	for _, w := range warnings {
		log.Printf("⚠️  %s", w)
	}
	if *tokenFlag == "" {
		log.Fatalf("❌ Telegram is not connected yet (%s). Run «macron telegram»: it asks for the bot token from @BotFather and your Telegram id", config.EnvFilePath())
	}
	owners, err := config.ParseIDs(*ownersFlag)
	if err != nil {
		log.Fatalf("❌ %s: %v", config.EnvOwners, err)
	}
	if len(owners) == 0 {
		log.Fatalf("❌ no owner: the bot answers only its owners. Run «macron telegram setup», or put your Telegram id in %s (send /id to @userinfobot to see it)", config.EnvOwners)
	}
	secretKey, err := config.SecretKey()
	if err != nil {
		log.Fatalf("❌ %v", err)
	}
	loc, err := config.Location()
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	jobCtx, cancelJobs := context.WithCancel(context.Background())
	defer cancelJobs()

	st, err := store.Open(rootCtx, *dsnFlag, store.Options{Location: loc, SecretKey: secretKey})
	if err != nil {
		log.Fatalf("❌ database: %v", err)
	}
	defer st.Close()
	seedProvider(rootCtx, st, cfg)

	llmHTTP := llm.NewHTTPClient()
	usage := func(provider string, u llm.Usage, failed bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.RecordStat(ctx, store.StatProvider, provider, failed, u.PromptTokens, u.CompletionTokens)
	}
	prov, err := providers.New(rootCtx, st, llmHTTP, usage)
	if err != nil {
		log.Fatalf("❌ providers: %v", err)
	}
	if !prov.Configured() {
		log.Printf("⚠️  no LLM provider yet: add one in the admin panel (/settings → پروایدرها)")
	}

	apiBase := strings.TrimRight(os.Getenv(config.EnvTelegramAPI), "/")
	if apiBase == "" {
		apiBase = "https://api.telegram.org"
	}
	localAPI := telegramLocal(apiBase, os.Getenv(config.EnvTelegramLocal))
	if localAPI {
		tools.SetSendLimit(telegram.LocalMaxBytes)
		answerSendBudget = localSendBudget
		log.Printf("📦 local Bot API server at %s: files up to %d MB both ways", apiBase, telegram.LocalMaxBytes>>20)
	}

	window := contextWindow(st, prov, cfg.ContextWindow)
	br, err := brain.Open(config.Home(), agent.DefaultSoul)
	if err != nil {
		log.Fatalf("❌ the agent's home folder %s: %v", config.Home(), err)
	}
	a, err := app.NewWith(rootCtx, cfg, agent.ChannelTelegram, prov, agent.Options{Location: loc, Soul: br.Soul,
		ContextWindow: window})
	if err != nil {
		log.Fatalf("❌ %v", err)
	}

	a.Search.Treg.SetTokenSource(tregToken(st, cfg.TregToken))
	cookies := newCookiePool(st, cfg.CookiesDir)
	migrateSingleCookie(rootCtx, st)
	cookies.http = a.Registry.HTTP
	a.Registry.SetCookiePool(cookies)
	a.Registry.SetUserCookies(cookies.user)
	go guard.KeepRunning(rootCtx, "the cookie refresher", cookies.refreshLoop)
	go guard.KeepRunning(rootCtx, "the cookie checker", func(ctx context.Context) { cookies.checkLoop(ctx, a.Registry.TestCookie) })

	mem := memory.New(st, prov, func() int { return historyBudget(st, window()) })

	api := telegram.NewBotWithBase(*tokenFlag, apiBase)
	api.SetLocal(localAPI)
	ctx, cancel := context.WithTimeout(rootCtx, 20*time.Second)
	me, err := api.GetMe(ctx)
	cancel()
	if err != nil {
		log.Fatalf("❌ cannot connect to Telegram: %v", err)
	}
	if *check {
		fmt.Printf("ok %s @%s\n", app.Version, me.Username)
		return
	}

	bot := &Bot{
		cookies: cookies,
		api:     api, app: a, st: st, prov: prov, mem: mem, me: *me, loc: loc, owner: owners,
		gate:       guard.NewGate(*maxJobs),
		flood:      guard.NewRateLimiter(*rate, time.Minute),
		runTimeout: cfg.RunTimeout,
		envWindow:  cfg.ContextWindow,
		jobCtx:     jobCtx,
		started:    time.Now(),
		personal:   true,
		brain:      br,
		workFiles:  cfg.WorkspaceTTL,
	}
	a.Registry.Gate, a.Registry.CheckpointDir = bot.newCommandGate(), config.HomePath("checkpoints")
	a.Agent.Teachings = bot.teachings
	api.OnSent = bot.noteSent
	bot.reloadEmoji(rootCtx)
	mem.UserBudget = bot.historyFor
	a.Agent.Redact = bot.redact

	a.Registry.SetSkillWriter(bot.sessionAdmin)
	a.Registry.SetConnectionAdmin(bot.sessionAdmin)

	bot.plugins = &plugins.Manager{Dir: cfg.PluginsDir, MCPFile: cfg.MCPConfig, Machine: a.Machine,
		HTTP:   pluginHTTP(cfg.UseEnvProxy),
		States: pluginStates{st}, Version: app.Version, IsAdmin: bot.sessionAdmin, Reserved: a.Registry.Builtin}
	bot.plugins.Reload(rootCtx)
	a.Registry.SetPlugins(bot.plugins)
	defer bot.plugins.Close()
	if n := len(bot.plugins.List()); n > 0 {
		log.Printf("🔌 %d plugins and MCP servers from %s", n, cfg.PluginsDir)
	}

	ctx, cancel = context.WithTimeout(rootCtx, 30*time.Second)
	if err := api.DeleteWebhook(ctx); err != nil {
		log.Printf("⚠️  could not clear a webhook: %v", err)
	}
	if err := bot.registerCommands(ctx); err != nil {
		log.Printf("⚠️  could not register the command menu: %v", err)
	}
	cancel()

	orphans := bot.orphanedJobs(rootCtx)
	go func() { defer guard.Recover("the restart notice"); bot.tellRestarted(rootCtx, orphans) }()
	bgOrphans := bot.orphanedBgTasks(rootCtx)
	go func() { defer guard.Recover("the restart notice"); bot.tellBgRestarted(rootCtx, bgOrphans) }()
	go guard.KeepRunning(rootCtx, "housekeeping", func(ctx context.Context) { housekeeping(ctx, st) })
	bot.startSysmon(rootCtx)
	go guard.KeepRunning(rootCtx, "the scheduler", bot.runScheduler)
	go guard.KeepRunning(rootCtx, "the weekly reports", bot.runWeeklyReports)
	go guard.KeepRunning(rootCtx, "the scheduled evals", bot.runEvalSchedule)
	go guard.KeepRunning(rootCtx, "the update notices", bot.runUpdateNotices)
	bot.startLate(rootCtx)
	bot.serveWeb(rootCtx)
	go func() { defer guard.Recover("the update report"); bot.reportUpdate(rootCtx) }()

	log.Printf("🤖 @%s is online (Macron Agent %s · home %s · time zone %s)", me.Username, app.Version, config.Home(), loc)
	poll(rootCtx, bot)

	log.Println("🛑 shutting down: letting running requests finish (max 30s)…")
	done := make(chan struct{})
	go func() { bot.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		log.Println("forcing shutdown")
		cancelJobs()
		<-time.After(2 * time.Second)
	}
}

func seedProvider(ctx context.Context, st *store.Store, cfg *config.Config) {
	if cfg.APIURL == "" {
		return
	}
	list, err := st.Providers(ctx)
	if err != nil || len(list) > 0 {
		return
	}
	base, api := llm.CleanBaseURL(cfg.APIURL)
	if _, err := st.AddProvider(ctx, store.Provider{Name: "default", BaseURL: base, API: api, APIKey: cfg.APIKey, Model: cfg.Model,
		Priority: 10, Enabled: true, Vision: true}); err != nil {
		log.Printf("⚠️  could not create the default provider: %v", err)
		return
	}
	log.Printf("created the model provider %q from the settings; more are added in /settings → providers", "default")
}

func poll(ctx context.Context, bot *Bot) {
	var offset int64
	skipTo := bot.lastHandledUpdate()
	defer func() { bot.confirmUpdates(offset) }()
	backoff := time.Second
	for ctx.Err() == nil {
		pctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		updates, err := bot.api.GetUpdates(pctx, offset, 30)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			hint := ""
			var api *telegram.APIError
			if errors.As(err, &api) && api.Code == 409 || strings.Contains(err.Error(), "Conflict") {
				hint = " (is another instance of this bot running?)"
			}
			log.Printf("⚠️  getUpdates failed: %v%s — retrying in %s", err, hint, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if u.UpdateID <= skipTo {
				log.Printf("update %d was handled before the restart; skipped", u.UpdateID)
				continue
			}
			bot.dispatch(u)
		}
		if len(updates) > 0 {
			go bot.markHandled(offset - 1)
		}
	}
}

func housekeeping(ctx context.Context, st *store.Store) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		hctx, cancel := context.WithTimeout(ctx, time.Minute)
		if n, err := st.DeleteOldPolls(hctx, 30*24*time.Hour); err != nil {
			log.Printf("housekeeping: %v", err)
		} else if n > 0 {
			log.Printf("housekeeping: removed %d old polls", n)
		}
		if n, err := st.PurgeGroupMessages(hctx); err != nil {
			log.Printf("housekeeping: %v", err)
		} else if n > 0 {
			log.Printf("housekeeping: removed %d expired group messages", n)
		}
		if removed, err := st.Retention(hctx); err != nil {
			log.Printf("housekeeping: %v", err)
		} else if len(removed) > 0 {
			log.Printf("housekeeping: removed old records %v", removed)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
