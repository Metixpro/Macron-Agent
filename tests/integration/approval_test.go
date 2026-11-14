package integration

import (
	"context"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/messenger"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"strings"
	"sync"
	"testing"
	"time"
)

type approvalTransport struct {
	mu        sync.Mutex
	first     bool
	inbox     chan []messenger.Message
	cancel    context.CancelFunc
	questions int
	final     string
}

func (c *approvalTransport) Poll(ctx context.Context, _ string) ([]messenger.Message, string, error) {
	c.mu.Lock()
	first := !c.first
	c.first = true
	c.mu.Unlock()
	if first {
		return []messenger.Message{{ID: "1", ChatID: "owner", UserID: "42", Text: "probe", Private: true}}, "1", nil
	}
	select {
	case <-ctx.Done():
		return nil, "", ctx.Err()
	case m := <-c.inbox:
		return m, "2", nil
	}
}
func (c *approvalTransport) Send(_ context.Context, _ string, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.Contains(text, "/approve") {
		c.questions++
		c.inbox <- []messenger.Message{{ID: "2", ChatID: "other", UserID: "stranger", Text: "/approve", Private: true}, {ID: "3", ChatID: "owner", UserID: "42", Text: "/approve", Private: true}}
		return nil
	}
	c.final = text
	c.cancel()
	return nil
}
func TestOwnerApprovalReachesTool(t *testing.T) {
	m, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	executed := 0
	c := &scriptedLLM{executed: &executed}
	reg := tools.NewRegistry(m, nil, c, tools.Options{})
	gate := cmdgate.New(cmdgate.Env{}, false)
	gate.Policy = func() cmdgate.Policy { return cmdgate.Policy{Mode: cmdgate.ModeManual} }
	reg.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: "probe", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, func(ctx context.Context, _ string, _ map[string]any) (string, error) {
		v := gate.CheckAction(ctx, "session", cmdgate.Action{Key: "probe", Detail: "local test", Level: cmdgate.Dangerous, Rule: "send-out"})
		if !v.Allow {
			return "", fmt.Errorf("approval did not arrive: %s", v.Message)
		}
		executed++
		return "437", nil
	})
	a := agent.NewAgent(c, reg, agent.Options{MaxSteps: 3, Channel: "bale"})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	transport := &approvalTransport{cancel: cancel, inbox: make(chan []messenger.Message, 1)}
	r := &messenger.Runner{Platform: "bale", Client: transport, Owners: map[string]bool{"42": true}, Agent: a, Memory: &fakeMemory{}, Timeout: time.Minute}
	_ = r.Run(ctx)
	if executed != 1 || transport.questions != 1 || transport.final != "probe result 437" {
		t.Fatal(executed, transport.questions, transport.final)
	}
}
