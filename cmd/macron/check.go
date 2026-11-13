package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/messenger"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func startupCheck(args []string) bool {
	for _, arg := range args {
		if arg == "--check" || arg == "-check" || arg == "--check=true" || arg == "--check-online" || arg == "-check-online" || arg == "--check-online=true" {
			return true
		}
	}
	return false
}
func runStartupCheck(args []string) error {
	if err := config.LoadDefaultDotEnv(); err != nil {
		return err
	}
	fs := flag.NewFlagSet("macron --check", flag.ContinueOnError)
	cfg := &config.Config{OptionalAPIURL: true}
	cfg.Bind(fs)
	fs.Bool("check", false, "validate local startup without contacting external services")
	online := fs.Bool("check-online", false, "also authenticate with the configured messenger")
	token := fs.String("token", os.Getenv(config.EnvBotToken), "Telegram bot token")
	owners := fs.String("owners", os.Getenv(config.EnvOwners), "Telegram owner IDs")
	db := fs.String("db", envOrDefault(config.EnvDB, config.HomePath(config.FileDB)), "SQLite database")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected check argument")
	}
	if _, err := cfg.Validate(fs); err != nil {
		return err
	}
	platform := strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_MESSENGER")))
	if platform == "" {
		platform = "cli"
		if *token != "" || *owners != "" {
			platform = "telegram"
		}
	}
	var probe func(context.Context) error
	switch platform {
	case "cli":
	case "telegram":
		ids, err := config.ParseIDs(*owners)
		if err != nil {
			return err
		}
		if *token == "" || len(ids) == 0 {
			return fmt.Errorf("configure the Telegram bot token and owner IDs first")
		}
		base := envOrDefault(config.EnvTelegramAPI, "https://api.telegram.org")
		if err := providers.ValidateBaseURL(base); err != nil {
			return fmt.Errorf("Telegram API: %w", err)
		}
		api := telegram.NewBotWithBase(*token, base)
		probe = func(ctx context.Context) error { _, err := api.GetMe(ctx); return err }
	case "bale", "rubika":
		upper := strings.ToUpper(platform)
		count := 0
		for _, id := range strings.Split(os.Getenv("MACRON_"+upper+"_OWNERS"), ",") {
			if strings.TrimSpace(id) != "" {
				count++
			}
		}
		if count == 0 || count > 64 {
			return fmt.Errorf("set MACRON_%s_OWNERS to between 1 and 64 owner IDs", upper)
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
		probe = client.Check
	default:
		return fmt.Errorf("unsupported inbound messenger %q", platform)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	secret, err := config.SecretKey()
	if err != nil {
		return err
	}
	loc, err := config.Location()
	if err != nil {
		return err
	}
	st, err := store.Open(ctx, *db, store.Options{SecretKey: secret, Location: loc})
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()
	if _, err := providers.New(ctx, st, llm.NewHTTPClient(), nil); err != nil {
		return fmt.Errorf("providers: %w", err)
	}
	if _, err := brain.Open(config.Home(), agent.DefaultSoul); err != nil {
		return fmt.Errorf("home: %w", err)
	}
	mode := "local"
	if *online {
		if probe == nil {
			return fmt.Errorf("no messenger configured for an online check")
		}
		if err := probe(ctx); err != nil {
			return err
		}
		mode = "online"
	}
	fmt.Printf("ok %s %s %s\n", app.Version, platform, mode)
	return nil
}
