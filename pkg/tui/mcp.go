package tui

import (
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type Plugin struct {
	Key, Name, Kind, Source, Target, Status, Error string
	Enabled, Removable                             bool
	Tools                                          []string
}

type PluginHost interface {
	Plugins() []Plugin
	AddMCP(name, target string, args []string, env map[string]string) (Plugin, error)
	SetPluginEnabled(name string, on bool) (Plugin, error)
	RemovePlugin(name string) error
}

var envArgRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*=`)

const mcpUsage = "/mcp add <name> <command and arguments> [KEY=value…] · /mcp add <name> https://…/mcp · /mcp on|off|remove|tools <name>"

func splitArgs(s string) []string {
	var out []string
	var cur strings.Builder
	var quote rune
	has := false
	for _, r := range s {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote, has = r, true
		case r == ' ' || r == '\t' || r == '\n':
			if has || cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
		}
	}
	if has || cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func pluginStatus(p Plugin) string {
	switch {
	case !p.Enabled:
		return "off"
	case p.Error != "":
		return "error: " + p.Error
	case p.Status == "connecting":
		return "connecting…"
	case p.Status == "ready":
		return plural(len(p.Tools), "tool")
	}
	return p.Status
}

func pluginKind(p Plugin) string {
	if p.Kind == "mcp" {
		return "MCP"
	}
	return "plugin"
}

func pluginNotice(p Plugin) tea.Msg {
	mark := stOK.Render(bullet)
	if p.Error != "" || (p.Enabled && p.Status != "ready") {
		mark = stWarn.Render(bullet)
	}
	return noticeMsg{text: mark + " " + stBold.Render(p.Name) + stDim.Render(" · "+pluginKind(p)+" · "+pluginStatus(p))}
}

func (m model) background(label string, fn func() tea.Msg) (tea.Model, tea.Cmd) {
	if m.running || m.busy != "" {
		return m, m.say("Wait until Macron finishes, or press esc")
	}
	m.busy, m.started = label, time.Now()
	return m, tea.Batch(fn, m.spin.Tick)
}

func (m model) mcpCommand(arg string) (tea.Model, tea.Cmd) {
	ph, ok := m.e.(PluginHost)
	if !ok {
		return m, m.say("MCP servers are not available here")
	}
	verb, rest, _ := strings.Cut(strings.TrimSpace(arg), " ")
	rest = strings.TrimSpace(rest)
	switch strings.ToLower(verb) {
	case "":
		list := ph.Plugins()
		if len(list) == 0 {
			m.print(stBold.Render("No MCP servers or plugins yet.") + "\n" + stDim.Render("Add one, for example:\n  /mcp add github npx -y @modelcontextprotocol/server-github GITHUB_PERSONAL_ACCESS_TOKEN=…\n  /mcp add docs https://example.com/mcp\nIts tools reach Macron here, in the desktop app and in Telegram."))
			return m, nil
		}
		items := make([]string, len(list))
		about := map[string]string{}
		for i, p := range list {
			items[i] = p.Name
			about[p.Name] = pluginKind(p) + " · " + pluginStatus(p)
		}
		m.pick = &picker{title: "MCP servers and plugins", items: items, about: about, pick: func(name string) tea.Cmd {
			return func() tea.Msg { return commandMsg{text: "/mcp menu " + name} }
		}}
		return m, nil
	case "menu":
		var p *Plugin
		for _, x := range ph.Plugins() {
			if x.Name == rest {
				x := x
				p = &x
			}
		}
		if p == nil {
			return m, m.say("It is gone")
		}
		toggle := "Turn off"
		if !p.Enabled {
			toggle = "Turn on"
		}
		items := []string{toggle, "Show its tools"}
		if p.Removable {
			items = append(items, "Remove")
		}
		about := map[string]string{toggle: pluginStatus(*p), "Show its tools": plural(len(p.Tools), "tool"), "Remove": "it stops and its settings are deleted"}
		name := p.Name
		m.pick = &picker{title: name + " · " + clip(p.Target, 50), items: items, about: about, pick: func(action string) tea.Cmd {
			cmd := map[string]string{"Turn off": "/mcp off ", "Turn on": "/mcp on ", "Show its tools": "/mcp tools ", "Remove": "/mcp remove "}[action]
			return func() tea.Msg { return commandMsg{text: cmd + name} }
		}}
		return m, nil
	case "add":
		fields := splitArgs(rest)
		if len(fields) < 2 {
			return m, m.say(mcpUsage)
		}
		name, target := fields[0], fields[1]
		var args []string
		env := map[string]string{}
		for _, f := range fields[2:] {
			if envArgRe.MatchString(f) {
				k, v, _ := strings.Cut(f, "=")
				env[k] = v
			} else {
				args = append(args, f)
			}
		}
		return m.background("Connecting "+name, func() tea.Msg {
			p, err := ph.AddMCP(name, target, args, env)
			if err != nil {
				return noticeMsg{err: err}
			}
			return pluginNotice(p)
		})
	case "on", "off":
		if rest == "" {
			return m, m.say(mcpUsage)
		}
		on := strings.EqualFold(verb, "on")
		return m.background(map[bool]string{true: "Starting ", false: "Stopping "}[on]+rest, func() tea.Msg {
			p, err := ph.SetPluginEnabled(rest, on)
			if err != nil {
				return noticeMsg{err: err}
			}
			return pluginNotice(p)
		})
	case "remove", "rm", "delete":
		if rest == "" {
			return m, m.say(mcpUsage)
		}
		if err := ph.RemovePlugin(rest); err != nil {
			return m, m.say(errText(err))
		}
		m.print(stOK.Render(bullet) + " Removed " + stBold.Render(rest))
		return m, nil
	case "tools":
		for _, p := range ph.Plugins() {
			if strings.EqualFold(p.Name, rest) || strings.EqualFold(strings.TrimPrefix(p.Key, "@"), rest) {
				if len(p.Tools) == 0 {
					m.print(stDim.Render(p.Name + " has no tools right now · " + pluginStatus(p)))
					return m, nil
				}
				lines := []string{stBold.Render(p.Name) + stDim.Render(" · "+plural(len(p.Tools), "tool"))}
				for _, t := range p.Tools {
					lines = append(lines, "  "+t)
				}
				m.print(strings.Join(lines, "\n"))
				return m, nil
			}
		}
		return m, m.say("No MCP server or plugin is called " + rest)
	}
	return m, m.say(mcpUsage)
}
