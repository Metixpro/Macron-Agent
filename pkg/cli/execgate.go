package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

// execGate is the command gate of `macron exec`, the same one the bot and the terminal interface use: the owner's
// approval settings, rules and "always" list come from the database when there is one, and the configured model is the
// smart-mode guardian. The caller adds a terminal asker when stdin is a terminal; without one nobody can approve, so a
// flagged command runs only when the guardian finds it routine, approvals are off, or yolo is on.
func execGate(term *IO, client llm.ChatClient, dbPath, workDir string, yolo bool) (*cmdgate.Gate, func()) {
	env := cmdgate.Env{
		SudoPassword: os.Getenv("SUDO_PASSWORD") != "",
		SelfNames:    []string{"macron", config.ServiceName()},
		SelfDB:       []string{config.HomePath(config.FileDB)},
		SelfPID:      os.Getpid(),
		SelfHome:     config.Home(),
		SelfSecrets:  config.SecretFiles(),
		WorkDir:      workDir,
	}
	if p := store.DBPath(dbPath); p != "" && p != env.SelfDB[0] {
		env.SelfDB = append(env.SelfDB, p)
	}
	env.UserHome, _ = os.UserHomeDir()
	g := cmdgate.New(env, yolo)
	g.Guardian = cmdgate.NewGuardian(func(ctx context.Context, system, user string) (string, error) {
		resp, err := client.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "system", Content: system},
			{Role: "user", Content: user}}, MaxTokens: 200})
		if err != nil {
			return "", err
		}
		return resp.Message.Content, nil
	})
	if p := store.DBPath(dbPath); p == "" || !fileExists(p) {
		return g, func() {}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	st, err := store.Open(ctx, dbPath, store.Options{SecretKey: "exec"})
	if err != nil {
		fmt.Fprintf(term.Err, "approvals: the database does not open (%v), so the default settings apply\n", err)
		return g, func() {}
	}
	g.Policy = func() cmdgate.Policy { return cmdgate.PolicyFrom(st.Setting) }
	g.Store = &execGateStore{st: st}
	return g, func() { st.Close() }
}

type execGateStore struct {
	st    *store.Store
	mu    sync.Mutex
	allow []string
}

func (s *execGateStore) Allowlist() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.allow == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		list, err := s.st.CmdAllowlist(ctx)
		if err != nil {
			return nil
		}
		s.allow = append([]string{}, list...)
	}
	return s.allow
}

func (s *execGateStore) AddAllow(p string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := s.st.AddCmdAllow(ctx, p)
	s.mu.Lock()
	if err == nil && s.allow != nil {
		s.allow = append(s.allow, p)
	}
	s.mu.Unlock()
	return err
}

func (s *execGateStore) Audit(e cmdgate.Entry) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = s.st.AddCmdAudit(ctx, store.CmdAudit{At: e.At, Session: e.Session, Origin: string(e.Origin), Command: e.Command,
		Decision: e.Decision, Decider: e.Decider, Rule: e.Rule})
}

// termAsker asks the owner on the terminal, one question at a time: the question goes to stderr and the answer is one
// line of stdin. Anything but once, session or always is a no, and so is the end of stdin or of the task.
func termAsker(term *IO) cmdgate.Asker {
	var (
		mu    sync.Mutex
		start sync.Once
		lines = make(chan string)
	)
	return func(ctx context.Context, q cmdgate.Question) cmdgate.Choice {
		mu.Lock()
		defer mu.Unlock()
		start.Do(func() {
			go func() {
				defer close(lines)
				for {
					s, err := term.In.ReadString('\n')
					if s != "" || err == nil {
						lines <- s
					}
					if err != nil {
						return
					}
				}
			}()
		})
		why := make([]string, 0, len(q.Findings))
		for _, f := range q.Findings {
			why = append(why, f.Reason)
		}
		fmt.Fprintf(term.Err, "\n[!] Approval needed: %s\n", strings.Join(why, "; "))
		for _, line := range strings.Split(strings.TrimSpace(clip(q.Command, 1500)), "\n") {
			fmt.Fprintln(term.Err, "    "+line)
		}
		if q.Guardian != "" {
			fmt.Fprintln(term.Err, "    guardian: "+q.Guardian)
		}
		fmt.Fprint(term.Err, "Allow it? [o]nce, [s]ession, [a]lways, [N]o: ")
		select {
		case s, ok := <-lines:
			if !ok {
				fmt.Fprintln(term.Err)
				return cmdgate.Deny
			}
			return parseChoice(s)
		case <-ctx.Done():
			fmt.Fprintln(term.Err)
			return cmdgate.Deny
		}
	}
}

func parseChoice(s string) cmdgate.Choice {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "o", "once", "y", "yes", "بله", "آره":
		return cmdgate.Once
	case "s", "session":
		return cmdgate.Session
	case "a", "always", "همیشه":
		return cmdgate.Always
	}
	return cmdgate.Deny
}

func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
