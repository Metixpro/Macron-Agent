package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/store"
)

// The plugin engine's links to the bot: where it keeps what a plugin is
// given (its switches and secrets, sealed), who a session is, and how it
// reaches an MCP server. Plugins, MCP servers and skills are added in the
// chat: the agent does it with the plugins tool (pluginstool.go).

// pluginStates keeps the panel's choices in the settings, sealed.
type pluginStates struct{ st *store.Store }

const (
	settingPluginPrefix = "plugin:"
	settingMCPServers   = "mcp_servers"
)

func (p pluginStates) PluginState(key string) plugins.State {
	var s plugins.State
	if v, err := p.st.SecretSetting(settingPluginPrefix + key); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &s)
	}
	return s
}

func (p pluginStates) SetPluginState(key string, s plugins.State) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if s.Enabled == nil && s.Access == "" && len(s.Env) == 0 {
		return p.st.DeleteSetting(ctx, settingPluginPrefix+key)
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return p.st.SetSecretSetting(ctx, settingPluginPrefix+key, string(data))
}

func (p pluginStates) PanelServers() []plugins.ServerConfig {
	var list []plugins.ServerConfig
	if v, err := p.st.SecretSetting(settingMCPServers); err == nil && v != "" {
		_ = json.Unmarshal([]byte(v), &list)
	}
	return list
}

func (p pluginStates) SetPanelServers(list []plugins.ServerConfig) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if len(list) == 0 {
		return p.st.DeleteSetting(ctx, settingMCPServers)
	}
	data, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return p.st.SetSecretSetting(ctx, settingMCPServers, string(data))
}

// sessionAdmin reports a session of one of the bot's admins.
func (b *Bot) sessionAdmin(sessionID string) bool {
	id, err := strconv.ParseInt(strings.TrimPrefix(sessionID, "tg-"), 10, 64)
	return err == nil && strings.HasPrefix(sessionID, "tg-") && b.isAdmin(id)
}

// pluginHTTP reaches the MCP servers that are given by address. They may run
// on this machine (127.0.0.1); a redirect may not leave the server's host, so
// the headers (a key) never follow one to another site.
func pluginHTTP(useProxy bool) *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	if useProxy {
		tr.Proxy = http.ProxyFromEnvironment
	}
	return &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
			return http.ErrUseLastResponse
		}
		return nil
	}}
}
