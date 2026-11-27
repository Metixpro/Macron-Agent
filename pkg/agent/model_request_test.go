package agent

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

type modelRequestClient func(context.Context, llm.Request) (*llm.Response, error)

func (f modelRequestClient) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	return f(ctx, req)
}

func agentWithModelRequestClient(t *testing.T, client modelRequestClient) *Agent {
	t.Helper()
	m, err := machine.New(machine.Config{BaseDir: t.TempDir(), Sandbox: "off"})
	if err != nil {
		t.Fatal(err)
	}
	return NewAgent(client, tools.NewRegistry(m, nil, client, tools.Options{}), Options{MaxSteps: 2, Channel: ChannelCLI})
}

func TestModelRequestRemainsActiveDuringOverflowRecovery(t *testing.T) {
	calls, cleanups := 0, 0
	a := agentWithModelRequestClient(t, func(ctx context.Context, req llm.Request) (*llm.Response, error) {
		if err := ctx.Err(); err != nil {
			t.Fatalf("provider called with a canceled context: %v", err)
		}
		calls++
		if calls <= 2 {
			return nil, &llm.APIError{Status: http.StatusRequestEntityTooLarge}
		}
		for _, m := range req.Messages {
			if strings.Contains(m.Content, "The steps to summarize:") {
				return &llm.Response{Message: llm.Message{Content: "Earlier context."}}, nil
			}
		}
		return &llm.Response{Message: llm.Message{Content: "Recovered successfully."}}, nil
	})
	res, err := a.Do(context.Background(), Request{
		SessionID: "overflow",
		Input:     llm.Message{Content: "Continue the conversation."},
		History: []llm.Message{
			{Role: "user", Content: strings.Repeat("Earlier context. ", 500)},
			{Role: "assistant", Content: "Acknowledged."},
		},
		Owner: true,
		ModelRequest: func(ctx context.Context) (context.Context, func() bool) {
			request, cancel := context.WithCancel(ctx)
			return request, func() bool {
				cleanups++
				cancel()
				return false
			}
		},
	})
	if err != nil || res == nil || res.Answer != "Recovered successfully." || calls < 3 || cleanups != 1 {
		t.Fatalf("recovery: result=%+v, err=%v, calls=%d, cleanups=%d", res, err, calls, cleanups)
	}
}

func TestModelRequestStopsAfterRepeatedRedirects(t *testing.T) {
	calls := 0
	a := agentWithModelRequestClient(t, func(context.Context, llm.Request) (*llm.Response, error) {
		calls++
		if calls > 3 {
			t.Fatal("model redirects exceeded the request budget")
		}
		return nil, context.Canceled
	})
	res, err := a.Do(context.Background(), Request{
		SessionID: "redirects",
		Input:     llm.Message{Content: "Run the task."},
		Owner:     true,
		ModelRequest: func(ctx context.Context) (context.Context, func() bool) {
			request, cancel := context.WithCancel(ctx)
			cancel()
			return request, func() bool { return true }
		},
	})
	if err != nil || res == nil || !res.Partial || res.StopReason != stopSteps.String() || calls != 3 {
		t.Fatalf("redirect termination: result=%+v, err=%v, calls=%d", res, err, calls)
	}
}
