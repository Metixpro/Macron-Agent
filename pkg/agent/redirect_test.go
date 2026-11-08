package agent

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestModelRedirectPreservesCompletedToolsAndLastStepBudget(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(callsTo("write_receipt", `{}`), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "The report is concise."}, "stop")
	})
	var effects atomic.Int32
	var pending atomic.Bool
	requests := 0
	tool := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "write_receipt", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
		if ctx.Err() != nil {
			t.Error("tool context was cancelled")
		}
		effects.Add(1)
		return ToolOutput{Text: "receipt: completed once"}, nil
	}}
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "Prepare a report"}, MaxSteps: 2, Tools: []LocalTool{tool},
		Inbox: func() []llm.Message {
			if pending.Swap(false) {
				return []llm.Message{{Role: "user", Content: "Make the report concise"}}
			}
			return nil
		},
		ModelRequest: func(ctx context.Context) (context.Context, func() bool) {
			requests++
			if requests == 2 {
				c, cancel := context.WithCancel(ctx)
				cancel()
				pending.Store(true)
				return c, func() bool { return true }
			}
			return ctx, func() bool { return false }
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if effects.Load() != 1 || res.Steps != 2 || res.Answer != "The report is concise." {
		t.Fatalf("redirect repeated work or consumed a tool step: effects=%d result=%+v", effects.Load(), res)
	}
	actual := f.all()
	if len(actual) != 2 {
		t.Fatalf("model requests: %d", len(actual))
	}
	req := actual[1]["messages"].([]any)
	foundTool, foundCorrection := false, false
	for _, v := range req {
		m := v.(map[string]any)
		if m["role"] == "tool" && strings.Contains(m["content"].(string), "receipt:") {
			foundTool = true
		}
		if m["role"] == "user" && strings.Contains(m["content"].(string), "Make the report concise") {
			foundCorrection = true
		}
	}
	if !foundTool || !foundCorrection {
		t.Fatal("redirect lost tool receipt or last-step correction")
	}
}
