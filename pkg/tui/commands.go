package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

type command struct {
	name, args, about string
}

var commands = []command{
	{"/help", "", "commands and keys"},
	{"/new", "", "start a new conversation in this folder"},
	{"/clear", "", "clear the screen and start a new conversation"},
	{"/compact", "", "summarize the conversation to free context"},
	{"/model", "[name]", "show or switch the model"},
	{"/approvals", "[smart|manual|off]", "how risky commands are approved"},
	{"/yolo", "", "run commands without asking for 30 minutes (again: off)"},
	{"/status", "", "model, context, tokens, folder, Telegram"},
	{"/init", "", "write MACRON.md: what Macron should know about this project"},
	{"/telegram", "", "connect or manage the Telegram bot"},
	{"/exit", "", "quit"},
}

const initPrompt = `Study this project (its layout, how it is built, tested and run, its conventions) and write a concise MACRON.md at its root for future sessions: what the project is, the exact build, test and run commands, the code style, and what to be careful about. If MACRON.md or AGENTS.md already exists, improve it instead of starting over. Keep it short and factual.`

func (m *model) suggest() {
	v := m.input.Value()
	if !strings.HasPrefix(v, "/") || strings.ContainsAny(v, " \n") {
		m.sugg = nil
		return
	}
	var out []command
	for _, c := range commands {
		if strings.HasPrefix(c.name, v) {
			out = append(out, c)
		}
	}
	if len(out) == 1 && out[0].name == v {
		out = nil
	}
	m.sugg = out
	if m.suggAt >= len(out) {
		m.suggAt = 0
	}
}

func helpText() string {
	var b strings.Builder
	b.WriteString(stBold.Render("Commands"))
	for _, c := range commands {
		name := c.name
		if c.args != "" {
			name += " " + c.args
		}
		fmt.Fprintf(&b, "\n  %-30s %s", stAccent.Render(name), stDim.Render(c.about))
	}
	b.WriteString("\n\n" + stBold.Render("Keys"))
	for _, k := range [][2]string{
		{"enter", "send"},
		{"ctrl+j · alt+enter · \\ enter", "new line"},
		{"esc", "interrupt Macron, or clear the input"},
		{"↑ ↓", "earlier messages"},
		{"ctrl+c twice · ctrl+d", "exit"},
		{"ctrl+l", "clear the screen"},
	} {
		fmt.Fprintf(&b, "\n  %-30s %s", k[0], stDim.Render(k[1]))
	}
	b.WriteString("\n\n" + stDim.Render("Type while Macron works: the message is handed to it at its next step. Project notes in MACRON.md or AGENTS.md are read every turn."))
	return b.String()
}

func (m model) command(text string) (tea.Model, tea.Cmd) {
	name, arg, _ := strings.Cut(text, " ")
	name, arg = strings.ToLower(name), strings.TrimSpace(arg)
	idle := func() bool { return !m.running && m.busy == "" }
	work := func(label string, fn func() tea.Msg) (tea.Model, tea.Cmd) {
		if !idle() {
			return m, m.say("Wait until Macron finishes, or press esc")
		}
		m.busy, m.started = label, time.Now()
		return m, tea.Batch(fn, m.spin.Tick)
	}
	e := m.e
	switch name {
	case "/help", "/?":
		m.print(helpText())
	case "/exit", "/quit", "/q":
		if m.cancel != nil {
			m.cancel()
		}
		m.quitting = true
		return m, tea.Quit
	case "/new", "/clear", "/reset":
		if !idle() {
			return m, m.say("Wait until Macron finishes, or press esc")
		}
		if err := e.New(context.Background()); err != nil {
			m.print(stFail.Render(bullet) + " " + errText(err))
			return m, nil
		}
		m.tokIn, m.tokOut, m.ctxUsed, m.lastPlan = 0, 0, 0, ""
		m.info = e.Info()
		if name == "/clear" {
			m.print(banner(m.info, m.width))
			return m, tea.ClearScreen
		}
		m.print(stOK.Render(bullet) + " New conversation: earlier messages in this folder are forgotten")
	case "/compact":
		return work("Compacting", func() tea.Msg {
			s, err := e.Compact(context.Background())
			if err != nil {
				return noticeMsg{err: err}
			}
			return noticeMsg{text: stOK.Render(bullet) + " " + s}
		})
	case "/model":
		if arg != "" {
			return work("Switching", func() tea.Msg {
				if err := e.SetModel(context.Background(), arg); err != nil {
					return noticeMsg{err: err}
				}
				return noticeMsg{text: stOK.Render(bullet) + " Model: " + stBold.Render(arg)}
			})
		}
		return work("Loading models", func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cur, list, err := e.Models(ctx)
			return modelsMsg{current: cur, list: list, err: err}
		})
	case "/approvals":
		if arg == "" {
			modes := []string{"smart", "manual", "off"}
			m.pick = &picker{title: "Approvals", items: modes, current: orDefault(m.info.Approvals, "smart"), sel: max(indexOf(modes, m.info.Approvals), 0),
				pick: func(mode string) tea.Cmd {
					return func() tea.Msg {
						if err := e.SetApprovals(mode); err != nil {
							return noticeMsg{err: err}
						}
						return noticeMsg{text: stOK.Render(bullet) + " Approvals: " + stBold.Render(mode) + stDim.Render(" · "+approvalAbout(mode))}
					}
				}}
			m.print(stDim.Render("smart: a guardian model judges flagged commands and asks you when unsure · manual: you approve every flagged command · off: no approval prompts"))
			return m, nil
		}
		if err := e.SetApprovals(arg); err != nil {
			m.print(stFail.Render(bullet) + " " + errText(err))
			return m, nil
		}
		m.info = e.Info()
		m.print(stOK.Render(bullet) + " Approvals: " + stBold.Render(arg) + stDim.Render(" · "+approvalAbout(arg)))
	case "/yolo":
		on := !m.info.Yolo
		if arg == "off" {
			on = false
		} else if arg == "on" {
			on = true
		}
		on = e.Yolo(on)
		m.info = e.Info()
		if on {
			m.print(stWarn.Render(bullet) + " Yolo for 30 minutes: commands run without asking (the hard limits still hold) · /yolo again to stop")
		} else {
			m.print(stOK.Render(bullet) + " Yolo off: risky commands are checked again")
		}
	case "/status":
		m.print(e.Status(context.Background()) + m.usage())
	case "/init":
		if !idle() {
			return m, m.say("Wait until Macron finishes, or press esc")
		}
		return m, m.startTurn(initPrompt)
	case "/telegram":
		if !idle() {
			return m, m.say("Wait until Macron finishes, or press esc")
		}
		if m.opt.Telegram == nil {
			m.print(stDim.Render("Run «macron telegram» in a shell to connect the bot"))
			return m, nil
		}
		return m, tea.ExecProcess(m.opt.Telegram(), func(err error) tea.Msg { return execDoneMsg{err: err} })
	default:
		m.print(stFail.Render(bullet) + " Unknown command " + name + stDim.Render(" · /help lists them; start with // to send a message that begins with /"))
	}
	return m, nil
}

func (m model) usage() string {
	var parts []string
	if m.ctxWindow > 0 {
		parts = append(parts, fmt.Sprintf("context %s of %s tokens", short(m.ctxUsed), short(m.ctxWindow)))
	}
	if m.tokIn+m.tokOut > 0 {
		parts = append(parts, fmt.Sprintf("this session ↑%s ↓%s tokens", short(m.tokIn), short(m.tokOut)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n  " + stDim.Render(strings.Join(parts, " · "))
}

func approvalAbout(mode string) string {
	switch mode {
	case "manual":
		return "you approve every flagged command"
	case "off":
		return "flagged commands run without asking; the hard limits still hold"
	}
	return "a guardian model judges flagged commands and asks you when unsure"
}
