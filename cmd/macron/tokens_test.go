package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func TestRequestsStayLight(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for range 120 {
		_ = h.st.AppendContext(ctx, 55, store.ContextMessage{Role: "user", Content: strings.Repeat("این یه پیام نسبتاً طولانیه برای تست. ", 12), Tokens: 200},
			store.ContextMessage{Role: "assistant", Content: strings.Repeat("این جواب ربات به اون پیامه با کمی جزئیات. ", 20), Tokens: 400})
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1", "type": "function",
				"function": map[string]any{"name": "list_files", "arguments": "{}"}}}}, 0
		}
		return map[string]any{"role": "assistant", "content": "ok"}, 0
	}
	h.send(private(56, "فایل‌هام رو نشون بده"))
	cost := func(text string) (tokens, toolBytes int) {
		h.llm.mu.Lock()
		h.llm.requests = nil
		h.llm.mu.Unlock()
		h.send(private(55, text))
		h.llm.mu.Lock()
		req := h.llm.requests[0]
		h.llm.mu.Unlock()
		raw, _ := json.Marshal(req["tools"])
		for _, m := range req["messages"].([]any) {
			tokens += memory.EstimateTokens(fmt.Sprint(m.(map[string]any)["content"]))
		}
		return tokens + len(raw)/4, len(raw)
	}

	if n, _ := cost("سلام"); n > 10000 {
		t.Errorf("a greeting costs ~%d tokens", n)
	}
	if n, _ := cost("یه عکس دیگه پیدا کن"); n < 70000 || n > 192000 {
		t.Errorf("automatic: the conversation fills the window: ~%d tokens", n)
	}

	if err := h.st.SetSetting(ctx, store.SettingHistoryTokens, "8000"); err != nil {
		t.Fatal(err)
	}
	if n, _ := cost("سلام"); n > 10000 {
		t.Errorf("a greeting costs ~%d tokens", n)
	}

	if n, tools := cost("یه عکس از برج میلاد برام پیدا کن"); n > 25000 || tools > 38000 {
		t.Errorf("a task costs ~%d tokens (%d bytes of tools)", n, tools)
	}
}
