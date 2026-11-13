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
func (m model) modeColor() lipgloss.TerminalColor {
	if m.modeName() == "Plan" {
		return colPlan
	}
	return colAccent
}
func (m model) composer() string {
	w := m.contentWidth()
	mode := lipgloss.NewStyle().Foreground(m.modeColor()).Background(colPanel).Bold(true).Render(m.modeName())
	model := orDefault(m.info.Model, "/connect model")
	effortWidth := 0
	if m.info.Effort != "" {
		effortWidth = ansi.StringWidth(m.info.Effort) + 2
	}
	meta := mode + stDim.Background(colPanel).Render(" · ") + stBold.Background(colPanel).Render(clip(model, max(8, w-18-effortWidth)))
	if m.info.Provider != "" && w > 64 {
		meta += stDim.Background(colPanel).Render("  " + clip(m.info.Provider, 20))
	}
	if m.info.Effort != "" {
		meta += stWarn.Background(colPanel).Render("  " + m.info.Effort)
	}
	body := terminalText(m.input.View()) + "\n\n" + clip(meta, w-5)
	return lipgloss.NewStyle().Border(lipgloss.Border{Left: "┃"}, false, false, false, true).BorderForeground(m.modeColor()).Background(colPanel).Foreground(colText).Padding(1, 2).Width(w - 1).Render(body)
}
func (m model) inputPanel() string {
	if m.ask != nil {
		return terminalText(m.askView())
	}
	if m.pick != nil {
		return terminalText(m.pickView())
	}
	if len(m.sugg) > 0 {
		return m.suggView() + "\n" + m.composer()
	}
	return m.composer()
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
func (m model) frame(content string) string {
	w := m.contentWidth()
	h := max(m.height, 1)
	lines := strings.Split(content, "\n")
	if len(lines) > h {
		lines = lines[len(lines)-h:]
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	margin := strings.Repeat(" ", max(0, (m.width-w)/2))
	for i, line := range lines {
		line = ansi.Truncate(line, w, "")
		lines[i] = margin + line + strings.Repeat(" ", max(0, w-ansi.StringWidth(line)))
	}
	return lipgloss.NewStyle().Background(colCanvas).Foreground(colText).Width(m.width).Render(strings.Join(lines, "\n"))
}
func (m model) homeView() string {
	w := m.contentWidth()
	panel := m.inputPanel()
	if m.pick != nil || m.ask != nil || m.height < 20 {
		gap := max(0, m.height-lipgloss.Height(panel)-2)
		return m.frame(panel + strings.Repeat("\n", gap) + "\n" + m.homeFooter(w))
	}
	hints := stDim.Render(clip("ctrl+p commands   tab mode   /connect", w))
	tip := stDim.Render(clip("• Ask anything, or connect your tools with /connect.", w))
	if m.notice != "" {
		tip = stWarn.Render(clip(m.notice, w))
	}
	tail := panel + "\n" + hints + "\n\n" + tip
	available := m.height - lipgloss.Height(tail) - 3
	var header string
	if available >= 9 && m.pick == nil && len(m.sugg) == 0 {
		title := lipgloss.PlaceHorizontal(w, lipgloss.Center, logo(w))
		tagline := "Your workspace. Your tools. Your agent."
		if w < 40 {
			tagline = "Your agent. Ready to work."
		}
		header = title + "\n\n" + lipgloss.PlaceHorizontal(w, lipgloss.Center, stDim.Render(tagline)) + "\n\n"
	}
	top := max(0, (m.height-lipgloss.Height(header+tail)-4)/3)
	body := strings.Repeat("\n", top) + header + tail
	gap := max(0, m.height-lipgloss.Height(body)-2)
	return m.frame(body + strings.Repeat("\n", gap) + "\n" + m.homeFooter(w))
}
func (m model) homeFooter(w int) string {
	right := "v" + m.info.Version
	left := tailClip(m.info.Dir, max(5, w-len(right)-3))
	return stFaint.Render(left + strings.Repeat(" ", max(1, w-ansi.StringWidth(left)-len(right))) + right)
}
func (m model) workspaceView() string {
	panel := m.inputPanel()
	status := m.activityView()
	footer := m.footer()
	bottom := panel + "\n" + status + "\n" + footer
	available := max(0, m.height-lipgloss.Height(bottom)-1)
	history := ""
	if available > 0 {
		history = m.historyView(available) + "\n"
	}
	return m.frame(history + bottom)
}
func (m model) activityView() string {
	w := m.contentWidth()
	label := "Ready"
	if m.modeName() == "Plan" {
		label = "Plan · read only"
	}
	if !m.follow {
		label = "History · ctrl+end to follow"
	}
	if m.running || m.busy != "" {
		label = m.spin.View() + " Thinking"
		if m.busy != "" {
			label = m.spin.View() + " " + m.busy
		}
		if len(m.tools) > 0 {
			t := m.tools[len(m.tools)-1]
			label = m.spin.View() + " " + ansi.Strip(toolTitle(t.name, t.args, w-14))
		}
		label += " · esc stop"
	}
	if queue := len(m.queue.list()); queue > 0 {
		label += fmt.Sprintf(" · %d queued", queue)
	}
	if m.notice != "" {
		label = m.notice
	}
	if m.info.Yolo {
		label = "YOLO · " + label
	} else if m.info.Approvals == "off" {
		label = "Approvals off · " + label
	}
	return lipgloss.NewStyle().Foreground(m.modeColor()).Render(clip("▰ "+label, w))
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
