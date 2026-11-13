package tui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type Tuning struct {
	Model, API               string
	Effort                   string
	EffortParam, EffortValue string
	EffortSupport            int
	Window, AutoWindow       int
	Temperature              *float64
}

const (
	EffortMaybe = iota
	EffortYes
	EffortNo
)

type Tuner interface {
	Tuning() Tuning
	SetEffort(level string) error
	SetContextWindow(tokens int) error
	SetTemperature(t *float64) error
}

var efforts = []string{"auto", "low", "medium", "high", "ultracode"}

var effortAbout = map[string]string{
	"auto":      "the model's own default; nothing extra is sent",
	"low":       "fast and cheap: chat and quick questions",
	"medium":    "balanced: most everyday work",
	"high":      "deep: analysis, code and multi-step work",
	"ultracode": "the model's maximum for big code and hard problems; jobs get twice the steps",
}

var windowSizes = []string{"auto", "32k", "64k", "128k", "200k", "256k", "400k", "1m", "2m"}

var temperatures = []string{"auto", "0", "0.2", "0.5", "0.7", "1"}

var temperatureAbout = map[string]string{
	"auto": "the provider's default (0.3; Claude and reasoning models use their own)",
	"0":    "the most exact and repeatable",
	"0.2":  "precise, a little variety",
	"0.5":  "balanced",
	"0.7":  "more varied wording",
	"1":    "creative and varied",
}

func label(level string) string {
	if level == "" {
		return "auto"
	}
	return level
}

func effortNote(t Tuning) string {
	switch {
	case t.EffortSupport == EffortNo:
		return "this model takes no effort setting and runs at its default"
	case t.EffortValue == "":
		return "the model's default"
	case t.EffortSupport == EffortMaybe:
		return fmt.Sprintf("sends %s = %s; if the model refuses it, Macron steps down by itself", t.EffortParam, t.EffortValue)
	}
	return fmt.Sprintf("sends %s = %s", t.EffortParam, t.EffortValue)
}

func parseWindow(s string) (int, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "auto" || s == "0" || s == "default" {
		return 0, true
	}
	s = strings.NewReplacer(",", "", "_", "", " ", "").Replace(s)
	mult := 1.0
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1000, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1000000, strings.TrimSuffix(s, "m")
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	n := int(f * mult)
	if n < 8000 || n > 10000000 {
		return 0, false
	}
	return n, true
}

func effectiveWindow(t Tuning) int {
	if t.Window > 0 {
		return t.Window
	}
	return t.AutoWindow
}

func windowLabel(t Tuning) string {
	if t.Window == 0 {
		return "auto"
	}
	for _, w := range windowSizes {
		if n, _ := parseWindow(w); n == t.Window {
			return w
		}
	}
	return short(t.Window)
}

func temperatureLabel(t *float64) string {
	if t == nil {
		return "auto"
	}
	return strconv.FormatFloat(*t, 'f', -1, 64)
}

func (m model) tuner() (Tuner, tea.Cmd) {
	tn, ok := m.e.(Tuner)
	if !ok || m.info.Model == "" {
		return nil, m.say("Connect a model first: /connect model")
	}
	return tn, nil
}

func (m model) effortCommand(arg string) (tea.Model, tea.Cmd) {
	tn, fail := m.tuner()
	if tn == nil {
		return m, fail
	}
	set := func(level string) tea.Cmd {
		return func() tea.Msg {
			if level == "auto" {
				level = ""
			}
			if err := tn.SetEffort(level); err != nil {
				return noticeMsg{err: err}
			}
			t := tn.Tuning()
			return noticeMsg{text: stOK.Render(bullet) + " Effort: " + stBold.Render(label(t.Effort)) + stDim.Render(" · "+effortNote(t))}
		}
	}
	if arg != "" {
		level := strings.ToLower(arg)
		if level == "default" || level == "none" {
			level = "auto"
		}
		if !slices.Contains(efforts, level) {
			return m, m.say("Effort is auto, low, medium, high or ultracode")
		}
		return m, set(level)
	}
	t := tn.Tuning()
	cur := label(t.Effort)
	m.pick = &picker{title: "Effort · how hard " + clip(t.Model, 40) + " thinks", items: efforts, about: effortAbout, current: cur, sel: max(indexOf(efforts, cur), 0), pick: set}
	if t.EffortSupport == EffortNo {
		m.print(stWarn.Render(bullet) + stDim.Render(" "+t.Model+" takes no effort setting: whatever you pick, it runs at its default"))
	}
	return m, nil
}

func (m model) contextCommand(arg string) (tea.Model, tea.Cmd) {
	tn, fail := m.tuner()
	if tn == nil {
		return m, fail
	}
	set := func(n int) tea.Cmd {
		return func() tea.Msg {
			if err := tn.SetContextWindow(n); err != nil {
				return noticeMsg{err: err}
			}
			t := tn.Tuning()
			how := "set by you"
			if t.Window == 0 {
				how = "worked out from the model's name"
			}
			return noticeMsg{text: stOK.Render(bullet) + " Context window: " + stBold.Render(short(effectiveWindow(t))+" tokens") + stDim.Render(" · "+how)}
		}
	}
	if arg != "" {
		n, ok := parseWindow(arg)
		if !ok {
			return m, m.say("Give the window in tokens, like 128k or 1m (8k to 10m), or auto")
		}
		return m, set(n)
	}
	t := tn.Tuning()
	about := map[string]string{"auto": "from the model's name: " + short(t.AutoWindow) + " tokens"}
	for _, w := range windowSizes[1:] {
		about[w] = "tokens"
	}
	cur := windowLabel(t)
	items := windowSizes
	if !slices.Contains(items, cur) {
		items = append(slices.Clone(windowSizes), cur)
		about[cur] = "tokens (your setting)"
	}
	m.pick = &picker{title: "Context window", items: items, about: about, current: cur, sel: max(indexOf(items, cur), 0), pick: func(w string) tea.Cmd {
		n, _ := parseWindow(w)
		return set(n)
	}}
	if m.ctxUsed > 0 {
		m.print(stDim.Render(fmt.Sprintf("This conversation uses %s of %s tokens. A larger window than the model really has makes requests fail; /compact frees room.", short(m.ctxUsed), short(m.ctxWindow))))
	}
	return m, nil
}

func (m model) temperatureCommand(arg string) (tea.Model, tea.Cmd) {
	tn, fail := m.tuner()
	if tn == nil {
		return m, fail
	}
	set := func(v string) tea.Cmd {
		return func() tea.Msg {
			var t *float64
			if v != "auto" {
				f, err := strconv.ParseFloat(v, 64)
				if err != nil {
					return noticeMsg{err: fmt.Errorf("temperature is a number like 0.2, or auto")}
				}
				t = &f
			}
			if err := tn.SetTemperature(t); err != nil {
				return noticeMsg{err: err}
			}
			return noticeMsg{text: stOK.Render(bullet) + " Temperature: " + stBold.Render(temperatureLabel(tn.Tuning().Temperature))}
		}
	}
	if arg != "" {
		v := strings.ToLower(arg)
		if v == "default" {
			v = "auto"
		}
		return m, set(v)
	}
	t := tn.Tuning()
	cur := temperatureLabel(t.Temperature)
	items := temperatures
	if !slices.Contains(items, cur) {
		items = append(slices.Clone(temperatures), cur)
	}
	m.pick = &picker{title: "Temperature", items: items, about: temperatureAbout, current: cur, sel: max(indexOf(items, cur), 0), pick: set}
	return m, nil
}

func (m model) settingsCommand() (tea.Model, tea.Cmd) {
	rows := []struct{ key, cmd, value string }{
		{"Model", "/model", orDefault(m.info.Model, "not connected")},
	}
	if tn, ok := m.e.(Tuner); ok && m.info.Model != "" {
		t := tn.Tuning()
		rows = append(rows,
			struct{ key, cmd, value string }{"Effort", "/effort", label(t.Effort) + " · " + effortNote(t)},
			struct{ key, cmd, value string }{"Context window", "/context", windowLabel(t) + " · " + short(effectiveWindow(t)) + " tokens"},
			struct{ key, cmd, value string }{"Temperature", "/temperature", temperatureLabel(t.Temperature)},
		)
	}
	rows = append(rows,
		struct{ key, cmd, value string }{"Provider", "/provider", orDefault(m.info.Provider, "none")},
		struct{ key, cmd, value string }{"Approvals", "/approvals", orDefault(m.info.Approvals, "smart")},
		struct{ key, cmd, value string }{"Theme", "/theme", orDefault(m.theme, "auto")},
		struct{ key, cmd, value string }{"Persian text", "/rtl", rtlModeName()},
		struct{ key, cmd, value string }{"MCP servers and plugins", "/mcp", ""},
	)
	items := make([]string, len(rows))
	about := map[string]string{}
	cmds := map[string]string{}
	for i, r := range rows {
		items[i] = r.key
		about[r.key] = r.value
		cmds[r.key] = r.cmd
	}
	m.pick = &picker{title: "Settings", items: items, about: about, pick: func(k string) tea.Cmd {
		return func() tea.Msg { return commandMsg{text: cmds[k]} }
	}}
	return m, nil
}

func rtlModeName() string {
	if rtlLogical {
		return "logical: the terminal orders right-to-left text"
	}
	return "visual: Macron orders right-to-left text"
}
