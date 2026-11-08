package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestContextWindowTable(t *testing.T) {
	for model, want := range map[string]int{
		"claude-sonnet-5": 1000000, "anthropic/claude-sonnet-5-20261001": 1000000, "claude-3-5-haiku": 200000,
		"gpt-4o-mini": 128000, "gpt-5.4-mini": 400000, "gemini-3-pro": 1048576, "deepseek-chat": 1000000, "deepseek-coder": 128000,
		"qwen3-coder-plus": 1000000, "some-local-model": DefaultContextWindow, "": DefaultContextWindow,
	} {
		if got := ContextWindow(model); got != want {
			t.Errorf("ContextWindow(%q) = %d, want %d", model, got, want)
		}
	}
	if compactTokens(1000000) != 750000 || compactTokens(100000) != 75000 || compactTokens(DefaultContextWindow) != 192000 {
		t.Fatal("compaction starts at 75% of the window, whatever its size")
	}
	if ContextBar(41000, 100000) != "▰▰▰▱▱▱▱▱ 41%" || HumanTokens(82400) != "82k" || HumanTokens(1048576) != "1M" || DefaultContextWindow != 256000 {
		t.Fatalf("meter: %q %q %q", ContextBar(41000, 100000), HumanTokens(82400), HumanTokens(1048576))
	}
}

func TestEstimateTokensFollowsTheScript(t *testing.T) {
	en, fa := estimateText(strings.Repeat("word ", 100)), estimateText(strings.Repeat("کلمه ", 100))
	if en < 100 || en > 150 || fa < 180 || fa > 260 {
		t.Fatalf("estimates: en %d, fa %d", en, fa)
	}
}

func TestTailNeverSplitsToolResults(t *testing.T) {
	msgs := []llm.Message{{Role: "system"}, {Role: "user", Content: "do it"}}
	for i := range 6 {
		msgs = append(msgs,
			llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall(fmt.Sprint(i), "a", "{}"), toolCall(fmt.Sprint(i)+"b", "b", "{}")}},
			llm.Message{Role: "tool", ToolCallID: fmt.Sprint(i), Content: strings.Repeat("x", 4000)},
			llm.Message{Role: "tool", ToolCallID: fmt.Sprint(i) + "b", Content: "short"})
	}
	for _, budget := range []int{0, 500, 3000, 100000} {
		start := tailStart(msgs, 1, budget, minTailRounds)
		if start <= 1 || start >= len(msgs) || msgs[start].Role != "assistant" {
			t.Fatalf("budget %d: tail starts at %d (%s)", budget, start, msgs[start].Role)
		}
		rounds := 0
		for _, m := range msgs[start:] {
			if m.Role == "assistant" {
				rounds++
			}
		}
		if rounds < minTailRounds {
			t.Fatalf("budget %d: only %d rounds kept", budget, rounds)
		}
	}
}

func TestFitHistoryDropsTheOldest(t *testing.T) {
	var h []llm.Message
	for i := range 10 {
		h = append(h, llm.Message{Role: "user", Content: fmt.Sprintf("q%d %s", i, strings.Repeat("x", 400))},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("a%d %s", i, strings.Repeat("y", 400))})
	}
	got := fitHistory(h, 600)
	if len(got) >= len(h) || got[0].Role != "user" || got[len(got)-1].Content != h[len(h)-1].Content || estimateTokens(got) > 600 {
		t.Fatalf("kept %d messages starting with %s, %d tokens", len(got), got[0].Role, estimateTokens(got))
	}
	if len(fitHistory(h, 1<<20)) != len(h) {
		t.Fatal("a history that fits stays whole")
	}
}

// bigTool returns n characters each time: a job that fills the context. Its
// outputs stay whole (like a book's pages), so they are not shortened when
// the next round comes and the context really grows.
func bigTool(n int) LocalTool {
	if !slices.Contains(keepWhole, "fetch_chunk") {
		keepWhole = append(keepWhole, "fetch_chunk")
	}
	return LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "fetch_chunk", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			return ToolOutput{Text: strings.Repeat("data ", n/5)}, nil
		},
	}
}

func TestLongJobsAreCompacted(t *testing.T) {
	var f *fakeModel
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		req := f.all()[n]
		if first := msgs(req)[0]; strings.HasPrefix(fmt.Sprint(first["content"]), "You compress the working memory") {
			return 200, answer(map[string]any{"role": "assistant", "content": "## Goal\nRead every chunk.\n## Remaining Work\nKeep reading."}, "stop")
		}
		if strings.Contains(fmt.Sprint(req["messages"]), "chunk 9 done") {
			return 200, answer(map[string]any{"role": "assistant", "content": "all chunks read"}, "stop")
		}
		return 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": fmt.Sprintf("chunk %d done", n),
			"tool_calls": []any{map[string]any{"id": fmt.Sprintf("c%d", n), "type": "function", "function": map[string]any{"name": "fetch_chunk", "arguments": "{}"}}}},
			"finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": 0}}
	})
	a.MaxSteps = 30
	a.ContextWindow = func() int { return 20000 }
	var compacted, reports int
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "read all the chunks"}, Tools: []LocalTool{bigTool(12000)},
		Listener: &EventListener{
			OnContext: func(used, window int) {
				reports++
				if window != compactTokens(20000) { // the bar is full where a compaction starts
					t.Errorf("window %d", window)
				}
			},
			OnCompact: func(before, after, window int, summarized bool) {
				if summarized {
					compacted++
				}
				if after >= before {
					t.Errorf("a compaction makes room: %d → %d", before, after)
				}
			},
		}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Compactions == 0 || compacted != res.Compactions || reports == 0 {
		t.Fatalf("compactions %d, reported %d of %d", res.Compactions, compacted, reports)
	}
	sawSummary := false
	for _, req := range f.all() {
		ms := msgs(req)
		if strings.HasPrefix(fmt.Sprint(ms[0]["content"]), "You compress") {
			continue
		}
		calls := map[string]bool{}
		for _, m := range ms {
			if tc, ok := m["tool_calls"].([]any); ok {
				for _, c := range tc {
					calls[c.(map[string]any)["id"].(string)] = true
				}
			}
			if m["role"] == "tool" && !calls[fmt.Sprint(m["tool_call_id"])] {
				t.Fatalf("a tool result lost its call after compaction: %v", m["tool_call_id"])
			}
			if s, _ := m["content"].(string); strings.HasPrefix(s, summaryPrefix) {
				sawSummary = true
				if !strings.Contains(s, "Read every chunk.") {
					t.Fatalf("summary: %q", s)
				}
			}
		}
		if est := estimateTokens(toMessages(ms)); est > 20000 {
			t.Fatalf("a request of ~%d tokens went over the window", est)
		}
	}
	if !sawSummary {
		t.Fatal("the summary replaces the middle of the job")
	}
	if ms := msgs(f.all()[len(f.all())-1]); ms[2]["content"] != "read all the chunks" || !strings.Contains(fmt.Sprint(ms[1]["content"]), "Current date and time") {
		t.Fatalf("the user's request always stays, after the clock: %v", ms[2]["content"])
	}
}

func TestCompactionFallsBackToTheWorkLog(t *testing.T) {
	var f *fakeModel
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		req := f.all()[n]
		if strings.HasPrefix(fmt.Sprint(msgs(req)[0]["content"]), "You compress") {
			return 500, map[string]any{"error": "summarizer down"}
		}
		if n > 12 {
			return 200, answer(map[string]any{"role": "assistant", "content": "done"}, "stop")
		}
		return 200, answer(callsTo("fetch_chunk", "{}"), "tool_calls")
	})
	a.ContextWindow = func() int { return 20000 }
	a.MaxSteps = 30
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "go"}, Tools: []LocalTool{bigTool(12000)}})
	if err != nil || res.Compactions == 0 {
		t.Fatalf("%v, %d compactions", err, res.Compactions)
	}
	found := false
	for _, req := range f.all() {
		for _, m := range msgs(req) {
			if s, _ := m["content"].(string); strings.HasPrefix(s, summaryPrefix) && strings.Contains(s, "fetch_chunk(") {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("without a summary the work log keeps the job going")
	}
}

func TestOverflowTeachesTheWindow(t *testing.T) {
	var f *fakeModel
	overflows := 0
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		req := f.all()[n]
		if strings.HasPrefix(fmt.Sprint(msgs(req)[0]["content"]), "You compress") {
			return 200, answer(map[string]any{"role": "assistant", "content": "## Goal\nsummary"}, "stop")
		}
		if est := estimateTokens(toMessages(msgs(req))); est > 15000 {
			overflows++
			return 400, map[string]any{"error": map[string]any{"message": "maximum context length exceeded"}}
		}
		if n > 14 {
			return 200, answer(map[string]any{"role": "assistant", "content": "done"}, "stop")
		}
		return 200, answer(callsTo("fetch_chunk", "{}"), "tool_calls")
	})
	a.ContextWindow = func() int { return 1000000 } // wrong: the real one is smaller
	a.MaxSteps = 30
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "go"}, Tools: []LocalTool{bigTool(12000)}})
	if err != nil || res.Answer != "done" || res.Compactions == 0 {
		t.Fatalf("%+v %v (overflows %d)", res, err, overflows)
	}
}

func toMessages(ms []map[string]any) []llm.Message {
	var out []llm.Message
	for _, m := range ms {
		s, _ := m["content"].(string)
		out = append(out, llm.Message{Role: fmt.Sprint(m["role"]), Content: s})
	}
	return out
}

// busyJob is a long review job like the one in the bug report: a long earlier
// conversation, a big [context], pictures and large tool outputs, with a
// provider that counts 40% more tokens than the estimate.
func busyJob(t *testing.T, window int, about string) (*Result, []string, *fakeModel) {
	t.Helper()
	var f *fakeModel
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		ms := msgs(f.all()[n])
		if strings.HasPrefix(fmt.Sprint(ms[0]["content"]), "You compress") {
			return 200, answer(map[string]any{"role": "assistant", "content": "## Goal\nReview the project.\n## Remaining Work\nGo on."}, "stop")
		}
		if n > 40 {
			return 200, answer(map[string]any{"role": "assistant", "content": "done"}, "stop")
		}
		name := "fetch_chunk"
		if n%3 == 0 {
			name = "look"
		}
		return 200, map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "tool_calls": []any{
			map[string]any{"id": fmt.Sprintf("c%d", n), "type": "function", "function": map[string]any{"name": name, "arguments": "{}"}}}},
			"finish_reason": "tool_calls"}}, "usage": map[string]any{"prompt_tokens": estimateTokens(toMessages(ms)) * 14 / 10}}
	})
	a.MaxSteps = 45
	a.ContextWindow = func() int { return window }
	var hist []llm.Message
	for i := range 200 {
		hist = append(hist, llm.Message{Role: "user", Content: fmt.Sprintf("سوال %d %s", i, strings.Repeat("متن طولانی کاربر ", 40))},
			llm.Message{Role: "assistant", Content: fmt.Sprintf("جواب %d %s", i, strings.Repeat("جواب مفصل ربات ", 60))})
	}
	look := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "look", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			return ToolOutput{Text: "here", Images: []string{"data:image/png;base64,AAAA"}}, nil
		}}
	var trail []string
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "یه پروژه رو ریویو کن"}, History: hist,
		About: about, Tools: []LocalTool{bigTool(14000), look},
		Listener: &EventListener{
			OnContext: func(used, w int) { trail = append(trail, fmt.Sprint(used*100/w)) },
			OnCompact: func(before, after, w int, summarized bool) {
				trail = append(trail, fmt.Sprintf("C%d", after*100/w))
			},
		}})
	if err != nil {
		t.Fatal(err)
	}
	return res, trail, f
}

func TestCompactionReallyMakesRoom(t *testing.T) {
	res, trail, f := busyJob(t, 128000, strings.Repeat("اطلاعات کاربر و پیام ریپلای‌شده ", 300))
	t.Logf("%d compactions in %d steps: %v", res.Compactions, res.Steps, trail)
	if res.Answer != "done" || res.Compactions == 0 || res.Compactions > 3 {
		t.Fatalf("%d compactions in %d steps: %v", res.Compactions, res.Steps, trail)
	}
	prev := 0
	for i, p := range trail {
		var pct int
		if _, err := fmt.Sscanf(strings.TrimPrefix(p, "C"), "%d", &pct); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(p, "C") && pct < prev && (i == 0 || !strings.HasPrefix(trail[i-1], "C")) {
			t.Fatalf("the meter only goes down at an announced compaction: %v", trail)
		}
		prev = pct
		// The meter's 100% is where a compaction starts (75% of the window).
		if strings.HasPrefix(p, "C") && pct > 55 {
			t.Fatalf("a compaction must make real room, got %d%%: %v", pct, trail)
		}
		if pct > 110 {
			t.Fatalf("the context ran full: %v", trail)
		}
	}
	folded := false
	for _, req := range f.all() {
		ms := msgs(req)
		if strings.HasPrefix(fmt.Sprint(ms[0]["content"]), "You compress") {
			folded = folded || strings.Contains(fmt.Sprint(ms[1]["content"]), "EARLIER CONVERSATION")
			if est := estimateTokens(toMessages(ms)); est > 128000/2 {
				t.Fatalf("the summarizer's own request is too big: ~%d tokens", est)
			}
		}
	}
	if !folded {
		t.Fatal("the earlier conversation is folded into the summary too")
	}
	last := msgs(f.all()[len(f.all())-1])
	if fmt.Sprint(last[len(last)-1]["role"]) == "" || !strings.Contains(fmt.Sprint(last), "یه پروژه رو ریویو کن") {
		t.Fatal("the user's request always stays")
	}
}

func TestCompactionDoesNotRepeatWhenItCannotHelp(t *testing.T) {
	// The [context] alone fills most of the window: compaction cannot get under
	// the threshold, so it must not run again at every step.
	res, trail, _ := busyJob(t, 128000, strings.Repeat("اطلاعات خیلی زیاد ", 22000))
	t.Logf("%d compactions in %d steps: %v", res.Compactions, res.Steps, trail)
	if res.Compactions > 6 {
		t.Fatalf("%d compactions in %d steps: %v", res.Compactions, res.Steps, trail)
	}
}
