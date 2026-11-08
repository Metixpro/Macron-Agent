package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const toolPlugins = "plugins"

const pluginsToolHelp = "Connect, list, test and remove your own extra tools: MCP servers and script plugins. " +
	"The owner asks in the chat (\"connect the GitHub MCP server, here is the token\"); you do it, then say what you got. " +
	"Actions: list; add_mcp (id + command/args/env for a program, or url/headers/transport for a remote server); " +
	"add_script (filename .py/.sh/.js + code); install_zip (path of a plugin .zip on this machine); remove; enable; disable; " +
	"set_env (id, env_name, env_value: a secret a plugin asks for); test; install_deps; reload (after editing mcp_servers.json or the plugins folder by hand). " +
	"The new tools are in your tool list from the next step."

func (b *Bot) pluginsTool(u *store.User, m *telegram.Message) []agent.LocalTool {
	if b.plugins == nil || !b.isAdmin(u.ID) {
		return nil
	}
	props := map[string]any{
		"action":    map[string]any{"type": "string", "enum": []string{"list", "add_mcp", "add_script", "install_zip", "remove", "enable", "disable", "set_env", "test", "install_deps", "reload"}},
		"id":        str("The server or plugin id (lowercase letters, digits, - and _)"),
		"name":      str("A readable name (optional)"),
		"command":   str("add_mcp: the program to run (npx, uvx, python, a path…)"),
		"args":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "add_mcp: its arguments"},
		"env":       map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "add_mcp: environment variables for the program (tokens, keys)"},
		"url":       str("add_mcp: the address of a remote MCP server"),
		"headers":   map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "add_mcp: HTTP headers for a remote server (Authorization…)"},
		"transport": str("add_mcp: sse or http (empty: from the address)"),
		"filename":  str("add_script: the file name with its extension (.py, .sh, .js)"),
		"code":      str("add_script: the file's contents"),
		"path":      str("install_zip: where the .zip is on this machine"),
		"env_name":  str("set_env: the variable's name"),
		"env_value": str("set_env: its value"),
	}
	return []agent.LocalTool{{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolPlugins,
			Description: pluginsToolHelp,
			Parameters:  map[string]any{"type": "object", "properties": props, "required": []string{"action"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			out, err := b.pluginsAction(ctx, args)
			if err != nil {
				return agent.ToolOutput{Text: "error: " + err.Error()}, nil
			}
			return agent.ToolOutput{Text: out}, nil
		},
	}}
}

func str(desc string) map[string]any { return map[string]any{"type": "string", "description": desc} }

func argStr(args map[string]any, k string) string {
	s, _ := args[k].(string)
	return strings.TrimSpace(s)
}

func argStrs(args map[string]any, k string) []string {
	var out []string
	switch v := args[k].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	case string:
		out = strings.Fields(v)
	}
	return out
}

func argMap(args map[string]any, k string) map[string]string {
	out := map[string]string{}
	if v, ok := args[k].(map[string]any); ok {
		for name, x := range v {
			out[name] = fmt.Sprint(x)
		}
	}
	return out
}

func (b *Bot) pluginKey(id string) (string, error) {
	id = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(id), "@"))
	if id == "" {
		return "", errors.New("say which one (id)")
	}
	for _, key := range []string{"@" + id, id} {
		if _, ok := b.plugins.Get(key); ok {
			return key, nil
		}
	}
	return "", fmt.Errorf("nothing is connected as %q (list shows what is)", id)
}

func (b *Bot) pluginsAction(ctx context.Context, args map[string]any) (string, error) {
	pm := b.plugins
	switch act := argStr(args, "action"); act {
	case "list":
		return pluginList(pm.List()), nil
	case "reload":
		pm.Reload(ctx)
		return "Read the plugins folder and mcp_servers.json again.\n" + pluginList(pm.List()), nil
	case "add_mcp":
		c := plugins.ServerConfig{ID: argStr(args, "id"), Name: argStr(args, "name"), Command: argStr(args, "command"),
			Args: argStrs(args, "args"), Env: argMap(args, "env"), URL: argStr(args, "url"), Headers: argMap(args, "headers"),
			Transport: argStr(args, "transport")}
		if c.ID == "" {
			c.ID = slugOf(c.Name, c.Command, c.URL)
		}
		if err := c.Check(); err != nil {
			return "", err
		}
		if err := writeMCPServer(pm.MCPFile, c); err != nil {
			return "", err
		}
		pm.Reload(ctx)
		info := pm.Wait("@"+c.ID, 30*time.Second)
		return "Saved in " + pm.MCPFile + ".\n" + pluginLine(info), nil
	case "add_script":
		name, code := argStr(args, "filename"), argStr(args, "code")
		if name == "" || code == "" {
			return "", errors.New("add_script needs filename and code")
		}
		res, err := pm.InstallScript(ctx, filepath.Base(name), []byte(code))
		if err != nil {
			return "", err
		}
		return installedText(pm, res), nil
	case "install_zip":
		data, err := os.ReadFile(argStr(args, "path"))
		if err != nil {
			return "", err
		}
		res, err := pm.InstallZip(ctx, data)
		if err != nil {
			return "", err
		}
		return installedText(pm, res), nil
	}
	key, err := b.pluginKey(argStr(args, "id"))
	if err != nil {
		return "", err
	}
	info, _ := pm.Get(key)
	switch act := argStr(args, "action"); act {
	case "enable", "disable":
		if err := pm.SetEnabled(ctx, key, act == "enable"); err != nil {
			return "", err
		}
		i, _ := pm.Get(key)
		return pluginLine(i), nil
	case "set_env":
		name := argStr(args, "env_name")
		if name == "" {
			return "", errors.New("set_env needs env_name and env_value")
		}
		if err := pm.SetEnv(ctx, key, name, argStr(args, "env_value")); err != nil {
			return "", err
		}
		return "Saved " + name + " for " + info.ID + ".", nil
	case "install_deps":
		out, err := pm.InstallDeps(ctx, key)
		if err != nil {
			return "", err
		}
		return "Installed its packages.\n" + out, nil
	case "test":
		res, err := pm.Test(ctx, key)
		if err != nil {
			return "", err
		}
		var sb strings.Builder
		for _, r := range res {
			switch {
			case r.Skipped != "":
				fmt.Fprintf(&sb, "- %s: skipped (%s)\n", r.Tool, r.Skipped)
			case r.OK:
				fmt.Fprintf(&sb, "- %s: ok in %s %s\n", r.Tool, r.Took.Round(time.Millisecond), r.Output)
			default:
				fmt.Fprintf(&sb, "- %s: failed %s\n", r.Tool, r.Output)
			}
		}
		return strings.TrimSpace(sb.String()), nil
	case "remove":
		if info.Source == plugins.SourceFile {
			if err := removeMCPServer(pm.MCPFile, info.ID); err != nil {
				return "", err
			}
			pm.Reload(ctx)
			return "Removed " + info.ID + " from " + pm.MCPFile + ".", nil
		}
		if err := pm.Delete(ctx, key); err != nil {
			return "", err
		}
		return "Removed " + info.ID + ".", nil
	default:
		return "", fmt.Errorf("unknown action %q", act)
	}
}

func installedText(pm *plugins.Manager, res plugins.Installed) string {
	var sb strings.Builder
	sb.WriteString("Installed.\n")
	for _, k := range res.Keys {
		if i, ok := pm.Get(k); ok {
			sb.WriteString(pluginLine(i) + "\n")
		}
	}
	if res.Deps != "" {
		sb.WriteString("Its packages: " + res.Deps + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func pluginList(list []plugins.Info) string {
	if len(list) == 0 {
		return "Nothing is connected yet: no MCP servers and no plugins."
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Key < list[j].Key })
	var sb strings.Builder
	for _, i := range list {
		sb.WriteString(pluginLine(i) + "\n")
	}
	return strings.TrimSpace(sb.String())
}

func pluginLine(i plugins.Info) string {
	state := i.Status
	if !i.Enabled {
		state = "off"
	}
	if state == "" {
		state = "ready"
	}
	var names []string
	for _, t := range i.Tools {
		if !t.Conflict {
			names = append(names, t.Name)
		}
	}
	line := fmt.Sprintf("- %s (%s, %s): %s", i.ID, i.Kind, i.Source, state)
	if i.Error != "" {
		line += " — " + i.Error
	}
	if len(names) > 0 {
		line += "\n  tools: " + strings.Join(names, ", ")
	}
	var missing []string
	for _, n := range i.EnvNames {
		if !i.EnvSet[n] {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		line += "\n  still needs: " + strings.Join(missing, ", ") + " (set_env)"
	}
	return line
}

func slugOf(parts ...string) string {
	for _, p := range parts {
		var sb strings.Builder
		for _, r := range strings.ToLower(filepath.Base(strings.TrimSuffix(p, "/"))) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				sb.WriteRune(r)
			case r == '-' || r == '_' || r == '.' || r == ' ':
				if sb.Len() > 0 {
					sb.WriteByte('-')
				}
			}
		}
		if s := strings.Trim(sb.String(), "-"); s != "" {
			if len(s) > 40 {
				s = s[:40]
			}
			return s
		}
	}
	return "mcp"
}

func readMCPFile(path string) (top map[string]json.RawMessage, servers map[string]json.RawMessage, err error) {
	top = map[string]json.RawMessage{}
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, nil, err
	}
	if len(strings.TrimSpace(string(data))) > 0 {
		if err := json.Unmarshal(data, &top); err != nil {
			return nil, nil, fmt.Errorf("%s is not valid JSON: %w", path, err)
		}
	}
	if raw, ok := top["mcpServers"]; ok {
		servers = map[string]json.RawMessage{}
		if err := json.Unmarshal(raw, &servers); err != nil {
			return nil, nil, fmt.Errorf("%s: mcpServers: %w", path, err)
		}
		return top, servers, nil
	}
	if len(top) == 0 {
		return top, map[string]json.RawMessage{}, nil
	}
	return top, top, nil
}

func writeMCPFile(path string, top, servers map[string]json.RawMessage) error {
	if _, nested := top["mcpServers"]; nested || len(top) == 0 {
		raw, err := json.Marshal(servers)
		if err != nil {
			return err
		}
		top["mcpServers"] = raw
	}
	data, err := json.MarshalIndent(top, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func writeMCPServer(path string, c plugins.ServerConfig) error {
	top, servers, err := readMCPFile(path)
	if err != nil {
		return err
	}
	entry := map[string]any{}
	if c.Name != "" {
		entry["name"] = c.Name
	}
	if c.Command != "" {
		entry["command"] = c.Command
		if len(c.Args) > 0 {
			entry["args"] = c.Args
		}
		if len(c.Env) > 0 {
			entry["env"] = c.Env
		}
	}
	if c.URL != "" {
		entry["url"] = c.URL
		if len(c.Headers) > 0 {
			entry["headers"] = c.Headers
		}
		if c.Transport != "" {
			entry["transport"] = c.Transport
		}
	}
	raw, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	servers[c.ID] = raw
	return writeMCPFile(path, top, servers)
}

func removeMCPServer(path, id string) error {
	top, servers, err := readMCPFile(path)
	if err != nil {
		return err
	}
	found := false
	for k := range servers {
		if strings.EqualFold(k, id) || strings.EqualFold(slugOf(k), id) {
			delete(servers, k)
			found = true
		}
	}
	if !found {
		return fmt.Errorf("%q is not in %s", id, path)
	}
	return writeMCPFile(path, top, servers)
}
