package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func execTask(term *IO, args []string) int {
	fs := flag.NewFlagSet("exec", flag.ContinueOnError)
	fs.SetOutput(term.Err)
	timeout := fs.Duration("timeout", 10*time.Minute, "overall task deadline")
	allTools := fs.Bool("all-tools", false, "advertise every available tool immediately instead of automatic discovery")
	selected := fs.String("tools", "", "comma-separated tool names (default: all available tools)")
	steps := fs.Int("max-steps", 30, "maximum tool rounds")
	yolo := fs.Bool("yolo", false, "run what the command gate flags without asking (what it blocks outright still never runs)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" || *timeout <= 0 || *steps < 1 || *steps > 1000 {
		fmt.Fprintln(term.Err, "usage: macron exec [--timeout 10m] [--max-steps 30] [--yolo] TASK")
		return 2
	}
	env, err := loadEnvFile(config.EnvFilePath())
	if err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}

	value := func(key string) string {
		if v, ok := os.LookupEnv(key); ok {
			return v
		}
		v, _ := env.Get(key)
		return v
	}
	endpoint, model := value(keyAPIURL), value(keyModel)
	u, err := url.Parse(endpoint)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || model == "" {
		fmt.Fprintln(term.Err, "configure MACRON_API_URL and MACRON_MODEL in config.env or the environment first (Ollama/local endpoints are supported)")
		return 1
	}
	b, err := brain.Open(config.Home(), agent.DefaultSoul)
	if err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	sk, err := skills.New(config.HomePath("skills"))
	if err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	if _, err := sk.Seed(); err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	m, err := machine.New(machine.Config{
		BaseDir: config.HomePath("workspaces"),
		HomeDir: config.Home(),
		Hide:    config.SecretFiles(),
	})
	if err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	session := "exec-" + hex.EncodeToString(nonce[:])
	defer m.StopShell(session)
	client := llm.NewClient(endpoint, value(keyAPIKey), model)
	client.API = llm.DetectAPI(endpoint)
	reg := tools.NewRegistry(m, search.NewEngine(nil), client, tools.Options{Skills: sk, SkillWrite: func(string) bool { return true }})
	dbPath := value(config.EnvDB)
	if dbPath == "" {
		dbPath = config.HomePath(config.FileDB)
	}
	gate, closeGate := execGate(term, client, dbPath, m.BaseDir, *yolo || os.Getenv("MACRON_YOLO") == "1")
	defer closeGate()
	reg.Gate, reg.CheckpointDir = gate, config.HomePath("checkpoints")
	var filter func(string) bool
	if *selected != "" {
		allowed := map[string]bool{}
		for _, name := range strings.Split(*selected, ",") {
			name = strings.TrimSpace(name)
			if name == "" || (!reg.Has(name) && name != "delegate_task" && name != "update_plan") {
				fmt.Fprintf(term.Err, "unknown tool %q\n", name)
				return 2
			}
			allowed[name] = true
		}
		filter = func(name string) bool { return allowed[name] }
	}
	a := agent.NewAgent(client, reg, agent.Options{Channel: agent.ChannelCLI, Soul: b.Soul, Skills: sk, MaxSteps: *steps})
	root, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(root, *timeout)
	defer cancel()
	if term.Interactive {
		ctx = cmdgate.WithAsker(ctx, termAsker(term))
	}
	fmt.Fprintln(term.Err, "workspace:", config.HomePath("workspaces", session))

	progress := func(prefix string) *agent.EventListener {
		return &agent.EventListener{
			OnToolEnd: func(name, args, result string, took time.Duration) {
				if name != agent.ToolPlan {
					fmt.Fprintf(term.Err, "%s• %s (%s)\n", prefix, agent.StepLine(name, args, result), took.Round(100*time.Millisecond))
				}
			},
		}
	}
	l := progress("")
	l.OnPlan = func(items []agent.PlanItem) {
		done, now := 0, ""
		for _, it := range items {
			switch it.Status {
			case agent.PlanDone:
				done++
			case agent.PlanInProgress:
				now = it.Text
			}
		}
		fmt.Fprintf(term.Err, "[plan %d/%d] %s\n", done, len(items), now)
	}
	l.SubAgent = func(title, _ string) *agent.EventListener {
		fmt.Fprintf(term.Err, "↳ sub-agent: %s\n", title)
		return progress("  ")
	}

	result, err := a.Do(ctx, agent.Request{
		SessionID:      session,
		Input:          llm.Message{Role: "user", Content: prompt},
		About:          b.User.Raw() + "\n" + b.Memory.Raw(),
		Lang:           "en",
		Owner:          true,
		ToolFilter:     filter,
		FocusTools:     !*allTools && filter == nil,
		EfficientTools: !*allTools && filter == nil,
		Listener:       l,
	})
	if result != nil && result.Answer != "" {
		fmt.Fprintln(term.Out, result.Answer)
	}
	if err != nil {
		fmt.Fprintln(term.Err, err)
		return 1
	}
	if ctx.Err() != nil {
		fmt.Fprintln(term.Err, ctx.Err())
		return 1
	}
	if result == nil || result.Partial {
		fmt.Fprintln(term.Err, "task incomplete")
		return 1
	}
	return 0
}
