package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type commandMsg struct{ text string }

func (m model) contentWidth() int { return max(12, min(m.width-4, 96)) }
func (m model) modeName() string {
	if strings.EqualFold(m.info.Mode, "plan") {
		return "Plan"
	}
	return "Build"
}
func (m model) composer() string {
	w := m.contentWidth()
	mode := stAccent.Bold(true).Render(m.modeName())
	if m.modeName() == "Plan" {
		mode = stOK.Bold(true).Render("Plan · read only")
	}
	meta := mode + stDim.Render("  ·  ") + stBold.Render(clip(orDefault(m.info.Model, "choose a model with /connect"), max(w-24, 8)))
	if m.info.Provider != "" && w > 62 {
		meta += stDim.Render("  " + clip(m.info.Provider, 18))
	}
	body := m.input.View() + "\n\n" + clip(meta, w-5)
	return lipgloss.NewStyle().Border(lipgloss.Border{Left: "┃"}).BorderLeft(true).BorderForeground(colAccent).Background(colPanel).Foreground(colText).Padding(1, 2).Width(w - 1).Render(body)
}
func (m model) inputPanel() string {
	switch {
	case m.ask != nil:
		return m.askView()
	case m.pick != nil:
		return m.pickView()
	default:
		out := m.composer()
		if len(m.sugg) > 0 {
			out += "\n" + m.suggView()
		}
		return out
	}
}
func logo(width int) string {
	if width < 38 {
		return stTitle.Render("m a c r o n")
	}
	letters := [][]string{
		{"█   █", "██ ██", "█ █ █", "█   █", "█   █"},
		{" ███ ", "█   █", "█████", "█   █", "█   █"},
		{" ████", "█    ", "█    ", "█    ", " ████"},
		{"████ ", "█   █", "████ ", "█ █  ", "█  ██"},
		{" ███ ", "█   █", "█   █", "█   █", " ███ "},
		{"█   █", "██  █", "█ █ █", "█  ██", "█   █"},
	}
	var lines []string
	for row := 0; row < 5; row++ {
		var parts []string
		for _, letter := range letters {
			parts = append(parts, letter[row])
		}
		line := strings.Join(parts, " ")
		style := stBold.Foreground(colText)
		if row >= 3 {
			style = stAccent
		}
		lines = append(lines, style.Render(line))
	}
	return strings.Join(lines, "\n")
}
func (m model) homeView() string {
	w := m.contentWidth()
	var pieces []string
	if m.height >= 24 && m.pick == nil {
		tagline := "Your workspace. Your tools. Your agent."
		if w < 40 {
			tagline = "Your agent. Ready to work."
		}
		pieces = append(pieces, lipgloss.PlaceHorizontal(w, lipgloss.Center, logo(w)), "", lipgloss.PlaceHorizontal(w, lipgloss.Center, stDim.Render(clip(tagline, w))), "")
	}
	if m.height >= 34 && m.pick == nil {
		pieces = append(pieces, "")
	}
	pieces = append(pieces, m.inputPanel())
	hints := "ctrl+p commands   tab mode   /connect services"
	if w < 54 {
		hints = "ctrl+p commands · tab mode"
	}
	pieces = append(pieces, stDim.Render(clip(hints, w)), "")
	if m.notice != "" {
		pieces = append(pieces, stWarn.Render(clip(m.notice, w)))
	}
	if m.pick == nil && m.ask == nil && m.height >= 24 {
		tip := "Connect Gmail, messengers and tools with /connect."
		if w < 50 {
			tip = "Connect services with /connect."
			if w < 34 {
				tip = "Services: /connect"
			}
		}
		if m.info.Resumed > 0 {
			tip = fmt.Sprintf("↺ %d earlier messages in this project · /new starts fresh", m.info.Resumed)
		}
		pieces = append(pieces, stAccent.Render("• ")+stDim.Render(clip(tip, w-2)))
		if m.info.Services > 0 {
			state := fmt.Sprintf("  %d of %d services configured · credentials stay private", m.info.Connections, m.info.Services)
			if w < 62 {
				state = fmt.Sprintf("  %d/%d configured · /connections", m.info.Connections, m.info.Services)
			}
			pieces = append(pieces, stFaint.Render(clip(state, w)))
		}
	}
	content := strings.Join(pieces, "\n")
	top := max(0, (m.height-lipgloss.Height(content)-4)/3)
	mid := strings.Repeat("\n", top) + content
	bottom := m.homeFooter(w)
	gap := max(0, m.height-lipgloss.Height(mid)-lipgloss.Height(bottom)-1)
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, mid+strings.Repeat("\n", gap)+"\n"+bottom)
}
func (m model) homeFooter(w int) string {
	right := "v" + m.info.Version
	left := tailClip(m.info.Dir, max(5, w-len(right)-3))
	return stFaint.Render(left + strings.Repeat(" ", max(1, w-ansi.StringWidth(left)-len(right))) + right)
}
func (m model) workspaceView() string {
	var pieces []string
	if live := strings.TrimSpace(m.liveView()); (m.running || m.busy != "") && live != "" {
		pieces = append(pieces, live)
	}
	for _, q := range m.queue.list() {
		pieces = append(pieces, stDim.Render(clip("⧗ "+oneLine(q), m.contentWidth())))
	}
	pieces = append(pieces, m.inputPanel(), m.footer())
	return "\n" + lipgloss.PlaceHorizontal(m.width, lipgloss.Center, strings.Join(pieces, "\n"))
}
func (m model) openCommands() (tea.Model, tea.Cmd) {
	var items []string
	for _, c := range commands {
		items = append(items, c.name+"  "+c.about)
	}
	m.pick = &picker{title: "Commands", items: items, pick: func(choice string) tea.Cmd {
		name, _, _ := strings.Cut(choice, " ")
		return func() tea.Msg { return commandMsg{text: name} }
	}}
	return m, nil
}
func (m model) toggleMode() (tea.Model, tea.Cmd) {
	if m.running || m.busy != "" {
		return m, m.say("Finish or stop the current request before changing mode")
	}
	engine, ok := m.e.(interface{ SetMode(string) error })
	if !ok {
		return m, nil
	}
	mode := "plan"
	if m.modeName() == "Plan" {
		mode = "build"
	}
	if err := engine.SetMode(mode); err != nil {
		return m, m.say(errText(err))
	}
	m.info = m.e.Info()
	return m, nil
}
func (m model) connectPicker() (tea.Model, tea.Cmd) {
	items := append([]string{"model"}, m.opt.Services...)
	m.pick = &picker{title: "Connect a service", items: items, pick: func(service string) tea.Cmd { return func() tea.Msg { return commandMsg{text: "/connect " + service} } }}
	return m, nil
}
func (m model) reloadModel() error {
	if e, ok := m.e.(interface{ ReloadModel(context.Context) error }); ok {
		return e.ReloadModel(context.Background())
	}
	return nil
}
