package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/messenger"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func runMessenger(platform string, args []string) error {
	if platform == "eitaa" {
		return fmt.Errorf("Eitaayar supports outgoing messages only; an inbound Eitaa bot transport cannot be enabled with EITAA_BOT_TOKEN")
	}
	if err := config.LoadDefaultDotEnv(); err != nil {
		return err
	}
	fs := flag.NewFlagSet(platform+" run", flag.ContinueOnError)
	cfg := &config.Config{OptionalAPIURL: true}
	cfg.Bind(fs)
	upper := strings.ToUpper(platform)
	ownersRaw := fs.String("owners", os.Getenv("MACRON_"+upper+"_OWNERS"), "comma-separated owner IDs in this messenger (not Telegram IDs)")
	check := fs.Bool("check", false, "validate local messenger and model configuration without network calls")
	online := fs.Bool("check-online", false, "also verify the messenger token with its API")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if _, err := cfg.Validate(fs); err != nil {
		return err
	}
	owners := map[string]bool{}
	for _, id := range strings.Split(*ownersRaw, ",") {
		if id = strings.TrimSpace(id); id != "" {
			owners[id] = true
		}
	}
	if len(owners) == 0 || len(owners) > 64 {
		return fmt.Errorf("set MACRON_%s_OWNERS to between 1 and 64 owner IDs from %s", upper, platform)
	}
	client, err := messenger.New(platform, os.Getenv(upper+"_BOT_TOKEN"))
	if err != nil {
		return err
	}
	if base := os.Getenv("MACRON_" + upper + "_API"); base != "" {
		if err := client.SetAPIBase(base); err != nil {
			return err
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	secret, err := config.SecretKey()
	if err != nil {
		return err
	}
	loc, err := config.Location()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, envOrDefault(config.EnvDB, config.HomePath(config.FileDB)), store.Options{SecretKey: secret, Location: loc})
	if err != nil {
		return err
	}
	defer st.Close()
	seedProvider(ctx, st, cfg)
	prov, err := providers.New(ctx, st, llm.NewHTTPClient(), nil)
	if err != nil {
		return err
	}
	if !prov.Configured() {
		return fmt.Errorf("no model provider configured; set MACRON_API_URL, MACRON_API_KEY and MACRON_MODEL, or configure a provider first")
	}
	if *check && !*online {
		fmt.Printf("ok %s %s local\n", app.Version, platform)
		return nil
	}
	if err := client.Check(ctx); err != nil {
		return err
	}
	if *online {
		fmt.Printf("ok %s %s online\n", app.Version, platform)
		return nil
	}
	br, err := brain.Open(config.Home(), agent.DefaultSoul)
	if err != nil {
		return err
	}
	window := contextWindow(st, prov, cfg.ContextWindow)
	a, err := app.NewWith(ctx, cfg, agent.Channel(platform), prov, agent.Options{Soul: br.Soul, Location: loc, ContextWindow: window})
	if err != nil {
		return err
	}
	mem := memory.New(st, prov, func() int { return historyBudget(st, window()) })
	bot := &Bot{app: a, st: st, prov: prov, jobCtx: ctx, brain: br}
	a.Registry.Gate, a.Registry.CheckpointDir = bot.newCommandGate(), config.HomePath("checkpoints")
	a.Registry.SetSkillWriter(func(session string) bool { return strings.HasPrefix(session, platform+"-") })
	a.Registry.SetConnectionAdmin(func(session string) bool { return strings.HasPrefix(session, platform+"-") })
	pm := &plugins.Manager{Dir: cfg.PluginsDir, MCPFile: cfg.MCPConfig, Machine: a.Machine, HTTP: pluginHTTP(cfg.UseEnvProxy), States: pluginStates{st}, Version: app.Version, IsAdmin: func(session string) bool { return strings.HasPrefix(session, platform+"-") }, Reserved: a.Registry.Builtin}
	pm.Reload(ctx)
	defer pm.Close()
	a.Registry.SetPlugins(pm)
	cursorKey := client.CursorKey()
	cursor, _ := st.Setting(cursorKey)
	runner := &messenger.Runner{Platform: platform, Client: client, Private: client.Private, Owners: owners, Agent: a.Agent, Memory: mem, Timeout: cfg.RunTimeout, Cursor: cursor,
		SaveCursor: func(ctx context.Context, cursor string) error {
			return st.SetSetting(ctx, cursorKey, cursor)
		},
		OnUser: func(ctx context.Context, uid int64) error {
			_, _, err := st.TouchUser(ctx, store.UserInfo{ID: uid, FirstName: platform + " owner"})
			return err
		}}
	fmt.Printf("%s is online; messages and tool calls are enabled for configured owners\n", platform)
	err = runner.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
