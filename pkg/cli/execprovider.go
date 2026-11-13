package cli

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
)

type selectedProvider struct {
	manager *providers.Manager
	id      int64
}

func (c selectedProvider) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	if c.id > 0 {
		ctx = providers.Only(ctx, c.id)
	}
	return c.manager.Complete(ctx, r)
}
func execProvider(ctx context.Context, dbPath, name string, value func(string) string) (llm.ChatClient, func(), int, error) {
	explicit := false
	for _, key := range []string{keyAPIURL, keyAPIKey, keyModel} {
		if _, ok := os.LookupEnv(key); ok {
			explicit = true
		}
	}
	if fileExists(store.DBPath(dbPath)) && (name != "" || !explicit) {
		secret, err := config.SecretKey()
		if err != nil {
			return nil, nil, 0, err
		}
		st, err := store.Open(ctx, dbPath, store.Options{SecretKey: secret})
		if err != nil {
			return nil, nil, 0, err
		}
		closeStore := func() { st.Close() }
		manager, err := providers.New(ctx, st, nil, nil)
		if err != nil {
			closeStore()
			return nil, nil, 0, err
		}
		list := manager.Snapshot()
		if len(list) > 0 {
			p, ok := manager.Active()
			id := int64(0)
			if name != "" {
				ok = false
				for _, entry := range list {
					if entry.Name == name || fmt.Sprint(entry.ID) == name {
						p = entry.Provider
						ok = p.Enabled
						id = p.ID
						break
					}
				}
			}
			if !ok {
				closeStore()
				return nil, nil, 0, fmt.Errorf("no enabled provider matches %q; use macron setup or enable it in the provider settings", name)
			}
			window := p.ContextWindow
			if window == 0 {
				window = agent.ContextWindow(p.Model)
			}
			return selectedProvider{manager: manager, id: id}, closeStore, window, nil
		}
		closeStore()
	}
	if name != "" {
		return nil, nil, 0, fmt.Errorf("provider %q was not found", name)
	}
	endpoint, api := llm.CleanBaseURL(value(keyAPIURL))
	if err := providers.ValidateBaseURL(endpoint); err != nil || strings.TrimSpace(value(keyModel)) == "" {
		return nil, nil, 0, fmt.Errorf("configure a provider with macron setup, or set MACRON_API_URL and MACRON_MODEL")
	}
	client := llm.NewClient(endpoint, value(keyAPIKey), value(keyModel))
	client.API = api
	return client, func() {}, agent.ContextWindow(value(keyModel)), nil
}
