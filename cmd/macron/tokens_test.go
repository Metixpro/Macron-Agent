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

// What a request costs, for a user with a long conversation behind them: a
// greeting a few thousand tokens; a task, the tools and the conversation.
// With 🧠 set by hand, a window of it; automatic, all of it until the
// model's window fills (then it is compacted).
func TestRequestsStayLight(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	for range 120 {
		_ = h.st.AppendContext(ctx, 55, store.ContextMessage{Role: "user", Content: strings.Repeat("این یه پیام نسبتاً طولانیه برای تست. ", 12), Tokens: 200},
			store.ContextMessage{Role: "assistant", Content: strings.Repeat("این جواب ربات به اون پیامه با کمی جزئیات. ", 20), Tokens: 400})
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 { // the model calls tools natively
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
	// Automatic: the whole conversation (~72k tokens) goes along, under where the window fills.
	// A greeting: the light prompt and the recent turns (chatHistoryTokens, 6k: enough to know what a short reply is about).
	if n, _ := cost("سلام"); n > 10000 {
		t.Errorf("a greeting costs ~%d tokens", n)
	}
	if n, _ := cost("یه عکس دیگه پیدا کن"); n < 70000 || n > 192000 {
		t.Errorf("automatic: the conversation fills the window: ~%d tokens", n)
	}

	// Set by hand: a window of it (the conversation is folded down to it).
	if err := h.st.SetSetting(ctx, store.SettingHistoryTokens, "8000"); err != nil {
		t.Fatal(err)
	}
	if n, _ := cost("سلام"); n > 10000 {
		t.Errorf("a greeting costs ~%d tokens", n)
	}
	// The tools: 26 KB, +2 KB for the browser and motion graphics tools where Chromium is installed (a production server),
	// +0.3 KB for telegram_lookup, +0.6 KB for the chart's series and types and Markdown PDFs, +0.7 KB for update_plan,
	// +0.5 KB for background_task, +1.1 KB for apply_patch, shell_jobs and background shell jobs (1.44), +0.2 KB for
	// ssh_servers (1.45), +0.5 KB for use_tools and the short github tool of a user who has not connected it (1.49, 1.50),
	// +0.95 KB for browser_agent where browser-use is installed (2.5), +0.15 KB for generate_image's styles (2.7),
	// +0.4 KB for search_past_chats and the second memory file (the personal edition; it drops the plans tool, this
	// test's bot is not personal), and +4.5 KB for bash_execute, run_code, apply_patch and the other developer
	// tools, which exist on every machine with an interpreter, and the plugins tool.
	if n, tools := cost("یه عکس از برج میلاد برام پیدا کن"); n > 25000 || tools > 38000 {
		t.Errorf("a task costs ~%d tokens (%d bytes of tools)", n, tools)
	}
}
