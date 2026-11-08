package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func skillAgent(t *testing.T, allow func(string) bool, withSkill bool, replies ...any) (*Agent, *scriptedLLM) {
	t.Helper()
	root := t.TempDir()
	if withSkill {
		p := filepath.Join(root, "devops", "linux-server-ops", skills.FileName)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte("---\ndescription: Use when diagnosing a Linux server over SSH.\n---\n1. uptime first."), 0o644)
	}
	st, err := skills.New(root)
	if err != nil {
		t.Fatal(err)
	}
	script := &scriptedLLM{replies: replies}
	srv := httptest.NewServer(http.HandlerFunc(script.handler))
	t.Cleanup(srv.Close)
	client := llm.NewClient(srv.URL, "k", "m")
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(sb, search.NewEngine(nil), client, tools.Options{HTTP: netclient.NewClient(netclient.Options{}), Skills: st, SkillWrite: allow})
	fixed := time.Date(2026, 10, 18, 9, 30, 0, 0, time.UTC)
	return NewAgent(client, reg, Options{MaxSteps: 3, Channel: ChannelTelegram, Skills: st, Now: func() time.Time { return fixed }}), script
}

func offeredTools(req map[string]any) map[string]bool {
	out := map[string]bool{}
	list, _ := req["tools"].([]any)
	for _, t := range list {
		fn, _ := t.(map[string]any)["function"].(map[string]any)
		name, _ := fn["name"].(string)
		out[name] = true
	}
	return out
}

func TestSkillsInThePrompt(t *testing.T) {
	a, script := skillAgent(t, func(s string) bool { return s == "admin" }, true, llm.Message{Role: "assistant", Content: "ok"})
	for _, session := range []string{"user", "admin"} {
		if _, err := a.Do(context.Background(), Request{SessionID: session, Input: llm.Message{Content: "my server is slow"}}); err != nil {
			t.Fatal(err)
		}
	}
	user, admin := script.requests[0], script.requests[1]
	sys := msgs(user)[0]["content"].(string)
	soulAt, indexAt, rulesAt := strings.Index(sys, "روح"), strings.Index(sys, "<available_skills>"), strings.Index(sys, "Ground rules")
	if indexAt < 0 || !(soulAt < indexAt && indexAt < rulesAt) {
		t.Fatalf("the index goes after the soul and before the rules (%d, %d, %d)", soulAt, indexAt, rulesAt)
	}
	for _, want := range []string{"- linux-server-ops: Use when diagnosing a Linux server over SSH.", "call read_skill first"} {
		if !strings.Contains(sys, want) {
			t.Errorf("prompt misses %q", want)
		}
	}
	if strings.Contains(sys, "1. uptime first.") {
		t.Error("the index never carries a skill's body")
	}
	if offeredTools(user)[tools.ToolSaveSkill] || strings.Contains(sys, "Learning:") {
		t.Error("a user who may not write skills is not offered save_skill")
	}
	if !offeredTools(user)[tools.ToolReadSkill] {
		t.Error("read_skill is offered")
	}
	adminSys := msgs(admin)[0]["content"].(string)
	if !offeredTools(admin)[tools.ToolSaveSkill] || !strings.Contains(adminSys, "Learning:") {
		t.Error("an admin is offered save_skill and the learning rule")
	}
}

func TestNoSkillsNoBlock(t *testing.T) {
	a, script := skillAgent(t, nil, false, llm.Message{Role: "assistant", Content: "ok"})
	if _, err := a.Do(context.Background(), Request{SessionID: "u", Input: llm.Message{Content: "hi"}}); err != nil {
		t.Fatal(err)
	}
	sys := msgs(script.requests[0])[0]["content"].(string)
	if strings.Contains(sys, "available_skills") || strings.Contains(sys, "- Skills:") {
		t.Fatal("no skills, no skills block")
	}
}

func TestFastModeKeepsOnlyReadSkill(t *testing.T) {
	a, script := skillAgent(t, func(string) bool { return true }, true, llm.Message{Role: "assistant", Content: "ok"})
	fast := func(n string) bool { return n == tools.ToolReadSkill || n == "web_search" }
	if _, err := a.Do(context.Background(), Request{SessionID: "admin", Input: llm.Message{Content: "hi"}, ToolFilter: fast}); err != nil {
		t.Fatal(err)
	}
	sys := msgs(script.requests[0])[0]["content"].(string)
	if !strings.Contains(sys, "<available_skills>") || strings.Contains(sys, "Learning:") || offeredTools(script.requests[0])[tools.ToolSaveSkill] {
		t.Fatal("fast mode: the index and read_skill stay, save_skill goes")
	}
}
