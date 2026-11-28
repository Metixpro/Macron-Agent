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
	"runtime"
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
	"github.com/Metixpro/macron-agent/pkg/desk"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/moa"
	"github.com/Metixpro/macron-agent/pkg/plugins"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/router"
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

var hiddenWork = []string{machine.SubDir + "/", machine.JobsDir + "/", ".browser/", ".tool-output/", ".run/", ".docindex/", ".widgets/", ".macron-uploads/", ".macron_page_watches/", ".macron_price_alerts/"}

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
	pm      *plugins.Manager
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

type workspace struct {
	st     *store.Store
	prov   *providers.Manager
	app    *app.App
	bot    *Bot
	gate   *cmdgate.Gate
	mem    *memory.Manager
	window func() int
	pm     *plugins.Manager
	stop   context.CancelFunc
}

func setupWorkspace(gateDir string) (*workspace, error) {
	secretKey, err := config.SecretKey()
	if err != nil {
		return nil, err
	}
	loc, err := config.Location()
	if err != nil {
		return nil, err
	}
	ctx, stop := context.WithCancel(context.Background())
	w := &workspace{stop: stop}
	st, err := store.Open(ctx, envOrDefault(config.EnvDB, config.HomePath(config.FileDB)), store.Options{Location: loc, SecretKey: secretKey})
	if err != nil {
		stop()
		return nil, fmt.Errorf("the database: %w", err)
	}
	w.st = st
	usage := func(provider string, u llm.Usage, failed bool) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = st.RecordStat(ctx, store.StatProvider, provider, failed, u.PromptTokens, u.CompletionTokens)
	}
	prov, err := providers.New(ctx, st, llm.NewHTTPClient(), usage)
	if err != nil {
		w.close()
		return nil, fmt.Errorf("providers: %w", err)
	}
	w.prov = prov
	cfg := &config.Config{OptionalAPIURL: true}
	fs := flag.NewFlagSet("macron", flag.ContinueOnError)
	cfg.Bind(fs)
	applySavedExecutionMode(cfg, st.Setting)
	if _, err := cfg.Validate(fs); err != nil {
		w.close()
		return nil, err
	}
	seedProvider(ctx, st, cfg)
	if err := prov.Reload(ctx); err != nil {
		w.close()
		return nil, fmt.Errorf("providers: %w", err)
	}
	w.window = contextWindow(st, prov, cfg.ContextWindow)
	br, err := brain.Open(config.Home(), agent.DefaultSoul)
	if err != nil {
		w.close()
		return nil, fmt.Errorf("the home folder %s: %w", config.Home(), err)
	}
	a, err := app.NewWith(ctx, cfg, agent.ChannelCLI, prov, agent.Options{Location: loc, Soul: br.Soul, ContextWindow: w.window})
	if err != nil {
		w.close()
		return nil, err
	}
	w.app = a
	a.Search.Treg.SetTokenSource(tregToken(st, cfg.TregToken))
	w.bot = &Bot{app: a, st: st, prov: prov, loc: loc, jobCtx: ctx, started: time.Now(), personal: true, brain: br, envWindow: cfg.ContextWindow}
	w.gate = w.bot.commandGate(gateDir)
	a.Registry.Gate, a.Registry.CheckpointDir = w.gate, config.HomePath("checkpoints")
	a.Agent.Teachings = w.bot.teachings
	w.pm = &plugins.Manager{Dir: cfg.PluginsDir, MCPFile: cfg.MCPConfig, Machine: a.Machine, HTTP: pluginHTTP(cfg.UseEnvProxy),
		States: pluginStates{st}, Version: app.Version, IsAdmin: func(string) bool { return true }, Reserved: a.Registry.Builtin}
	w.pm.Reload(ctx)
	a.Registry.SetPlugins(w.pm)
	w.mem = memory.New(st, prov, func() int { return historyBudget(st, w.window()) })
	return w, nil
}

func (w *workspace) open(dir string) (*terminal, error) {
	t := &terminal{st: w.st, prov: w.prov, app: w.app, bot: w.bot, window: w.window, dir: dir, shown: shortDir(dir), gate: w.gate, mem: w.mem, pm: w.pm}
	sum := sha256.Sum256([]byte(dir))
	t.session = "term-" + hex.EncodeToString(sum[:6])
	t.uid = -int64(binary.BigEndian.Uint64(sum[8:16])>>2) - 1
	if _, err := w.app.Machine.Bind(t.session, dir); err != nil {
		return nil, err
	}
	excludeWork(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if mc, err := t.mem.Load(ctx, t.uid); err == nil {
		t.resumed = mc.Messages
	}
	return t, nil
}

func (t *terminal) currentUID() int64 {
	t.modeMu.RLock()
	uid := t.uid
	t.modeMu.RUnlock()
	return uid
}

func (t *terminal) SetThread(threadID string) {
	key := t.dir
	if tid := strings.TrimSpace(threadID); tid != "" && tid != "default" {
		key = t.dir + "::" + tid
	}
	sum := sha256.Sum256([]byte(key))
	t.modeMu.Lock()
	t.uid = -int64(binary.BigEndian.Uint64(sum[8:16])>>2) - 1
	t.modeMu.Unlock()
}

func (w *workspace) close() {
	if w.app != nil && w.app.Registry != nil {
		w.app.Registry.CloseAllBrowsers()
	}
	if w.pm != nil {
		w.pm.Close()
	}
	if w.st != nil {
		w.st.Close()
	}
	w.stop()
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
	w, err := setupWorkspace(dir)
	if err != nil {
		return fail("%v", err)
	}
	defer w.close()
	t, err := w.open(dir)
	if err != nil {
		return fail("%v", err)
	}
	defer w.app.Machine.StopShell(t.session)
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
	if runtime.GOOS == "windows" {
		return dir
	}
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
	out, err := machine.Hide(exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD")).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func excludeWork(dir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := machine.Hide(exec.CommandContext(ctx, "git", "-C", dir, "rev-parse", "--git-path", "info/exclude")).Output()
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
	if facts := t.bot.factsAbout(ctx, t.currentUID(), false); facts != "" {
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
	uid := t.currentUID()
	mc, err := t.mem.Load(ctx, uid)
	if err != nil {
		return nil, err
	}
	modernizeHistory(mc.History)
	window := t.window()
	carry, compacted := t.bot.contextCarry(ctx, uid, mc)
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

	runCtx := ctx
	turnText := text
	forceMoa := false
	if strings.HasPrefix(strings.TrimSpace(turnText), "/moa ") {
		turnText = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(turnText), "/moa "))
		forceMoa = true
	}

	// Smart Model Routing (shared with Telegram Bot settings)
	if !forceMoa && t.bot != nil {
		if rt := t.bot.chooseModel(uid, tierOwner, router.Input{Text: turnText}, false, nil); rt != nil {
			runCtx = rt.prefer(runCtx)
			window = rt.window(t.bot, t.bot.tier(tierOwner), window)
			if rt.strong && l != nil && l.OnThought != nil {
				l.OnThought(fmt.Sprintf("Smart Routing → strong model (%s)", rt.verdict.Kind))
			}
		}
	}

	// Multi-Model Voting (MoA): one-shot /moa or always-on preset
	if t.bot != nil {
		var preset *moa.Preset
		if forceMoa {
			defName, _ := t.st.Setting(settingMoaDefault)
			if p, ok := moa.Find(t.bot.moaPresets(), defName); ok && len(t.bot.moaProblems(p)) == 0 {
				if p.PrivacyFilter == "" {
					p.PrivacyFilter, _ = t.st.Setting(settingMoaPrivacy)
				}
				preset = &p
			}
		} else if actName, _ := t.st.Setting(settingMoaActive); actName != "" {
			if p, ok := moa.Find(t.bot.moaPresets(), actName); ok && len(t.bot.moaProblems(p)) == 0 {
				if p.PrivacyFilter == "" {
					p.PrivacyFilter, _ = t.st.Setting(settingMoaPrivacy)
				}
				preset = &p
			}
		}
		if preset != nil {
			mcClient := &moa.Client{
				Preset: *preset,
				Strict: t.bot.moaStrict(uid),
				OnAdvice: func(advice []moa.Advice) {
					if l == nil || l.OnThought == nil {
						return
					}
					var names []string
					for _, a := range advice {
						if a.Err == nil {
							names = append(names, a.Slot.Provider)
						}
					}
					l.OnThought(fmt.Sprintf("MoA (%s): gathered advice from %s → aggregating with %s", preset.Name, strings.Join(names, ", "), preset.Aggregator.Provider))
				},
				Dest: func(s moa.Slot) string {
					if pr, ok := t.bot.moaProvider(s.Provider); ok {
						return pr.BaseURL + "|" + pr.Model
					}
					return s.Provider
				},
				Call: func(cctx context.Context, s moa.Slot, req llm.Request) (*llm.Response, error) {
					pr, ok := t.bot.moaProvider(s.Provider)
					if !ok {
						return nil, fmt.Errorf("no provider %q", s.Provider)
					}
					cctx = providers.Only(cctx, pr.ID)
					if s.Effort != "" {
						cctx = llm.WithEffort(cctx, s.Effort)
					}
					return t.prov.Complete(cctx, req)
				},
			}
			runCtx = agent.WithClient(runCtx, mcClient)
		}
	}

	res, err := t.app.Agent.Do(runCtx, agent.Request{
		SessionID:        t.session,
		Input:            llm.Message{Role: "user", Content: turnText},
		History:          mc.History,
		Memory:           mc.Summary,
		Chat:             agent.IsSmallTalk(turnText),
		HistoryTokens:    historyBudget(t.st, window),
		ContextWindow:    window,
		ContextCarry:     carry,
		ContextCompacted: compacted,
		About:            about,
		ToolFilter:       filter,
		MaxSteps:         t.app.Config.MaxSteps,
		Listener:         l,
		ToolYield:        yield,
		Preload:          t.bot.sticky.get(uid),
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
		Tools:           append(t.bot.factTools(nil), t.sendFileTool()),
		Owner:           true,
		OwnerNote:       t.bot.ownerNote(true, tierConf{}),
		FocusTools:      true,
		EfficientTools:  true,
	})
	if res != nil {
		t.bot.sticky.keep(uid, res.Loaded)
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
	uid := t.currentUID()
	if err := t.mem.Append(ctx, uid, userTurn, stored); err != nil {
		log.Printf("terminal: saving the conversation: %v", err)
		return
	}
	if res != nil {
		t.bot.saveContextCarry(ctx, uid, res.Context, mc)
	}
	t.tidy.Add(1)
	go func(targetUID int64) {
		defer t.tidy.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if _, err := t.mem.Tidy(ctx, targetUID); err != nil {
			log.Printf("terminal: compressing the conversation: %v", err)
		}
	}(uid)
}

func (t *terminal) New(ctx context.Context) error {
	uid := t.currentUID()
	if err := t.mem.Reset(ctx, uid); err != nil {
		return err
	}
	_ = t.st.SetUserPref(ctx, uid, store.PrefContextMeter, "")
	t.gate.ForgetSession(t.session)
	t.resumed = 0
	return nil
}

func (t *terminal) Compact(ctx context.Context) (string, error) {
	uid := t.currentUID()
	before, err := t.mem.Load(ctx, uid)
	if err != nil {
		return "", err
	}
	if before.Messages == 0 {
		return "Nothing to compact yet", nil
	}
	done, err := t.mem.Compress(ctx, uid)
	if err != nil {
		return "", err
	}
	if !done {
		return "The conversation is short already", nil
	}
	after, err := t.mem.Load(ctx, uid)
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
	fmt.Fprintf(&b, "%s %s — by %s (%s) · MIT License", app.Name, app.Version, app.Creator, app.CreatorURL)
	row := func(k, v string) { fmt.Fprintf(&b, "\n  %-11s %s", k, v) }
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
	failover := "on"
	if t.st != nil && !t.st.SettingBool(store.SettingFailover, true) {
		failover = "off"
	}
	row("failover", failover)
	row("window", shortCount(t.window())+" tokens")
	if mc, err := t.mem.Load(ctx, t.currentUID()); err == nil {
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
	row("execution", t.executionStatus())
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

var skipDirs = map[string]bool{".git": true, "node_modules": true, "vendor": true, ".venv": true, "venv": true, "__pycache__": true,
	"dist": true, "build": true, "target": true, ".next": true, ".cache": true, ".idea": true, ".macron": true}

func (t *terminal) Files() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if out, err := machine.Hide(exec.CommandContext(ctx, "git", "-C", t.dir, "ls-files", "--cached", "--others", "--exclude-standard")).Output(); err == nil {
		var files []string
		for _, f := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if f != "" {
				files = append(files, f)
			}
			if len(files) == 5000 {
				break
			}
		}
		return files
	}
	var files []string
	_ = filepath.WalkDir(t.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if len(files) >= 5000 {
			return filepath.SkipAll
		}
		if d.IsDir() {
			if path != t.dir && (skipDirs[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if rel, err := filepath.Rel(t.dir, path); err == nil {
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	return files
}

func (t *terminal) HasBrowser() bool {
	return t.app != nil && t.app.Registry != nil && t.app.Registry.HasBrowser()
}

func (t *terminal) Browse(ctx context.Context, args map[string]any) (string, error) {
	if !t.HasBrowser() {
		return "", errors.New("browser runtime is not available")
	}
	return t.app.Registry.Browse(ctx, t.session, args)
}

func convertBrowserState(s tools.BrowserLiveState) desk.BrowserState {
	out := desk.BrowserState{
		Available:      s.Available,
		Open:           s.Open,
		Live:           s.Live,
		AgentActive:    s.AgentActive,
		Takeover:       s.Takeover,
		TakeoverReason: s.TakeoverReason,
		URL:            s.URL,
		Title:          s.Title,
		Updated:        s.Updated,
		Cursor:         s.Cursor,
		Teaching:       s.Teaching,
		TeachStartedAt: s.TeachStartedAt,
	}
	if s.Challenge != nil {
		out.Challenge = &desk.BrowserChallenge{Status: s.Challenge.Status, Reason: s.Challenge.Reason}
	}
	if s.Dialog != nil {
		out.Dialog = &desk.BrowserDialog{ID: s.Dialog.ID, Type: s.Dialog.Type, Message: s.Dialog.Message, DefaultValue: s.Dialog.DefaultValue}
	}
	if s.Profile != nil {
		out.Profile = &desk.BrowserProfileBadge{ID: s.Profile.ID, Name: s.Profile.Name, Color: s.Profile.Color}
	}
	if len(s.TeachSteps) > 0 {
		out.TeachSteps = make([]desk.BrowserTeachStep, len(s.TeachSteps))
		for i, st := range s.TeachSteps {
			out.TeachSteps[i] = desk.BrowserTeachStep{
				Index:       st.Index,
				Kind:        st.Kind,
				Description: st.Description,
				URL:         st.URL,
				Sensitive:   st.Sensitive,
				At:          st.At,
			}
		}
	}
	if len(s.Tabs) > 0 {
		out.Tabs = make([]desk.BrowserTab, len(s.Tabs))
		for i, tab := range s.Tabs {
			out.Tabs[i] = desk.BrowserTab{
				Index:    tab.Index,
				URL:      tab.URL,
				Title:    tab.Title,
				Active:   tab.Active,
				Frozen:   tab.Frozen,
				Sleeping: tab.Sleeping,
				Crashed:  tab.Crashed,
			}
		}
	}
	if s.Viewport != nil {
		out.Viewport = &desk.BrowserViewport{Width: s.Viewport.Width, Height: s.Viewport.Height}
	}
	return out
}

func convertProfileView(v tools.BrowserProfileView) desk.BrowserProfileView {
	out := desk.BrowserProfileView{
		ID:            v.ID,
		Name:          v.Name,
		Color:         v.Color,
		Scope:         v.Scope,
		BuiltIn:       v.BuiltIn,
		HasProxy:      v.HasProxy,
		ProxyPassword: v.ProxyPassword,
	}
	if v.Proxy != nil {
		out.Proxy = &desk.BrowserProxyConfig{
			Scheme:   v.Proxy.Scheme,
			Host:     v.Proxy.Host,
			Port:     v.Proxy.Port,
			Username: v.Proxy.Username,
			Bypass:   v.Proxy.Bypass,
		}
	}
	return out
}

func (t *terminal) BrowserLiveState() desk.BrowserState {
	if !t.HasBrowser() {
		return desk.BrowserState{}
	}
	return convertBrowserState(t.app.Registry.BrowserState(t.session))
}

func (t *terminal) BrowserNav(ctx context.Context, action string, params map[string]any) (desk.BrowserState, error) {
	if !t.HasBrowser() {
		return desk.BrowserState{}, errors.New("browser runtime is not available")
	}
	st, err := t.app.Registry.BrowserNav(ctx, t.session, action, params)
	return convertBrowserState(st), err
}

func (t *terminal) BrowserInput(ctx context.Context, ev desk.BrowserInput) error {
	if !t.HasBrowser() {
		return errors.New("browser runtime is not available")
	}
	return t.app.Registry.BrowserInput(ctx, t.session, tools.BrowserInputEvent{
		Type:       ev.Type,
		X:          ev.X,
		Y:          ev.Y,
		DeltaX:     ev.DeltaX,
		DeltaY:     ev.DeltaY,
		Button:     ev.Button,
		ClickCount: ev.ClickCount,
		Key:        ev.Key,
		Text:       ev.Text,
		Modifiers:  ev.Modifiers,
	})
}

func (t *terminal) SetBrowserTakeover(ctx context.Context, active bool, reason string) (desk.BrowserState, error) {
	if !t.HasBrowser() {
		return desk.BrowserState{}, errors.New("browser runtime is not available")
	}
	st, err := t.app.Registry.SetBrowserTakeover(ctx, t.session, active, reason)
	return convertBrowserState(st), err
}

func (t *terminal) BrowserResize(ctx context.Context, width, height int) (desk.BrowserState, error) {
	if !t.HasBrowser() {
		return desk.BrowserState{}, errors.New("browser runtime is not available")
	}
	st, err := t.app.Registry.BrowserResize(ctx, t.session, width, height)
	return convertBrowserState(st), err
}

func (t *terminal) BrowserHandleDialog(ctx context.Context, accept bool, promptText, dialogID string) (desk.BrowserState, error) {
	if !t.HasBrowser() {
		return desk.BrowserState{}, errors.New("browser runtime is not available")
	}
	st, err := t.app.Registry.BrowserHandleDialog(ctx, t.session, accept, promptText, dialogID)
	return convertBrowserState(st), err
}

func (t *terminal) BrowserContextMenu(ctx context.Context, params map[string]any) (map[string]any, error) {
	if !t.HasBrowser() {
		return nil, errors.New("browser runtime is not available")
	}
	return t.app.Registry.BrowserContextMenu(ctx, t.session, params)
}

func (t *terminal) BrowserTeach(ctx context.Context, action string) (desk.BrowserState, []desk.BrowserTeachStep, error) {
	if !t.HasBrowser() {
		return desk.BrowserState{}, nil, errors.New("browser runtime is not available")
	}
	st, steps, err := t.app.Registry.BrowserTeach(ctx, t.session, action)
	outSteps := make([]desk.BrowserTeachStep, len(steps))
	for i, s := range steps {
		outSteps[i] = desk.BrowserTeachStep{
			Index:       s.Index,
			Kind:        s.Kind,
			Description: s.Description,
			URL:         s.URL,
			Sensitive:   s.Sensitive,
			At:          s.At,
		}
	}
	return convertBrowserState(st), outSteps, err
}

func (t *terminal) SetBrowserProfile(profileID string) (desk.BrowserProfileView, error) {
	if !t.HasBrowser() {
		return desk.BrowserProfileView{}, errors.New("browser runtime is not available")
	}
	v, err := t.app.Registry.SetSessionBrowserProfile(t.session, profileID)
	return convertProfileView(v), err
}

func (t *terminal) SetBrowserEventObserver(fn func(event string, payload map[string]any)) {
	if !t.HasBrowser() {
		return
	}
	t.app.Registry.SetBrowserEventObserver(t.session, fn)
}

func (t *terminal) SubscribeBrowserFrames() ([]byte, <-chan []byte, func()) {
	if !t.HasBrowser() {
		closed := make(chan []byte)
		close(closed)
		return nil, closed, func() {}
	}
	return t.app.Registry.SubscribeBrowserFrames(t.session)
}

func (t *terminal) SetBrowserObserver(fn func()) {
	if !t.HasBrowser() {
		return
	}
	if fn == nil {
		t.app.Registry.SetBrowserObserver(t.session, nil)
		return
	}
	t.app.Registry.SetBrowserObserver(t.session, func(tools.BrowserLiveState) {
		fn()
	})
}

func (t *terminal) CloseBrowser() {
	if t.HasBrowser() {
		t.app.Registry.CloseBrowser(t.session)
	}
}

func (t *terminal) Shell(ctx context.Context, command string) string {
	res := t.app.Machine.Run(ctx, t.session, machine.RunOptions{Shell: command, Timeout: 10 * time.Minute, InSession: true, MergeOutput: true})
	out := "$ " + command + "\n" + strings.TrimRight(res.Stdout, "\n")
	switch {
	case res.ExitCode == 124 && res.Error != "":
		out += "\n[timed out: " + res.Error + "]"
	case res.ExitCode == -1 && res.Error != "":
		out += "\n[could not run: " + res.Error + "]"
	default:
		out += fmt.Sprintf("\n[exit %d · %s]", res.ExitCode, res.Duration)
	}
	return out
}

const (
	settingTUITheme = "tui_theme"
	settingTUIRTL   = "tui_rtl"
)

func (t *terminal) RTL() string {
	v, _ := t.st.Setting(settingTUIRTL)
	return v
}

func (t *terminal) SetRTL(mode string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.st.SetSetting(ctx, settingTUIRTL, mode)
}

func (t *terminal) Theme() string {
	v, _ := t.st.Setting(settingTUITheme)
	return v
}

func (t *terminal) SetTheme(theme string) error {
	if theme == "auto" {
		theme = ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return t.st.SetSetting(ctx, settingTUITheme, theme)
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

func (t *terminal) ConfigureProvider(ctx context.Context, field, value string) (string, error) {
	if field == "failover" {
		on, err := parseBoolToggle(value)
		if err != nil {
			return "", err
		}
		if err := t.st.SetSetting(ctx, store.SettingFailover, fmt.Sprint(on)); err != nil {
			return "", err
		}
		if on {
			return "Auto-failover: on", nil
		}
		return "Auto-failover: off", nil
	}
	id := t.prov.ActiveID()
	if id == 0 {
		return "", errors.New("no provider is active")
	}
	p, err := t.st.Provider(ctx, id)
	if err != nil {
		return "", err
	}
	var summary string
	switch field {
	case "api":
		switch value {
		case "auto", "default":
			p.API = ""
			summary = fmt.Sprintf("Provider %s API: auto (%s)", p.Name, providers.API(*p))
		case llm.APIOpenAI, llm.APIResponses, llm.APIAnthropic:
			p.API = value
			summary = fmt.Sprintf("Provider %s API: %s", p.Name, value)
		default:
			return "", errors.New("API must be auto, openai, responses, or anthropic")
		}
	case "vision":
		on, err := parseBoolToggle(value)
		if err != nil {
			return "", err
		}
		p.Vision = on
		summary = fmt.Sprintf("Provider %s vision: %v", p.Name, on)
	case "stream":
		on, err := parseBoolToggle(value)
		if err != nil {
			return "", err
		}
		p.NoStream = !on
		summary = fmt.Sprintf("Provider %s streaming: %v", p.Name, on)
	default:
		return "", fmt.Errorf("unknown provider setting %q", field)
	}
	normalized, err := providers.Normalize(*p)
	if err != nil {
		return "", err
	}
	if err := t.st.UpdateProvider(ctx, normalized); err != nil {
		return "", err
	}
	if err := t.prov.Reload(ctx); err != nil {
		return "", err
	}
	return summary, nil
}

func (t *terminal) TestActiveProvider(ctx context.Context) (string, error) {
	p, ok := t.prov.Active()
	if !ok {
		return "", errors.New("no provider is active")
	}
	res, err := t.prov.Test(ctx, p)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Provider %s (%s) OK in %s: %q", p.Name, p.Model, res.Latency.Round(time.Millisecond), shortText(res.Reply, 60)), nil
}

func parseBoolToggle(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("expected on or off, got %q", v)
}
