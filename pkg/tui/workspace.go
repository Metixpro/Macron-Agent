package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Metixpro/macron-agent/pkg/agent"
)

type commandMsg struct{ text string }

const (
	placeholderIdle = "Ask anything… build, fix, connect, explore"
	placeholderBusy = "Type to add to the job: Macron reads it at its next step"
)

func (m model) contentWidth() int { return max(12, min(m.width-4, 110)) }

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
	bg := lipgloss.NewStyle().Background(colPanel)
	modeName, modeColor := m.modeName(), m.modeColor()
	if strings.HasPrefix(m.input.Value(), "!") {
		modeName, modeColor = "Shell", colWarn
	}
	mode := lipgloss.NewStyle().Foreground(modeColor).Background(colPanel).Bold(true).Render(modeName)
	model := orDefault(m.info.Model, "no model · /connect model")
	meta := mode + stDim.Background(colPanel).Render(" · ") + stBold.Background(colPanel).Render(clip(model, max(8, w-30)))
	if m.info.Provider != "" && w > 64 {
		meta += stDim.Background(colPanel).Render(" · " + clip(m.info.Provider, 20))
	}
	if m.info.Effort != "" {
		meta += stWarn.Background(colPanel).Render(" · " + m.info.Effort)
	}
	right := ""
	if n := len(m.queue.list()); n > 0 {
		right = stWarn.Background(colPanel).Render(fmt.Sprintf("%d queued", n))
	}
	if right != "" {
		gap := max(1, w-7-ansi.StringWidth(meta)-ansi.StringWidth(right))
		meta += bg.Render(strings.Repeat(" ", gap)) + right
	}
	body := keepBg(terminalText(m.input.View())+"\n\n"+clip(meta, w-5), colPanel)
	return lipgloss.NewStyle().Border(lipgloss.Border{Left: "┃"}, false, false, false, true).BorderForeground(modeColor).
		Background(colPanel).Foreground(colText).Padding(1, 2).Width(w - 1).Render(body)
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
	shades := []lipgloss.TerminalColor{colText, colText, colText, colAccent, colAccent}
	var lines []string
	for row := 0; row < 5; row++ {
		var parts []string
		for _, letter := range letters {
			parts = append(parts, letter[row])
		}
		lines = append(lines, lipgloss.NewStyle().Foreground(shades[row]).Bold(true).Render(strings.Join(parts, " ")))
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
	return strings.Join(lines, "\n")
}

func (m model) homeView() string {
	w := m.contentWidth()
	panel := m.inputPanel()
	if m.pick != nil || m.ask != nil || m.height < 20 {
		gap := max(0, m.height-lipgloss.Height(panel)-1)
		return m.frame(panel + strings.Repeat("\n", gap) + "\n" + m.statusBar(w))
	}
	hints := stDim.Render(clip(keyHint("ctrl+p", "commands")+"   "+keyHint("tab", "build / plan")+"   "+keyHint("/connect", "tools")+"   "+keyHint("/help", "keys"), w))
	tip := stDim.Render(clip("• "+m.homeTip(), w))
	if m.notice != "" {
		tip = stWarn.Render(clip(m.notice, w))
	}
	tail := panel + "\n" + hints + "\n\n" + tip
	available := m.height - lipgloss.Height(tail) - 3
	var header string
	if available >= 9 && len(m.sugg) == 0 {
		title := lipgloss.PlaceHorizontal(w, lipgloss.Center, logo(w))
		tagline := "Your workspace. Your tools. Your agent."
		if w < 40 {
			tagline = "Your agent. Ready to work."
		}
		header = title + "\n\n" + lipgloss.PlaceHorizontal(w, lipgloss.Center, stDim.Render(tagline)) + "\n\n"
	}
	top := max(0, (m.height-lipgloss.Height(header+tail)-3)/3)
	body := strings.Repeat("\n", top) + header + tail
	gap := max(0, m.height-lipgloss.Height(body)-1)
	return m.frame(body + strings.Repeat("\n", gap) + "\n" + m.statusBar(w))
}

func (m model) homeTip() string {
	switch {
	case m.info.Model == "":
		return "Connect a model first: /connect model"
	case m.info.Resumed > 0:
		return fmt.Sprintf("Picking up where you left off here: %d earlier messages are remembered · /new starts fresh", m.info.Resumed)
	case m.info.Services > 0 && m.info.Connections == 0:
		return "Ask anything, or connect Gmail, GitHub, Bale and more with /connect"
	}
	return fmt.Sprintf("%d of %d services connected · ask anything, or /connect more", m.info.Connections, m.info.Services)
}

func keyHint(key, what string) string {
	return stBold.Foreground(colText).Render(key) + stDim.Render(" "+what)
}

func (m model) workspaceView() string {
	w := m.contentWidth()
	var bottom []string
	if live := m.livePanel(w); live != "" {
		bottom = append(bottom, live)
	}
	bottom = append(bottom, m.inputPanel(), m.activityView(), m.statusBar(w))
	tail := strings.Join(bottom, "\n")
	available := max(0, m.height-lipgloss.Height(tail)-2)
	history := ""
	if available > 0 {
		history = m.historyView(available) + "\n\n"
	}
	return m.frame(history + tail)
}

func (m model) livePanel(w int) string {
	if !m.running {
		return ""
	}
	var lines []string
	if len(m.plan) > 0 {
		done := 0
		for _, it := range m.plan {
			if it.Status == agent.PlanDone {
				done++
			}
		}
		lines = append(lines, stBold.Render("Plan")+stDim.Render(fmt.Sprintf(" %d/%d", done, len(m.plan))))
		from, to := planWindow(m.plan, 5)
		for _, it := range m.plan[from:to] {
			text := clip(oneLine(it.Text), w-6)
			switch it.Status {
			case agent.PlanDone:
				lines = append(lines, stDim.Render("  ☒ "+text))
			case agent.PlanInProgress:
				lines = append(lines, stAccent.Bold(true).Render("  ◼ "+text))
			default:
				lines = append(lines, "  ☐ "+text)
			}
		}
		if more := len(m.plan) - (to - from); more > 0 {
			lines = append(lines, stFaint.Render(fmt.Sprintf("  … %d more", more)))
		}
	}
	shown := 0
	for _, s := range m.subs {
		if shown == 4 {
			lines = append(lines, stFaint.Render(fmt.Sprintf("  … %d more sub-agents", len(m.subs)-shown)))
			break
		}
		shown++
		mark, st := m.spin.View(), stAccent
		if s.done {
			mark, st = "✓", stOK
			if s.failed {
				mark, st = "✗", stFail
			}
		}
		step := ""
		if s.last != "" {
			step = stDim.Render(" · " + s.last)
		}
		head := st.Render(mark) + " " + stBold.Render(clip(oneLine(s.title), 30)) + stDim.Render(fmt.Sprintf(" · %s", plural(s.steps, "step")))
		lines = append(lines, clip(head+step, w))
	}
	for _, t := range m.tools {
		if t.progress == "" {
			continue
		}
		lines = append(lines, stDim.Render(clip("  "+elbow+"  "+oneLine(t.progress), w)))
	}
	if m.thought != "" && len(m.tools) == 0 && strings.TrimSpace(m.stream) == "" {
		lines = append(lines, stDim.Italic(true).Render(clip("∴ "+m.thought, w)))
	}
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}

func planWindow(items []agent.PlanItem, size int) (int, int) {
	if len(items) <= size {
		return 0, len(items)
	}
	at := 0
	for i, it := range items {
		if it.Status != agent.PlanDone {
			at = i
			break
		}
	}
	from := max(0, min(at-1, len(items)-size))
	return from, from + size
}

func (m model) activityView() string {
	w := m.contentWidth()
	label := stOK.Render("●") + stDim.Render(" Ready")
	if m.modeName() == "Plan" {
		label = lipgloss.NewStyle().Foreground(colPlan).Render("●") + stDim.Render(" Plan mode · reads and plans, changes nothing")
	}
	if !m.follow {
		label = stWarn.Render("↑") + stDim.Render(" Reading history · ctrl+end to follow")
	}
	hint := keyHint("ctrl+p", "commands")
	if m.running || m.busy != "" {
		what := "Thinking"
		switch {
		case m.busy != "":
			what = m.busy
		case len(m.tools) > 0:
			t := m.tools[len(m.tools)-1]
			what = ansi.Strip(toolTitle(t.name, t.args, w-30))
		case strings.TrimSpace(m.stream) != "":
			what = "Writing"
		}
		label = stAccent.Render(m.spin.View()) + " " + stBold.Render(clip(what, w-30)) + stDim.Render(" · "+since(time.Since(m.started)))
		hint = keyHint("esc", "stop")
		if m.running && m.info.Yolo {
			label = stWarn.Render("YOLO ") + label
		}
	}
	if m.notice != "" {
		label = stWarn.Render(clip(m.notice, w-20))
	}
	if !m.running && m.info.Yolo {
		label = stWarn.Render("● YOLO") + stDim.Render(" · commands run without asking · /yolo to stop")
	} else if !m.running && m.info.Approvals == "off" && m.notice == "" {
		label += stDim.Render(" · approvals off")
	}
	gap := max(1, w-ansi.StringWidth(label)-ansi.StringWidth(hint))
	return clip(label+strings.Repeat(" ", gap)+hint, w)
}

func (m model) statusBar(w int) string {
	left := orDefault(m.info.Dir, ".")
	if m.info.Branch != "" {
		left += " ⎇ " + m.info.Branch
	}
	var right []string
	if m.ctxWindow > 0 && m.ctxUsed > 0 {
		right = append(right, fmt.Sprintf("context %s/%s", short(m.ctxUsed), short(m.ctxWindow)))
	}
	if m.tokIn+m.tokOut > 0 {
		right = append(right, fmt.Sprintf("↑%s ↓%s", short(m.tokIn), short(m.tokOut)))
	}
	if m.toolCount > 0 && w >= 80 {
		right = append(right, plural(m.toolCount, "tool call"))
	}
	right = append(right, "v"+m.info.Version)
	r := strings.Join(right, " · ")
	if ansi.StringWidth(r) > w/2 {
		r = "v" + m.info.Version
	}
	left = tailClip(left, max(5, w-ansi.StringWidth(r)-3))
	return stFaint.Render(left + strings.Repeat(" ", max(1, w-ansi.StringWidth(left)-ansi.StringWidth(r))) + r)
}

func (m model) openCommands() (tea.Model, tea.Cmd) {
	names := make([]string, len(commands))
	about := map[string]string{}
	for i, c := range commands {
		names[i] = c.name
		about[c.name] = c.about
	}
	m.pick = &picker{title: "Commands", items: names, about: about, pick: func(name string) tea.Cmd {
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
	about := map[string]string{"model": "the AI provider and model Macron thinks with"}
	m.pick = &picker{title: "Connect", items: items, about: about, pick: func(service string) tea.Cmd {
		return func() tea.Msg { return commandMsg{text: "/connect " + service} }
	}}
	return m, nil
}

func (m model) reloadModel() error {
	if e, ok := m.e.(interface{ ReloadModel(context.Context) error }); ok {
		return e.ReloadModel(context.Background())
	}
	return nil
}
