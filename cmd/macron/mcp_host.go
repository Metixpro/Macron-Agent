package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

var slugRe = regexp.MustCompile(`[^a-z0-9_-]+`)

func pluginView(i plugins.Info) tui.Plugin {
	target := i.Command
	if i.URL != "" {
		target = i.URL
	}
	p := tui.Plugin{Key: i.Key, Name: i.Name, Kind: i.Kind, Source: i.Source, Target: target, Enabled: i.Enabled, Status: i.Status, Error: i.Error,
		Removable: i.Source != plugins.SourceFile}
	if p.Name == "" {
		p.Name = strings.TrimPrefix(i.Key, "@")
	}
	if i.Kind == plugins.KindScript {
		p.Status = "ready"
		if !i.Enabled {
			p.Status = "off"
		}
	}
	for _, t := range i.Tools {
		p.Tools = append(p.Tools, t.Name)
	}
	return p
}

func (t *terminal) Plugins() []tui.Plugin {
	if t.pm == nil {
		return nil
	}
	var out []tui.Plugin
	for _, i := range t.pm.List() {
		out = append(out, pluginView(i))
	}
	return out
}

func (t *terminal) findPlugin(name string) (string, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, i := range t.pm.List() {
		if strings.ToLower(i.Key) == name || strings.ToLower(strings.TrimPrefix(i.Key, "@")) == name || strings.ToLower(i.Name) == name {
			return i.Key, nil
		}
	}
	return "", errors.New("no MCP server or plugin is called " + name)
}

func (t *terminal) AddMCP(name, target string, args []string, env map[string]string) (tui.Plugin, error) {
	if t.pm == nil {
		return tui.Plugin{}, errors.New("plugins are not available here")
	}
	id := strings.Trim(slugRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-"), "-")
	if len(id) > 40 {
		id = id[:40]
	}
	if id == "" {
		return tui.Plugin{}, errors.New("give the server a short name, like github")
	}
	cfg := plugins.ServerConfig{ID: id, Name: strings.TrimSpace(name)}
	if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
		cfg.URL, cfg.Headers = target, env
	} else {
		cfg.Command, cfg.Args, cfg.Env = target, args, env
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	keys, err := t.pm.AddServers(ctx, []plugins.ServerConfig{cfg})
	if err != nil {
		return tui.Plugin{}, err
	}
	return pluginView(t.pm.Wait(keys[0], 25*time.Second)), nil
}

func (t *terminal) SetPluginEnabled(name string, on bool) (tui.Plugin, error) {
	key, err := t.findPlugin(name)
	if err != nil {
		return tui.Plugin{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := t.pm.SetEnabled(ctx, key, on); err != nil {
		return tui.Plugin{}, err
	}
	return pluginView(t.pm.Wait(key, 25*time.Second)), nil
}

func (t *terminal) RemovePlugin(name string) error {
	key, err := t.findPlugin(name)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return t.pm.Delete(ctx, key)
}
