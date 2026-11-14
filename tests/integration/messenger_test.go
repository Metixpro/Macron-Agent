package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/messenger"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestChannelParsingAndPrivateIsolation(t *testing.T) {
	b, _ := messenger.New("bale", "secret-token")
	b.HTTP = &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
		var p map[string]any
		_ = json.NewDecoder(r.Body).Decode(&p)
		if p["offset"] != float64(4) {
			t.Error(p)
		}
		return response(200, `{"ok":true,"result":[{"update_id":3,"message":{"message_id":1,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"old"}},{"update_id":4,"message":{"message_id":2,"from":{"id":42},"chat":{"id":42,"type":"private"},"text":"new"}}]}`), nil
	})}
	messages, next, e := b.Poll(context.Background(), "4")
	if e != nil || next != "5" || len(messages) != 1 || messages[0].Text != "new" || !messages[0].Private {
		t.Fatal(messages, next, e)
	}
	r, _ := messenger.New("rubika", "secret-token")
	r.HTTP = &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		if strings.HasSuffix(req.URL.Path, "getChat") {
			return response(200, `{"status":"OK","data":{"chat":{"chat_type":"User","user_id":"u0owner"}}}`), nil
		}
		return response(200, `{"status":"OK","data":{"updates":[{"type":"NewMessage","chat_id":"b0chat","new_message":{"message_id":"1","text":"test","sender_id":"u0owner","sender_type":"User"}}],"next_offset_id":"next"}}`), nil
	})}
	messages, next, e = r.Poll(context.Background(), "")
	if e != nil || next != "next" || len(messages) != 1 {
		t.Fatal(messages, next, e)
	}
	ok, e := r.Private(context.Background(), messages[0])
	if !ok || e != nil {
		t.Fatal(ok, e)
	}
	other := messages[0]
	other.UserID = "other"
	ok, e = r.Private(context.Background(), other)
	if ok || e != nil {
		t.Fatal("cross-user private chat accepted")
	}
	if messenger.UserID("bale", "42") == messenger.UserID("rubika", "42") || messenger.UserID("bale", "42") >= 0 {
		t.Fatal("identities not isolated")
	}
}

type fakeMemory struct {
	mu    sync.Mutex
	turns []llm.Message
}

func (m *fakeMemory) Load(context.Context, int64) (*memory.Context, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return &memory.Context{History: append([]llm.Message(nil), m.turns...)}, nil
}
func (m *fakeMemory) Save(_ context.Context, _ int64, u, a string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.turns = append(m.turns, llm.Message{Role: "user", Content: u}, llm.Message{Role: "assistant", Content: a})
	return false, nil
}
func (m *fakeMemory) Reset(context.Context, int64) error {
	m.mu.Lock()
	m.turns = nil
	m.mu.Unlock()
	return nil
}

type channelMock struct {
	mu     sync.Mutex
	polled bool
	text   string
	cancel context.CancelFunc
	sent   []string
}

func (c *channelMock) Poll(ctx context.Context, _ string) ([]messenger.Message, string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.polled {
		c.polled = true
		text := c.text
		if text == "" {
			text = "run the probe"
		}
		return []messenger.Message{{ID: "1", ChatID: "stranger", UserID: "other", Text: "forbidden", Private: true}, {ID: "2", ChatID: "owner", UserID: "42", Text: text, Private: true}}, "done", nil
	}
	return nil, "done", nil
}
func (c *channelMock) Send(_ context.Context, chat, text string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if chat != "owner" {
		return fmt.Errorf("sent to stranger")
	}
	c.sent = append(c.sent, text)
	c.cancel()
	return nil
}

type scriptedLLM struct {
	mu       sync.Mutex
	calls    int
	executed *int
}

func (c *scriptedLLM) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.calls == 1 {
		return &llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: "test", Type: "function", Function: llm.FunctionCall{Name: "probe", Arguments: "{}"}}}}}, nil
	}
	if *c.executed != 1 {
		return nil, fmt.Errorf("tool was not executed")
	}
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: "probe result 437"}}, nil
}
func TestInboundOwnerExecutesToolAndReplies(t *testing.T) {
	m, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	executed := 0
	c := &scriptedLLM{executed: &executed}
	reg := tools.NewRegistry(m, nil, c, tools.Options{})
	reg.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: "probe", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, func(context.Context, string, map[string]any) (string, error) { executed++; return "437", nil })
	a := agent.NewAgent(c, reg, agent.Options{MaxSteps: 3, Channel: "bale"})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	transport := &channelMock{cancel: cancel}
	runner := &messenger.Runner{Platform: "bale", Client: transport, Owners: map[string]bool{"42": true}, Agent: a, Memory: &fakeMemory{}}
	_ = runner.Run(ctx)
	if executed != 1 || len(transport.sent) != 1 || transport.sent[0] != "probe result 437" {
		t.Fatal(executed, transport.sent)
	}
}

type failingCursor struct{}

func (failingCursor) Poll(context.Context, string) ([]messenger.Message, string, error) {
	return nil, "next", nil
}
func (failingCursor) Send(context.Context, string, string) error { return nil }
func TestCursorFailureStopsWorkers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := &messenger.Runner{Platform: "bale", Client: failingCursor{}, Owners: map[string]bool{"42": true}, SaveCursor: func(context.Context, string) error { return fmt.Errorf("disk full") }}
	start := time.Now()
	err := r.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "disk full") || time.Since(start) > 500*time.Millisecond {
		t.Fatal(err, time.Since(start))
	}
}
