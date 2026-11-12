package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
)

type Info struct {
	Version   string
	Model     string
	Dir       string
	Branch    string
	Approvals string
	Yolo      bool
	Telegram  string
	Resumed   int
}

type Engine interface {
	Info() Info
	Turn(ctx context.Context, text string, l *agent.EventListener, inbox func() []string, yield func() <-chan struct{}) (*agent.Result, error)
	New(ctx context.Context) error
	Compact(ctx context.Context) (string, error)
	Models(ctx context.Context) (current string, list []string, err error)
	SetModel(ctx context.Context, name string) error
	SetApprovals(mode string) error
	Yolo(on bool) bool
	Status(ctx context.Context) string
}

type Options struct {
	Telegram func() *exec.Cmd
}

type (
	textMsg      struct{ s string }
	toolStartMsg struct{ name, args string }
	toolEndMsg   struct{ name, args, result string }
	progressMsg  struct{ name, text string }
	planMsg      struct{ items []agent.PlanItem }
	ctxMsg       struct{ used, window int }
	compactMsg   struct {
		before, after int
		summarized    bool
	}
	escalateMsg struct{ why string }
	subMsg      struct {
		id                  int
		title, step         string
		start, done, failed bool
	}
	askMsg struct {
		q     cmdgate.Question
		reply chan cmdgate.Choice
	}
	takenMsg struct{ items []string }
	doneMsg  struct {
		res  *agent.Result
		err  error
		took time.Duration
	}
	noticeMsg struct {
		text string
		err  error
	}
	modelsMsg struct {
		current string
		list    []string
		err     error
	}
	execDoneMsg struct{ err error }
	clearNotice struct{ at time.Time }
)

type liveTool struct {
	name, args string
	at         time.Time
	progress   string
}

type liveSub struct {
	id          int
	title, last string
	steps       int
	done        bool
}

type askState struct {
	q     cmdgate.Question
	reply chan cmdgate.Choice
	sel   int
}

type picker struct {
	title   string
	items   []string
	current string
	filter  string
	sel     int
	pick    func(string) tea.Cmd
}

type queue struct {
	mu    sync.Mutex
	items []string
	yield chan struct{}
}

func (q *queue) add(s string) {
	q.mu.Lock()
	q.items = append(q.items, s)
	if q.yield != nil {
		close(q.yield)
		q.yield = nil
	}
	q.mu.Unlock()
}

func (q *queue) signal() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.yield == nil {
		q.yield = make(chan struct{})
	}
	return q.yield
}

func (q *queue) take() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := q.items
	q.items = nil
	return out
}

func (q *queue) list() []string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]string(nil), q.items...)
}

type printer struct {
	mu   sync.Mutex
	q    []string
	wake chan struct{}
	p    *tea.Program
}

func (pr *printer) push(s string) {
	pr.mu.Lock()
	pr.q = append(pr.q, s)
	pr.mu.Unlock()
	select {
	case pr.wake <- struct{}{}:
	default:
	}
}

func (pr *printer) loop(done <-chan struct{}) {
	for {
		for {
			pr.mu.Lock()
			if len(pr.q) == 0 {
				pr.mu.Unlock()
				break
			}
			s := pr.q[0]
			pr.mu.Unlock()
			pr.p.Send(tea.Println(s)())
			select {
			case <-done:
				return
			default:
			}
			pr.mu.Lock()
			pr.q = pr.q[1:]
			pr.mu.Unlock()
		}
		select {
		case <-pr.wake:
		case <-done:
			return
		}
	}
}

func (pr *printer) rest() []string {
	pr.mu.Lock()
	defer pr.mu.Unlock()
	out := pr.q
	pr.q = nil
	return out
}

type bridge struct {
	p   *tea.Program
	out *printer
}

func (b *bridge) send(msg tea.Msg) {
	if b.p != nil {
		b.p.Send(msg)
	}
}

type model struct {
	e    Engine
	opt  Options
	br   *bridge
	info Info
	md   *markdown

	width, height int

	input  textarea.Model
	spin   spinner.Model
	hist   []string
	histAt int
	draft  string

	running     bool
	busy        string
	cancel      context.CancelFunc
	started     time.Time
	stream      string
	streamView  string
	dirty       bool
	tools       []*liveTool
	subs        []*liveSub
	lastPlan    string
	queue       *queue
	ctxUsed     int
	ctxWindow   int
	tokIn       int
	tokOut      int
	ask         *askState
	pick        *picker
	sugg        []command
	suggAt      int
	quitAt      time.Time
	notice      string
	noticeAt    time.Time
	interrupted bool
	shown       bool
	quitting    bool
}

func Run(e Engine, opt Options) error {
	md := &markdown{style: markdownStyle()}
	ta := textarea.New()
	ta.Placeholder = "Ask Macron to do anything · / for commands"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.MaxHeight = 0
	ta.SetHeight(1)
	ta.SetPromptFunc(2, func(i int) string {
		if i == 0 {
			return stAccent.Render("❯ ")
		}
		return "  "
	})
	ta.FocusedStyle.CursorLine = lipgloss.NewStyle()
	ta.FocusedStyle.Base = lipgloss.NewStyle()
	ta.FocusedStyle.Placeholder = stFaint
	ta.BlurredStyle = ta.FocusedStyle
	ta.KeyMap.InsertNewline.SetKeys("ctrl+j", "alt+enter")
	ta.Focus()

	sp := spinner.New(spinner.WithSpinner(spinner.Spinner{Frames: []string{"✢", "✳", "✶", "✻", "✽", "✻", "✶", "✳"}, FPS: time.Second / 8}), spinner.WithStyle(stAccent))

	br := &bridge{out: &printer{wake: make(chan struct{}, 1)}}
	m := model{e: e, opt: opt, br: br, info: e.Info(), md: md, input: ta, spin: sp, queue: &queue{}, histAt: -1}
	p := tea.NewProgram(m)
	br.p, br.out.p = p, p
	done := make(chan struct{})
	go br.out.loop(done)
	_, err := p.Run()
	close(done)
	for _, s := range br.out.rest() {
		fmt.Fprintln(os.Stdout, s)
	}
	return err
}

func (m model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, tea.WindowSize())
}

func (m *model) print(s string) {
	if m.width > 0 {
		s = ansi.Wrap(s, m.width-1, "")
	}
	m.br.out.push("\n" + s)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		if !m.shown {
			m.shown = true
			m.br.out.push(banner(m.info, m.width))
		}
		m.input.SetWidth(max(m.width-4, 10))
		m.fitInput()
		m.dirty = m.stream != ""
		return m, nil
	case tea.KeyMsg:
		return m.key(msg)
	case spinner.TickMsg:
		if !m.running && m.busy == "" {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		if m.dirty {
			m.streamView, m.dirty = m.renderStream(), false
		}
		return m, cmd
	case textMsg:
		m.stream, m.dirty = cleanStream(msg.s), true
		return m, nil
	case toolStartMsg:
		if msg.name == agent.ToolPlan {
			return m, nil
		}
		m.flushStream()
		m.tools = append(m.tools, &liveTool{name: msg.name, args: msg.args, at: time.Now()})
		return m, nil
	case toolEndMsg:
		if msg.name == agent.ToolPlan {
			return m, nil
		}
		m.endTool(msg.name, msg.args)
		m.print(toolBlock(msg.name, msg.args, msg.result, m.width))
		return m, nil
	case progressMsg:
		for i := len(m.tools) - 1; i >= 0; i-- {
			if m.tools[i].name == msg.name {
				if last := lastLine(msg.text); last != "" {
					m.tools[i].progress = last
				}
				break
			}
		}
		return m, nil
	case planMsg:
		block := planBlock(msg.items, m.width)
		if len(msg.items) > 0 && block != m.lastPlan {
			m.flushStream()
			m.lastPlan = block
			m.print(block)
		}
		return m, nil
	case ctxMsg:
		m.ctxUsed, m.ctxWindow = msg.used, msg.window
		return m, nil
	case compactMsg:
		if msg.after >= msg.before {
			return m, nil
		}
		how := "trimmed"
		if msg.summarized {
			how = "summarized"
		}
		m.print(stDim.Render(fmt.Sprintf("✻ Context %s: %s → %s tokens", how, short(msg.before), short(msg.after))))
		return m, nil
	case escalateMsg:
		m.print(stDim.Render("↑ " + oneLine(msg.why)))
		return m, nil
	case subMsg:
		m.sub(msg)
		return m, nil
	case askMsg:
		m.flushStream()
		m.ask = &askState{q: msg.q, reply: msg.reply}
		return m, nil
	case takenMsg:
		for _, s := range msg.items {
			m.print(userBlock(s, m.width))
		}
		return m, nil
	case doneMsg:
		return m.done(msg)
	case noticeMsg:
		m.busy = ""
		switch {
		case msg.err != nil:
			m.print(stFail.Render(bullet) + " " + errText(msg.err))
		case msg.text != "":
			m.print(msg.text)
		}
		m.info = m.e.Info()
		return m, nil
	case modelsMsg:
		m.busy = ""
		if msg.err != nil {
			m.print(stFail.Render(bullet) + " " + errText(msg.err))
			return m, nil
		}
		if len(msg.list) == 0 {
			m.print(stDim.Render("The provider lists no models; use /model NAME"))
			return m, nil
		}
		e := m.e
		m.pick = &picker{title: "Model", items: msg.list, current: msg.current, sel: max(indexOf(msg.list, msg.current), 0),
			pick: func(name string) tea.Cmd {
				return func() tea.Msg {
					if err := e.SetModel(context.Background(), name); err != nil {
						return noticeMsg{err: err}
					}
					return noticeMsg{text: stOK.Render(bullet) + " Model: " + stBold.Render(name)}
				}
			}}
		return m, nil
	case execDoneMsg:
		m.info = m.e.Info()
		if msg.err != nil {
			m.print(stFail.Render(bullet) + " " + errText(msg.err))
		}
		if m.info.Telegram != "" {
			m.print(stDim.Render("Telegram: ") + m.info.Telegram)
		}
		return m, nil
	case clearNotice:
		if msg.at.Equal(m.noticeAt) {
			m.notice = ""
		}
		return m, nil
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m *model) flushStream() {
	text := strings.TrimSpace(m.stream)
	m.stream, m.streamView, m.dirty = "", "", false
	if text == "" {
		return
	}
	m.print(withBullet(m.md.render(text, m.width), "●"))
}

func (m *model) endTool(name, args string) {
	at := -1
	for i, t := range m.tools {
		if t.name == name && t.args == args {
			at = i
			break
		}
	}
	if at < 0 {
		for i, t := range m.tools {
			if t.name == name {
				at = i
				break
			}
		}
	}
	if at >= 0 {
		m.tools = append(m.tools[:at], m.tools[at+1:]...)
	}
}

func (m *model) sub(msg subMsg) {
	var s *liveSub
	for _, x := range m.subs {
		if x.id == msg.id {
			s = x
		}
	}
	if s == nil {
		s = &liveSub{id: msg.id, title: msg.title}
		m.subs = append(m.subs, s)
	}
	if msg.step != "" {
		s.steps++
		s.last = msg.step
	}
	if msg.done {
		s.done = true
	}
}

func (m *model) renderStream() string {
	text := strings.TrimSpace(m.stream)
	if text == "" {
		return ""
	}
	return withBullet(m.md.render(text, m.width), "●")
}

func (m model) key(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.ask != nil {
		return m.askKey(msg)
	}
	if m.pick != nil {
		return m.pickKey(msg)
	}
	switch msg.String() {
	case "ctrl+c":
		switch {
		case m.input.Value() != "":
			m.input.Reset()
			m.sugg = nil
			m.fitInput()
		case m.running:
			m.interrupt()
		case time.Since(m.quitAt) < 2*time.Second:
			m.quitting = true
			return m, tea.Quit
		default:
			m.quitAt = time.Now()
			return m, m.say("Press Ctrl+C again to exit")
		}
		return m, nil
	case "ctrl+d":
		if m.input.Value() == "" {
			m.quitting = true
			return m, tea.Quit
		}
	case "esc":
		switch {
		case len(m.sugg) > 0:
			m.sugg = nil
		case m.running:
			m.interrupt()
		case m.input.Value() != "":
			m.input.Reset()
			m.fitInput()
		}
		return m, nil
	case "ctrl+l":
		return m, tea.ClearScreen
	case "tab":
		if len(m.sugg) > 0 {
			m.input.SetValue(m.sugg[m.suggAt].name + " ")
			m.sugg = nil
			return m, nil
		}
		return m, nil
	case "up", "down":
		if len(m.sugg) > 0 {
			if msg.String() == "up" {
				m.suggAt = (m.suggAt + len(m.sugg) - 1) % len(m.sugg)
			} else {
				m.suggAt = (m.suggAt + 1) % len(m.sugg)
			}
			return m, nil
		}
		if !strings.Contains(m.input.Value(), "\n") {
			m.browse(msg.String() == "up")
			return m, nil
		}
	case "enter":
		if len(m.sugg) > 0 {
			c := m.sugg[m.suggAt]
			m.sugg = nil
			if c.args != "" && !strings.Contains(m.input.Value(), " ") {
				m.input.SetValue(c.name + " ")
				return m, nil
			}
			if !strings.Contains(m.input.Value(), " ") {
				m.input.SetValue(c.name)
			}
		}
		if v := m.input.Value(); strings.HasSuffix(v, "\\") {
			m.input.SetValue(strings.TrimSuffix(v, "\\") + "\n")
			m.fitInput()
			return m, nil
		}
		return m.submit()
	}
	m.input.SetHeight(min(m.input.Height()+1, 10))
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	m.suggest()
	m.fitInput()
	return m, cmd
}

func (m *model) say(text string) tea.Cmd {
	at := time.Now()
	m.notice, m.noticeAt = text, at
	return tea.Tick(2500*time.Millisecond, func(time.Time) tea.Msg { return clearNotice{at: at} })
}

func (m *model) interrupt() {
	if m.cancel != nil {
		m.cancel()
	}
	m.interrupted = true
	if m.ask != nil {
		m.ask.reply <- cmdgate.Deny
		m.ask = nil
	}
}

func (m *model) browse(up bool) {
	if len(m.hist) == 0 {
		return
	}
	if m.histAt < 0 {
		if !up {
			return
		}
		m.draft = m.input.Value()
		m.histAt = len(m.hist)
	}
	if up {
		m.histAt = max(m.histAt-1, 0)
	} else {
		m.histAt++
	}
	if m.histAt >= len(m.hist) {
		m.histAt = -1
		m.input.SetValue(m.draft)
	} else {
		m.input.SetValue(m.hist[m.histAt])
	}
	m.fitInput()
}

func (m *model) fitInput() {
	w := max(m.input.Width(), 10)
	rows := 0
	for _, line := range strings.Split(m.input.Value(), "\n") {
		rows += max(1, (ansi.StringWidth(line)+w-1)/w)
	}
	m.input.SetHeight(min(max(rows, 1), 10))
}

func (m model) submit() (tea.Model, tea.Cmd) {
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.Reset()
	m.fitInput()
	m.sugg = nil
	if len(m.hist) == 0 || m.hist[len(m.hist)-1] != text {
		m.hist = append(m.hist, text)
	}
	m.histAt = -1
	if strings.HasPrefix(text, "/") && !strings.HasPrefix(text, "//") {
		return m.command(text)
	}
	text = strings.TrimPrefix(text, "/")
	if m.running {
		m.queue.add(text)
		return m, nil
	}
	if m.busy != "" {
		return m, m.say("Wait a moment: " + strings.ToLower(m.busy))
	}
	return m, m.startTurn(text)
}

func (m *model) startTurn(text string) tea.Cmd {
	ctx, cancel := context.WithCancel(context.Background())
	m.ask = nil
	ctx = cmdgate.WithAsker(ctx, m.asker())
	m.running, m.cancel, m.started, m.interrupted = true, cancel, time.Now(), false
	m.stream, m.streamView, m.tools, m.subs = "", "", nil, nil
	m.print(userBlock(text, m.width))
	e, l, q, br := m.e, m.listener(), m.queue, m.br
	inbox := func() []string {
		items := q.take()
		if len(items) > 0 {
			br.send(takenMsg{items: items})
		}
		return items
	}
	run := func() (msg tea.Msg) {
		start := time.Now()
		defer func() {
			if r := recover(); r != nil {
				msg = doneMsg{err: fmt.Errorf("internal error: %v\n%s", r, debug.Stack()), took: time.Since(start)}
			}
			cancel()
		}()
		res, err := e.Turn(ctx, text, l, inbox, q.signal)
		return doneMsg{res: res, err: err, took: time.Since(start)}
	}
	return tea.Batch(run, m.spin.Tick)
}

func (m *model) asker() cmdgate.Asker {
	br := m.br
	return func(ctx context.Context, q cmdgate.Question) cmdgate.Choice {
		reply := make(chan cmdgate.Choice, 1)
		br.send(askMsg{q: q, reply: reply})
		select {
		case c := <-reply:
			return c
		case <-ctx.Done():
			return cmdgate.Deny
		}
	}
}

func (m *model) listener() *agent.EventListener {
	send := m.br.send
	var subs atomic.Int32
	l := &agent.EventListener{
		OnText:      func(s string) { send(textMsg{s: s}) },
		OnToolStart: func(name, args string) { send(toolStartMsg{name: name, args: args}) },
		OnToolEnd: func(name, args, result string, _ time.Duration) {
			send(toolEndMsg{name: name, args: args, result: result})
		},
		OnToolProgress: func(name, text string, _ bool) { send(progressMsg{name: name, text: text}) },
		OnPlan:         func(items []agent.PlanItem) { send(planMsg{items: append([]agent.PlanItem(nil), items...)}) },
		OnContext:      func(used, window int) { send(ctxMsg{used: used, window: window}) },
		OnCompact: func(before, after, _ int, summarized bool) {
			send(compactMsg{before: before, after: after, summarized: summarized})
		},
		OnEscalate: func(why string) { send(escalateMsg{why: why}) },
	}
	l.SubAgent = func(title, _ string) *agent.EventListener {
		id := int(subs.Add(1))
		send(subMsg{id: id, title: title, start: true})
		return &agent.EventListener{
			OnToolStart: func(name, args string) {
				if name != agent.ToolPlan {
					send(subMsg{id: id, step: ansi.Strip(toolTitle(name, args, 60))})
				}
			},
			OnDone: func(_ int, failed bool) { send(subMsg{id: id, done: true, failed: failed}) },
		}
	}
	return l
}

func (m model) done(msg doneMsg) (tea.Model, tea.Cmd) {
	m.running, m.cancel = false, nil
	if m.ask != nil {
		m.ask.reply <- cmdgate.Deny
		m.ask = nil
	}
	m.stream, m.streamView, m.dirty = "", "", false
	m.tools, m.subs = nil, nil
	res, err := msg.res, msg.err
	if res != nil {
		m.tokIn += res.Usage.PromptTokens
		m.tokOut += res.Usage.CompletionTokens
	}
	answer := ""
	if res != nil {
		answer = strings.TrimSpace(res.Answer)
	}
	switch {
	case m.interrupted || errors.Is(err, context.Canceled):
		m.print("  " + stDim.Render(elbow) + "  " + stWarn.Render("Interrupted") + stDim.Render(" · tell Macron what to do instead, or say «continue»"))
	case err != nil:
		if answer != "" {
			m.print(withBullet(m.md.render(answer, m.width), "●"))
		}
		m.print(stFail.Render(bullet) + " " + errText(err))
	case answer != "":
		m.print(withBullet(m.md.render(answer, m.width), "●"))
	}
	if msg.took >= 20*time.Second && !m.interrupted {
		m.print(stDim.Render("✻ Worked for " + since(msg.took)))
	}
	m.info = m.e.Info()
	left := m.queue.take()
	switch {
	case len(left) == 0:
	case m.interrupted:
		if m.input.Value() == "" {
			m.input.SetValue(strings.Join(left, "\n"))
			m.fitInput()
		}
	default:
		return m, m.startTurn(strings.Join(left, "\n\n"))
	}
	return m, nil
}

func (m model) askKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	choices := []cmdgate.Choice{cmdgate.Once, cmdgate.Session, cmdgate.Always, cmdgate.Deny}
	answer := func(c cmdgate.Choice) (tea.Model, tea.Cmd) {
		m.ask.reply <- c
		m.ask = nil
		if c == cmdgate.Deny {
			m.interrupt()
		}
		return m, nil
	}
	switch msg.String() {
	case "up", "k", "shift+tab":
		m.ask.sel = (m.ask.sel + len(choices) - 1) % len(choices)
	case "down", "j", "tab":
		m.ask.sel = (m.ask.sel + 1) % len(choices)
	case "enter":
		return answer(choices[m.ask.sel])
	case "1", "y":
		return answer(cmdgate.Once)
	case "2":
		return answer(cmdgate.Session)
	case "3":
		return answer(cmdgate.Always)
	case "4", "n", "esc":
		return answer(cmdgate.Deny)
	case "ctrl+c":
		m.interrupt()
	}
	return m, nil
}

func (m model) pickKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := m.pick
	items := p.visible()
	switch msg.String() {
	case "esc", "ctrl+c":
		m.pick = nil
	case "up":
		if len(items) > 0 {
			p.sel = (p.sel + len(items) - 1) % len(items)
		}
	case "down", "tab":
		if len(items) > 0 {
			p.sel = (p.sel + 1) % len(items)
		}
	case "enter":
		m.pick = nil
		if len(items) > 0 {
			m.busy = "Switching"
			return m, tea.Batch(p.pick(items[p.sel]), m.spin.Tick)
		}
	case "backspace":
		if r := []rune(p.filter); len(r) > 0 {
			p.filter, p.sel = string(r[:len(r)-1]), 0
		}
	default:
		if msg.Type == tea.KeyRunes && !msg.Alt {
			p.filter, p.sel = p.filter+string(msg.Runes), 0
		}
	}
	return m, nil
}

func (p *picker) visible() []string {
	if p.filter == "" {
		return p.items
	}
	f := strings.ToLower(p.filter)
	var out []string
	for _, it := range p.items {
		if strings.Contains(strings.ToLower(it), f) {
			out = append(out, it)
		}
	}
	return out
}

func (m model) View() string {
	if m.width == 0 || m.quitting {
		return ""
	}
	parts := []string{""}
	if m.running || m.busy != "" {
		if live := strings.TrimPrefix(m.liveView(), "\n"); live != "" {
			parts = append(parts, live)
		}
	}
	for _, q := range m.queue.list() {
		parts = append(parts, stDim.Render(clip("  ⧗ "+oneLine(q), m.width-1)))
	}
	switch {
	case m.ask != nil:
		parts = append(parts, m.askView())
	case m.pick != nil:
		parts = append(parts, m.pickView())
	default:
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colFaint).Padding(0, 1).Width(max(m.width-2, 10))
		parts = append(parts, box.Render(m.input.View()))
		if len(m.sugg) > 0 {
			parts = append(parts, m.suggView())
		}
	}
	parts = append(parts, m.footer())
	return strings.Join(parts, "\n")
}

func (m model) liveView() string {
	var lines []string
	if m.streamView != "" {
		view := strings.Split(m.streamView, "\n")
		limit := max(m.height-14, 4)
		if len(view) > limit {
			view = append([]string{stDim.Render("  …")}, view[len(view)-limit:]...)
		}
		lines = append(lines, "")
		lines = append(lines, view...)
	}
	blink := m.spin.View()
	for _, t := range m.tools {
		lines = append(lines, "", blink+" "+toolTitle(t.name, t.args, m.width-12)+stDim.Render(" "+since(time.Since(t.at))))
		if t.progress != "" {
			lines = append(lines, "  "+stDim.Render(elbow+"  "+clip(t.progress, m.width-8)))
		}
	}
	for _, s := range m.subs {
		if s.done {
			continue
		}
		line := fmt.Sprintf("  ↳ %s · %s", oneLine(s.title), plural(s.steps, "step"))
		if s.last != "" {
			line += " · " + s.last
		}
		lines = append(lines, stDim.Render(clip(line, m.width-1)))
	}
	verb := "Thinking"
	switch {
	case m.busy != "":
		verb = m.busy
	case len(m.tools) > 0:
		verb = "Working"
	case m.stream != "":
		verb = "Writing"
	}
	hint := " · esc to interrupt"
	if m.busy != "" {
		hint = ""
	}
	status := m.spin.View() + " " + stAccent.Render(verb+"…") + stDim.Render(" ("+since(time.Since(m.started))+hint+")")
	if m.busy != "" {
		status = m.spin.View() + " " + stAccent.Render(verb+"…")
	}
	lines = append(lines, "", status)
	return strings.Join(lines, "\n")
}

func (m model) askView() string {
	q := m.ask.q
	w := max(m.width-2, 20)
	var b strings.Builder
	b.WriteString(stWarn.Bold(true).Render("Run this command?") + "\n\n")
	for _, l := range strings.Split(strings.TrimSpace(clean(q.Command)), "\n") {
		b.WriteString("  " + clip(l, w-6) + "\n")
	}
	if len(q.Findings) > 0 {
		b.WriteString("\n")
		for _, f := range q.Findings {
			b.WriteString(stWarn.Render(clip("  ! "+oneLine(f.Reason), w-4)) + "\n")
		}
	}
	if g := strings.TrimSpace(q.Guardian); g != "" {
		b.WriteString(stDim.Render(clip("  guardian: "+oneLine(g), w-4)) + "\n")
	}
	b.WriteString("\n")
	opts := []string{"Yes", "Yes, and don't ask again for this command in this session", "Always allow this command", "No, tell Macron what to do instead (esc)"}
	for i, o := range opts {
		line := fmt.Sprintf("  %d. %s", i+1, o)
		if i == m.ask.sel {
			line = stAccent.Render(fmt.Sprintf("❯ %d. %s", i+1, o))
		}
		b.WriteString(clip(line, w-4))
		if i < len(opts)-1 {
			b.WriteString("\n")
		}
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colWarn).Padding(0, 1).Width(w).Render(b.String())
}

func (m model) pickView() string {
	p := m.pick
	w := max(m.width-2, 20)
	items := p.visible()
	var b strings.Builder
	b.WriteString(stBold.Render(p.title))
	if p.filter != "" {
		b.WriteString(stDim.Render("  filter: ") + p.filter)
	} else {
		b.WriteString(stDim.Render("  type to filter · enter to choose · esc to cancel"))
	}
	b.WriteString("\n")
	const shown = 10
	from := 0
	if p.sel >= shown {
		from = p.sel - shown + 1
	}
	for i := from; i < len(items) && i < from+shown; i++ {
		name := items[i]
		mark := "  "
		if name == p.current {
			mark = stOK.Render("✓ ")
		}
		line := mark + name
		if i == p.sel {
			line = stAccent.Render("❯ ") + stAccent.Render(name)
			if name == p.current {
				line += stOK.Render(" ✓")
			}
		} else {
			line = "  " + line
		}
		b.WriteString("\n" + clip(line, w-4))
	}
	if len(items) == 0 {
		b.WriteString("\n" + stDim.Render("  nothing matches"))
	} else if len(items) > shown {
		b.WriteString("\n" + stDim.Render(fmt.Sprintf("  %d of %d", p.sel+1, len(items))))
	}
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1).Width(w).Render(b.String())
}

func (m model) suggView() string {
	var lines []string
	for i, c := range m.sugg {
		name := fmt.Sprintf("%-12s", c.name)
		line := "  " + name + stDim.Render(c.about)
		if i == m.suggAt {
			line = stAccent.Render("❯ "+name) + stDim.Render(c.about)
		}
		lines = append(lines, clip(line, m.width-1))
	}
	return strings.Join(lines, "\n")
}

func (m model) footer() string {
	if m.notice != "" {
		return stDim.Render("  " + m.notice)
	}
	var left []string
	model := m.info.Model
	if model == "" {
		model = "no model"
	}
	left = append(left, model)
	if m.ctxWindow > 0 && m.ctxUsed*100 >= m.ctxWindow {
		left = append(left, fmt.Sprintf("%d%% context", min(100, m.ctxUsed*100/m.ctxWindow)))
	}
	if m.tokIn+m.tokOut > 0 {
		left = append(left, "↑"+short(m.tokIn)+" ↓"+short(m.tokOut))
	}
	appr := "approvals " + orDefault(m.info.Approvals, "smart")
	if m.info.Yolo {
		appr = stWarn.Render("yolo: commands run without asking")
	}
	left = append(left, appr)
	l := "  " + strings.Join(left, " · ")
	branch := ""
	if m.info.Branch != "" {
		branch = " (" + m.info.Branch + ")"
	}
	room := m.width - ansi.StringWidth(l) - ansi.StringWidth(branch) - 4
	if room < 12 {
		return stDim.Render(clip(l, m.width-1))
	}
	dir := tailClip(m.info.Dir, room) + branch
	space := m.width - ansi.StringWidth(l) - ansi.StringWidth(dir) - 2
	return stDim.Render(l + strings.Repeat(" ", space) + dir)
}

func banner(info Info, width int) string {
	w := min(max(width-2, 30), 70)
	var rows []string
	rows = append(rows, stTitle.Render("✻ Macron Agent")+" "+stDim.Render(info.Version), "")
	row := func(k, v string) { rows = append(rows, stDim.Render(fmt.Sprintf("%-10s", k))+v) }
	row("model", orDefault(info.Model, stWarn.Render("none")))
	dir := tailClip(info.Dir, w-14)
	if info.Branch != "" {
		dir += stDim.Render(" (" + info.Branch + ")")
	}
	row("folder", dir)
	if info.Telegram != "" {
		row("telegram", info.Telegram)
	}
	rows = append(rows, "", stDim.Render("/help for commands · esc interrupts · ctrl+c twice exits"))
	out := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(colAccent).Padding(0, 1).Width(w).Render(strings.Join(rows, "\n"))
	if info.Resumed > 0 {
		out += "\n" + stDim.Render(fmt.Sprintf("  ↺ continuing this folder's conversation (%s) · /new starts fresh", plural(info.Resumed, "earlier message")))
	}
	return out
}

func userBlock(text string, width int) string {
	lines := strings.Split(strings.TrimSpace(clean(text)), "\n")
	for i, l := range lines {
		l = wrap(l, max(width-4, 10))
		mark := "  "
		if i == 0 {
			mark = stAccent.Render("❯ ")
		}
		lines[i] = mark + strings.ReplaceAll(l, "\n", "\n  ")
	}
	return stUser.Render(strings.Join(lines, "\n"))
}

func cleanStream(s string) string {
	if i := strings.LastIndex(s, "<work_notes"); i >= 0 {
		s = s[:i]
	}
	return s
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(clean(s), "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			return t
		}
	}
	return ""
}

func errText(err error) string {
	s := strings.TrimSpace(err.Error())
	if r := []rune(s); len(r) > 600 {
		s = string(r[:600]) + "…"
	}
	return s
}

func since(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	if s < 3600 {
		return fmt.Sprintf("%dm %02ds", s/60, s%60)
	}
	return fmt.Sprintf("%dh %02dm", s/3600, s/60%60)
}

func short(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

func tailClip(s string, width int) string {
	if width < 4 || ansi.StringWidth(s) <= width {
		return s
	}
	r := []rune(s)
	for len(r) > 0 && ansi.StringWidth(string(r))+1 > width {
		r = r[1:]
	}
	return "…" + string(r)
}

func orDefault(s, d string) string {
	if strings.TrimSpace(s) == "" {
		return d
	}
	return s
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}
