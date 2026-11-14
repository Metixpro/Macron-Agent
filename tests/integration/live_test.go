package integration

import (
	"context"
	"encoding/json"
	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/messenger"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

type tracedClient struct {
	c      llm.ChatClient
	mu     sync.Mutex
	names  []string
	counts []int
}

func (c *tracedClient) Complete(ctx context.Context, r llm.Request) (*llm.Response, error) {
	r.MaxTokens = 1500
	res, e := c.c.Complete(ctx, r)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts = append(c.counts, len(r.Tools))
	if res != nil {
		for _, call := range res.Message.ToolCalls {
			c.names = append(c.names, call.Function.Name)
		}
	}
	return res, e
}
func TestLiveQwenInboundChannelTools(t *testing.T) {
	key := liveModelKey(t)
	client := llm.NewClient(liveModelURL(), strings.TrimSpace(string(key)), liveModelName())
	client.MaxRetries = 0
	traced := &tracedClient{c: client}
	machine, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	reg := tools.NewRegistry(machine, nil, traced, tools.Options{})
	for _, def := range reg.GetTools() {
		if def.Function.Name != "bash_execute" {
			reg.Register(def, func(context.Context, string, map[string]any) (string, error) {
				return "error: unrelated tool disabled in validation", nil
			})
		}
	}
	a := agent.NewAgent(traced, reg, agent.Options{MaxSteps: 4, Channel: "bale", Soul: func() string { return "Use tools for requested actions, then report the exact result." }})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	defer cancel()
	transport := &channelMock{cancel: cancel, text: "با ابزار bash_execute دستور printf MACRON_PROBE_437 را اجرا کن و خروجی را دقیقاً بنویس."}
	runner := &messenger.Runner{Platform: "bale", Client: transport, Owners: map[string]bool{"42": true}, Agent: a, Memory: &fakeMemory{}, Timeout: 60 * time.Second}
	_ = runner.Run(ctx)
	if len(transport.sent) != 1 || !strings.Contains(transport.sent[0], "MACRON_PROBE_437") {
		t.Fatal(transport.sent)
	}
	t.Log("model tool calls", traced.names, "schema counts", traced.counts)
	if len(traced.names) == 0 {
		t.Fatal("no tool call")
	}
}
func TestLiveQwenGmailRead(t *testing.T) {
	key := liveModelKey(t)
	t.Setenv("MACRON_GMAIL_ACCESS_TOKEN", "fake-access")
	client := llm.NewClient(liveModelURL(), strings.TrimSpace(string(key)), liveModelName())
	client.MaxRetries = 0
	traced := &tracedClient{c: client}
	m, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	reads, sends := 0, 0
	httpMock := &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "gmail.googleapis.com" {
			t.Error("unexpected API host", req.URL.Host)
		}
		if req.Method == "POST" {
			sends++
			return response(403, `{}`), nil
		}
		reads++
		if strings.HasSuffix(req.URL.Path, "/messages") {
			return response(200, `{"messages":[{"id":"m1"}]}`), nil
		}
		return response(200, `{"id":"m1","snippet":"validation code GMAIL_PROBE_612","payload":{"mimeType":"text/plain","body":{"data":"R01BSUxfUFJPQkVfNjEy"},"headers":[{"name":"Subject","value":"Validation"}]}}`), nil
	})}
	reg := tools.NewRegistry(m, nil, traced, tools.Options{HTTP: httpMock})
	for _, def := range reg.GetTools() {
		if def.Function.Name != tools.ToolEmailClient {
			reg.Register(def, func(context.Context, string, map[string]any) (string, error) {
				return "error: unrelated tool disabled in validation", nil
			})
		}
	}
	a := agent.NewAgent(traced, reg, agent.Options{MaxSteps: 5, Soul: func() string { return "Use tools to read email; never invent messages." }})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	res, e := a.Do(ctx, agent.Request{SessionID: "gmail-check", Owner: true, Input: llm.Message{Content: "با ابزار email_client و provider=gmail ایمیل message_id=m1 را با action=read بخوان و کد آزمایشی داخلش را بگو. ایمیل نفرست."}})
	if e != nil {
		t.Fatal(e)
	}
	if reads == 0 || sends != 0 || res == nil || !strings.Contains(res.Answer, "GMAIL_PROBE_612") {
		if res != nil {
			t.Fatal(reads, sends, res.Answer)
		}
		t.Fatal(reads, sends, res)
	}
	raw, _ := json.Marshal(traced.names)
	_, _ = io.Discard.Write(raw)
	t.Log("model tool calls", traced.names, "schema counts", traced.counts)
}
