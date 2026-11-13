package main

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/term"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/cli"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"github.com/Metixpro/macron-agent/pkg/tui"
)

const (
	projectNoteRunes = 8000
	terminalLog      = "terminal.log"
)

var projectNotes = []string{"MACRON.md", "AGENTS.md", "CLAUDE.md"}

var hiddenWork = []string{machine.SubDir + "/", machine.JobsDir + "/", ".tool-output/", ".run/", ".docindex/", ".widgets/", ".macron_page_watches/", ".macron_price_alerts/"}

func interactiveTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd())) && term.IsTerminal(int(os.Stdout.Fd())) && os.Getenv("TERM") != "dumb"
}

type terminal struct {
	mode    string
	modeMu  sync.RWMutex
	st      *store.Store
	prov    *providers.Manager
	app     *app.App
	bot     *Bot
	gate    *cmdgate.Gate
	mem     *memory.Manager
	window  func() int
	uid     int64
	session string
	dir     string
	shown   string
	resumed int
	tidy    sync.WaitGroup
}

func fail(format string, a ...any) int {
	fmt.Fprintf(os.Stderr, "macron: "+format+"\n", a...)
	return 1
}

func runTerminal() int {
	_ = config.LoadDefaultDotEnv()
	if err := os.MkdirAll(config.Home(), 0o700); err != nil {
		return fail("%v", err)
	}
	if f, err := os.OpenFile(config.HomePath(terminalLog), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
		defer f.Close()
		log.SetOutput(f)
	}
	dir, err := os.Getwd()
	if err != nil {
		return fail("%v", err)
	}
	if dir, err = filepath.EvalSymlinks(dir); err != nil {
		return fail("%v", err)
	}
	secretKey, err := config.SecretKey()
	if err != nil {
		return fail("%v", err)
	}
	loc, err := config.Location()
	if err != nil {
		return fail("%v", err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	st, err := store.Open(ctx, envOrDefault(config.EnvDB, config.HomePath(config.FileDB)), store.Options{Location: loc, SecretKey: secretKey})
	if err != nil {
		return fail("the database: %v", err)
	}
	defer st.Close()
	usage := func(provider string, u llm.Usage, failed bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.RecordStat(ctx, store.StatProvider, provider, failed, u.PromptTokens, u.CompletionTokens)
	}
	prov, err := providers.New(ctx, st, llm.NewHTTPClient(), usage)
	if err != nil {
		return fail("providers: %v", err)
	}
	cfg := &config.Config{OptionalAPIURL: true}
	fs := flag.NewFlagSet("macron", flag.ContinueOnError)
	cfg.Bind(fs)
	if _, err := cfg.Validate(fs); err != nil {
		return fail("%v", err)
	}
	seedProvider(ctx, st, cfg)
	if err := prov.Reload(ctx); err != nil {
		return fail("providers: %v", err)
	}
	window := contextWindow(st, prov, cfg.ContextWindow)
	br, err := brain.Open(config.Home(), agent.DefaultSoul)
	if err != nil {
		return fail("the home folder %s: %v", config.Home(), err)
	}
	a, err := app.NewWith(ctx, cfg, agent.ChannelCLI, prov, agent.Options{Location: loc, Soul: br.Soul, ContextWindow: window})
	if err != nil {
		return fail("%v", err)
	}
	a.Search.Treg.SetTokenSource(tregToken(st, cfg.TregToken))
	b := &Bot{app: a, st: st, prov: prov, loc: loc, jobCtx: ctx, started: time.Now(), personal: true, brain: br, envWindow: cfg.ContextWindow}

	t := &terminal{st: st, prov: prov, app: a, bot: b, window: window, dir: dir, shown: shortDir(dir)}
	sum := sha256.Sum256([]byte(dir))
	t.session = "term-" + hex.EncodeToString(sum[:6])
	t.uid = -int64(binary.BigEndian.Uint64(sum[8:16])>>2) - 1
	if _, err := a.Machine.Bind(t.session, dir); err != nil {
		return fail("%v", err)
	}
	defer a.Machine.StopShell(t.session)
	excludeWork(dir)

	t.gate = b.commandGate(dir)
	a.Registry.Gate, a.Registry.CheckpointDir = t.gate, config.HomePath("checkpoints")
	a.Agent.Teachings = b.teachings
	pm := &plugins.Manager{Dir: cfg.PluginsDir, MCPFile: cfg.MCPConfig, Machine: a.Machine, HTTP: pluginHTTP(cfg.UseEnvProxy),
		States: pluginStates{st}, Version: app.Version, IsAdmin: func(string) bool { return true }, Reserved: a.Registry.Builtin}
	pm.Reload(ctx)
	a.Registry.SetPlugins(pm)
	defer pm.Close()

	t.mem = memory.New(st, prov, func() int { return historyBudget(st, window()) })
	if mc, err := t.mem.Load(ctx, t.uid); err == nil {
		t.resumed = mc.Messages
	}
	services := []string{}
	for _, spec := range tools.ConnectionSpecs() {
		services = append(services, spec.ID)
	}
	err = tui.Run(t, tui.Options{Services: services, Connections: tools.ConnectionsStatus, Setup: func() *exec.Cmd {
		bin, _ := os.Executable()
		return exec.Command(bin, "setup", "--no-telegram", "--no-service")
	}, Telegram: func() *exec.Cmd {
		bin, err := os.Executable()
		if err != nil {
			bin = os.Args[0]
		}
		return exec.Command(bin, "telegram")
	}})
	done := make(chan struct{})
	go func() { t.tidy.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
	}
	if err != nil {
		return fail("%v", err)
	}
	return 0
}

func shortDir(dir string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if dir == home {
			return "~"
		}
		if rel, ok := strings.CutPrefix(dir, home+string(filepath.Separator)); ok {
			return "~/" + rel
		}
	}
	return dir
}

func gitBranch(dir string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func excludeWork(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-path", "info/exclude").Output()
	if err != nil {
		return
	}
	path := strings.TrimSpace(string(out))
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}
	have, _ := os.ReadFile(path)
	lines := map[string]bool{}
	for _, l := range strings.Split(string(have), "\n") {
		lines[strings.TrimSpace(l)] = true
	}
	var add []string
	for _, p := range hiddenWork {
		if !lines[p] {
			add = append(add, p)
		}
	}
	if len(add) == 0 {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	var text strings.Builder
	if len(have) > 0 && !strings.HasSuffix(string(have), "\n") {
		text.WriteString("\n")
	}
	if !lines["# macron"] {
		text.WriteString("# macron\n")
	}
	for _, p := range add {
		text.WriteString(p + "\n")
	}
	_, _ = f.WriteString(text.String())
}

func (t *terminal) Info() tui.Info {
	i := tui.Info{Version: app.Version, Dir: t.shown, Branch: gitBranch(t.dir), Approvals: t.bot.gateMode(), Resumed: t.resumed,
		Telegram: cli.TelegramSummary(), Mode: t.Mode(), ContextWindow: t.window(), ToolCount: len(t.app.Registry.GetTools())}
	for _, spec := range tools.ConnectionSpecs() {
		i.Services++
		if tools.ConnectionReady(spec) {
			i.Connections++
		}
	}
	if p, ok := t.prov.Active(); ok {
		i.Model = p.Model
		i.Provider = p.Name
		i.Effort = p.Effort
	}
	i.Yolo, _ = t.gate.Yolo()
	return i
}

func (t *terminal) about(ctx context.Context) string {
	var parts []string
	where := "Working directory: " + t.dir
	if b := gitBranch(t.dir); b != "" {
		where += " (git branch " + b + ")"
	}
	parts = append(parts, where+". Relative paths in your tools start here.")
	for _, name := range projectNotes {
		data, err := os.ReadFile(filepath.Join(t.dir, name))
		if err != nil || strings.TrimSpace(string(data)) == "" {
			continue
		}
		text := textutil.TruncateRunes(strings.TrimSpace(string(data)), projectNoteRunes, "\n… (cut: read the file for the rest)")
		parts = append(parts, "Project instructions ("+name+"):\n"+text)
		break
	}
	if facts := t.bot.factsAbout(ctx, t.uid, false); facts != "" {
		parts = append(parts, facts)
	}
	return strings.Join(parts, "\n\n")
}

func (t *terminal) Turn(ctx context.Context, text string, l *agent.EventListener, inbox func() []string, yield func() <-chan struct{}) (*agent.Result, error) {
	t.tidy.Add(1)
	defer t.tidy.Done()
	if err := t.prov.Reload(ctx); err != nil {
		return nil, err
	}
	mc, err := t.mem.Load(ctx, t.uid)
	if err != nil {
		return nil, err
	}
	modernizeHistory(mc.History)
	window := t.window()
	carry, compacted := t.bot.contextCarry(ctx, t.uid, mc)
	files := newFileTracker(t.app.Machine, t.session)
	shownStart, shownEnd := l.OnToolStart, l.OnToolEnd
	l.OnToolStart = func(name, args string) {
		files.start(name)
		shownStart(name, args)
	}
	l.OnToolEnd = func(name, args, result string, took time.Duration) {
		files.end(name, result)
		go t.bot.recordStat(store.StatTool, name, failedResult(result), 0, 0)
		shownEnd(name, args, result, took)
	}
	shownText := l.OnText
	l.OnText = func(s string) { shownText(streamSafe(scrubFileNotes(s))) }
	var taken []string
	var takenMu sync.Mutex
	mode := t.Mode()
	about := t.about(ctx)
	var filter func(string) bool
	if mode == "plan" {
		filter = planToolAllowed
		about += "\nPlan mode: inspect with read-only tools and explain the plan. Do not execute shell commands, change files, configure services, or send messages."
	}
	res, err := t.app.Agent.Do(ctx, agent.Request{
		SessionID:        t.session,
		Input:            llm.Message{Role: "user", Content: text},
		History:          mc.History,
		Memory:           mc.Summary,
		Chat:             agent.IsSmallTalk(text),
		HistoryTokens:    historyBudget(t.st, window),
		ContextWindow:    window,
		ContextCarry:     carry,
		ContextCompacted: compacted,
		About:            about,
		ToolFilter:       filter,
		MaxSteps:         t.app.Config.MaxSteps,
		Listener:         l,
		ToolYield:        yield,
		Preload:          t.bot.sticky.get(t.uid),
		Inbox: func() []llm.Message {
			items := inbox()
			takenMu.Lock()
			taken = append(taken, items...)
			takenMu.Unlock()
			msgs := make([]llm.Message, len(items))
			for i, s := range items {
				msgs[i] = llm.Message{Role: "user", Content: s}
			}
			return msgs
		},
		Extensions:      t.bot.agentExtensions(),
		IdleTimeout:     t.bot.idleTimeout(),
		SkillNudgeEvery: learnNudgeEvery,
		Tools:           t.bot.factTools(nil),
		Owner:           true,
		OwnerNote:       t.bot.ownerNote(true, tierConf{}),
		FocusTools:      true,
		EfficientTools:  true,
	})
	if res != nil {
		t.bot.sticky.keep(t.uid, res.Loaded)
	}
	userTurn := text
	takenMu.Lock()
	if len(taken) > 0 {
		userTurn += "\n\n[messages the user sent while I was working on it]\n" + strings.Join(taken, "\n")
	}
	takenMu.Unlock()
	t.remember(userTurn, res, err, files.changed(), mc)
	return res, err
}

func (t *terminal) remember(userTurn string, res *agent.Result, runErr error, changed []string, mc *memory.Context) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var stored string
	switch {
	case runErr != nil || res == nil:
		reason := store.JobFailed
		if errors.Is(runErr, context.Canceled) {
			reason = store.JobStopped
		}
		stored = "[" + unfinishedLine(reason) + "]"
		if res != nil && res.WorkLog != "" {
			stored += "\n\n" + agent.WorkLogMark + "\n" + res.WorkLog
			if len(changed) > 0 {
				stored += "\nFiles changed: " + strings.Join(changed, ", ")
			}
		}
	default:
		stored = strings.TrimSpace(res.Answer)
		if res.Partial && res.WorkLog != "" {
			stored += "\n\n" + agent.WorkLogMark + "\n" + res.WorkLog
			if len(changed) > 0 {
				stored += "\nFiles changed: " + strings.Join(changed, ", ")
			}
		} else if note := workNote(res.Notes, changed); note != "" {
			stored += "\n\n" + note
		}
	}
	if err := t.mem.Append(ctx, t.uid, userTurn, stored); err != nil {
		log.Printf("terminal: saving the conversation: %v", err)
		return
	}
	if res != nil {
		t.bot.saveContextCarry(ctx, t.uid, res.Context, mc)
	}
	t.tidy.Add(1)
	go func() {
		defer t.tidy.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := t.mem.Tidy(ctx, t.uid); err != nil {
			log.Printf("terminal: compressing the conversation: %v", err)
		}
	}()
}

func (t *terminal) New(ctx context.Context) error {
	if err := t.mem.Reset(ctx, t.uid); err != nil {
		return err
	}
	_ = t.st.SetUserPref(ctx, t.uid, store.PrefContextMeter, "")
	t.gate.ForgetSession(t.session)
	t.resumed = 0
	return nil
}

func (t *terminal) Compact(ctx context.Context) (string, error) {
	before, err := t.mem.Load(ctx, t.uid)
	if err != nil {
		return "", err
	}
	if before.Messages == 0 {
		return "Nothing to compact yet", nil
	}
	done, err := t.mem.Compress(ctx, t.uid)
	if err != nil {
		return "", err
	}
	if !done {
		return "The conversation is short already", nil
	}
	after, err := t.mem.Load(ctx, t.uid)
	if err != nil {
		return "Compacted", nil
	}
	return fmt.Sprintf("Compacted: %s → %s tokens (a summary keeps what was done)", shortCount(before.Tokens), shortCount(after.Tokens)), nil
}

func shortCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprint(n)
}

func (t *terminal) Models(ctx context.Context) (string, []string, error) {
	p, ok := t.prov.Active()
	if !ok {
		return "", nil, errors.New("no provider is active")
	}
	list, err := t.prov.Models(ctx, p)
	return p.Model, list, err
}

func (t *terminal) SetModel(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	id := t.prov.ActiveID()
	if id == 0 || name == "" {
		return errors.New("no provider is active")
	}
	p, err := t.st.Provider(ctx, id)
	if err != nil {
		return err
	}
	p.Model = name
	normalized, err := providers.Normalize(*p)
	if err != nil {
		return err
	}
	if err := t.st.UpdateProvider(ctx, normalized); err != nil {
		return err
	}
	return t.prov.Reload(ctx)
}

func (t *terminal) SetApprovals(mode string) error {
	switch mode = strings.ToLower(strings.TrimSpace(mode)); mode {
	case cmdgate.ModeSmart, cmdgate.ModeManual, cmdgate.ModeOff:
	default:
		return fmt.Errorf("unknown mode %q: smart, manual or off", mode)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.st.SetSetting(ctx, settingGateMode, mode)
}

func (t *terminal) Yolo(on bool) bool {
	if on {
		t.gate.SetYolo(gateYoloFor)
	} else {
		t.gate.SetYolo(0)
	}
	y, _ := t.gate.Yolo()
	return y
}

func (t *terminal) Status(ctx context.Context) string {
	var b strings.Builder
	b.WriteString("Macron Agent " + app.Version)
	row := func(k, v string) { fmt.Fprintf(&b, "\n  %-10s %s", k, v) }
	if p, ok := t.prov.Active(); ok {
		row("model", p.Model+" · provider "+p.Name)
		row("API", providers.API(p)+" · "+p.BaseURL)
		row("stream", fmt.Sprint(!p.NoStream))
		row("vision", fmt.Sprint(p.Vision))
		effort := p.Effort
		if effort == "" {
			effort = "auto"
		}
		row("effort", effort)
		temperature := "model default"
		if p.Temperature != nil {
			temperature = fmt.Sprint(*p.Temperature)
		}
		row("temperature", temperature)
	}
	row("window", shortCount(t.window())+" tokens")
	if mc, err := t.mem.Load(ctx, t.uid); err == nil {
		conv := fmt.Sprintf("%d messages, %s tokens", mc.Messages, shortCount(mc.Tokens))
		if mc.Compressions > 0 {
			conv += fmt.Sprintf(" · summarized %d times", mc.Compressions)
		}
		row("history", conv)
	}
	dir := t.dir
	if br := gitBranch(t.dir); br != "" {
		dir += " (" + br + ")"
	}
	row("folder", dir)
	row("mode", t.Mode())
	for _, name := range projectNotes {
		if _, err := os.Stat(filepath.Join(t.dir, name)); err == nil {
			row("notes", name)
			break
		}
	}
	mode := t.bot.gateMode()
	if y, until := t.gate.Yolo(); y {
		mode = "yolo"
		if !until.IsZero() {
			mode += " until " + until.Format("15:04")
		}
	}
	row("approvals", mode)
	row("telegram", cli.TelegramSummary())
	row("home", config.Home())
	return b.String()
}

func (t *terminal) Mode() string {
	t.modeMu.RLock()
	defer t.modeMu.RUnlock()
	if t.mode == "plan" {
		return "plan"
	}
	return "build"
}
func (t *terminal) SetMode(mode string) error {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != "plan" && mode != "build" {
		return fmt.Errorf("mode must be build or plan")
	}
	t.modeMu.Lock()
	t.mode = mode
	t.modeMu.Unlock()
	return nil
}
func planToolAllowed(name string) bool {
	switch name {
	case "read_file", "list_files", "search_code", "find_files", "project_tree", "code_outline", "web_search", "web_fetch", "document_reader", "view_image", "search_past_chats", "recall_history", "update_plan":
		return true
	}
	return false
}
func (t *terminal) ReloadModel(ctx context.Context) error { return t.prov.Reload(ctx) }

func (t *terminal) Providers(ctx context.Context) ([]tui.ProviderChoice, error) {
	if err := t.prov.Reload(ctx); err != nil {
		return nil, err
	}
	var out []tui.ProviderChoice
	for _, p := range t.prov.Snapshot() {
		out = append(out, tui.ProviderChoice{ID: p.ID, Name: p.Name, Model: p.Model, Enabled: p.Enabled, Active: p.Active})
	}
	return out, nil
}
func (t *terminal) SetProvider(ctx context.Context, id int64) error {
	p, err := t.st.Provider(ctx, id)
	if err != nil {
		return err
	}
	normalized, err := providers.Normalize(*p)
	if err != nil {
		return err
	}
	p = &normalized
	if !p.Enabled {
		p.Enabled = true
		if err := t.st.UpdateProvider(ctx, *p); err != nil {
			return err
		}
	}
	if err := t.st.SetSetting(ctx, store.SettingActiveProvider, fmt.Sprint(id)); err != nil {
		return err
	}
	return t.prov.Reload(ctx)
}
