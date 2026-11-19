package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type executionController interface {
	ExecutionMode() string
	SetExecutionMode(mode string) (string, error)
}

type sandboxReporter interface {
	Sandboxed() bool
}

func (m model) executionCommand(arg string) (tea.Model, tea.Cmd) {
	if m.running || m.busy != "" {
		return m, m.say("Finish or stop the current request first")
	}
	ctrl, ok := m.e.(executionController)
	if !ok {
		return m, m.say("Execution mode switching is not available in this session")
	}

	arg = strings.ToLower(strings.TrimSpace(arg))
	current := orDefault(ctrl.ExecutionMode(), "best-effort")

	if arg == "" {
		modes := []string{"sandbox", "best-effort", "local"}
		about := map[string]string{
			"sandbox":     executionAbout("sandbox"),
			"best-effort": executionAbout("best-effort"),
			"local":       executionAbout("local"),
		}
		m.pick = &picker{
			title:   "Execution Mode",
			items:   modes,
			about:   about,
			current: current,
			sel:     max(indexOf(modes, current), 0),
			pick: func(mode string) tea.Cmd {
				return func() tea.Msg {
					note, err := ctrl.SetExecutionMode(mode)
					if err != nil {
						return noticeMsg{err: err}
					}
					return noticeMsg{text: executionStatusLine(m.e, mode, note)}
				}
			},
		}
		m.print(stDim.Render("sandbox: strict bwrap isolation (fail-closed) · best-effort: use sandbox when available · local: bypass sandbox"))
		return m, nil
	}

	if arg == "status" {
		m.print(executionStatusLine(m.e, current, ""))
		return m, nil
	}

	note, err := ctrl.SetExecutionMode(arg)
	if err != nil {
		m.print(stFail.Render(bullet) + " " + errText(err))
		return m, nil
	}
	m.info = m.e.Info()
	m.print(executionStatusLine(m.e, arg, note))
	return m, nil
}

func executionStatusLine(e Engine, mode, note string) string {
	style := stOK
	if mode == "local" {
		style = stWarn
	}
	line := style.Render(bullet) + " Execution mode: " + stBold.Render(mode)
	if sr, ok := e.(sandboxReporter); ok {
		if sr.Sandboxed() {
			line += stOK.Render(" (sandbox active)")
		} else {
			line += stDim.Render(" (unsandboxed)")
		}
	}
	if note != "" {
		line += stDim.Render(" · " + note)
	} else {
		line += stDim.Render(" · " + executionAbout(mode))
	}
	return line
}

func executionAbout(mode string) string {
	switch mode {
	case "sandbox":
		return "strict bubblewrap isolation; fails closed if unavailable"
	case "local":
		return "bypass sandbox and run commands directly on the host"
	default:
		return "use sandbox when available, otherwise run locally"
	}
}
