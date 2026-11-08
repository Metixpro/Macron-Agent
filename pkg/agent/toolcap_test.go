package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func manyTools(n int) []LocalTool {
	var out []LocalTool
	for i := 0; i < n; i++ {
		out = append(out, LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: fmt.Sprintf("plugin_tool_%03d", i),
			Description: "a plugin's tool", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
			Handler: func(context.Context, map[string]any) (ToolOutput, error) { return ToolOutput{Text: "ok"}, nil }})
	}
	return out
}

func limitedProvider(t *testing.T, limit int, reply func(n int) map[string]any) (*Agent, *[][]string) {
	t.Helper()
	var mu sync.Mutex
	var seen [][]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		names := toolNames(req)
		mu.Lock()
		seen = append(seen, names)
		n := len(seen)
		mu.Unlock()
		if len(names) > limit {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprintf(w, `{"error":{"message":"[400]: {\"error\":{\"message\":\"'tools' : maximum number of items is %d\",\"type\":\"invalid_request_error\"}}\n","type":"invalid_request_error","code":"bad_request"}}`, limit)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": reply(n)}}})
	}))
	t.Cleanup(srv.Close)
	client := llm.NewClient(srv.URL, "k", "m")
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(sb, search.NewEngine(nil), client, tools.Options{HTTP: netclient.NewClient(netclient.Options{})})
	fixed := time.Date(2026, 11, 2, 9, 30, 0, 0, time.UTC)
	return NewAgent(client, reg, Options{MaxSteps: 4, Channel: ChannelTelegram, Now: func() time.Time { return fixed }}), &seen
}

func TestMoreToolsThanACallCarries(t *testing.T) {
	a, seen := limitedProvider(t, MaxTools, func(n int) map[string]any {
		if n == 1 {
			return map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", ToolMoreTools, `{}`)}}
		}
		return map[string]any{"role": "assistant", "content": "انجام شد"}
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار رو بگو"}, Tools: manyTools(150)})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "انجام شد" || len(*seen) != 2 {
		t.Fatalf("answer %q after %d calls", res.Answer, len(*seen))
	}
	first, second := (*seen)[0], (*seen)[1]
	if len(first) >= 100 || !slices.Contains(first, ToolMoreTools) || !slices.Contains(first, "web_search") {
		t.Fatalf("the first call starts focused: %d tools", len(first))
	}
	if len(second) != MaxTools || !slices.Contains(second, "web_search") || !slices.Contains(second, "plugin_tool_000") {
		t.Fatalf("after more_tools: %d tools", len(second))
	}
}

func TestAProvidersSmallerToolLimitIsLearned(t *testing.T) {
	a, seen := limitedProvider(t, 20, func(int) map[string]any { return map[string]any{"role": "assistant", "content": "باشه"} })
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "سلام، یه فایل بساز"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "باشه" || len(*seen) != 2 || len((*seen)[1]) > 20 || a.toolLimit() != 20 {
		t.Fatalf("answer %q, calls %d, last %d tools, limit %d", res.Answer, len(*seen), len((*seen)[len(*seen)-1]), a.toolLimit())
	}

	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "دوباره"}}); err != nil || len((*seen)[2]) > 20 {
		t.Fatalf("next request: %v, %d tools", err, len((*seen)[2]))
	}
}

func TestToolLimitFrom(t *testing.T) {
	for _, c := range []struct {
		msg  string
		want int
	}{
		{`'tools' : maximum number of items is 128`, 128},
		{`Invalid 'tools': array too long. Expected an array with maximum length 128`, 0},
		{`at most 64 tools are allowed`, 64},
		{`property 'enable_thinking' is unsupported`, 0},
	} {
		if got := toolLimitFrom(fmt.Errorf("%s", c.msg)); got != c.want {
			t.Errorf("%q: %d, want %d", c.msg, got, c.want)
		}
	}
	if toolLimitFrom(nil) != 0 {
		t.Error("nil")
	}
}
