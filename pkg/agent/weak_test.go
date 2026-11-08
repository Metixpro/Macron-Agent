package agent

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func priceTool(got *map[string]any) LocalTool {
	return LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "lookup_price", Parameters: map[string]any{"type": "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}},
		"required":   []string{"query"}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			*got = args
			return ToolOutput{Text: "42"}, nil
		}}
}

// A call with the tool and its parameters named another way, and a number in
// Persian digits, runs as meant; the transcript keeps the repaired call.
func TestAWeakModelsCallIsRepaired(t *testing.T) {
	var got map[string]any
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "functions.LookupPrice", `{"q": "دلار", "limit": "۳"}`)}},
		llm.Message{Role: "assistant", Content: "۴۲"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار"}, Tools: []LocalTool{priceTool(&got)}})
	if err != nil || res.Answer != "۴۲" {
		t.Fatalf("%+v %v", res, err)
	}
	if got["query"] != "دلار" || got["limit"] != 3.0 {
		t.Errorf("the tool got: %v", got)
	}
	var call map[string]any
	for _, m := range msgs(script.requests[1]) {
		if tc, ok := m["tool_calls"].([]any); ok {
			call = tc[0].(map[string]any)["function"].(map[string]any)
		}
	}
	if call["name"] != "lookup_price" || call["arguments"] != `{"limit":3,"query":"دلار"}` {
		t.Errorf("the transcript keeps the repaired call: %v", call)
	}
	if q := res.CallQuality[""]; q.Calls != 1 || q.Fixed != 1 || q.Problems() != 1 {
		t.Errorf("quality: %+v", q)
	}
}

// «الان سرچ می‌کنم…» and no call: it is sent back to do it.
func TestAnAnnouncementWithoutACallIsSentBack(t *testing.T) {
	var got map[string]any
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "الان برات قیمت رو پیدا می‌کنم…"},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "lookup_price", `{"query": "دلار"}`)}},
		llm.Message{Role: "assistant", Content: "۴۲ هزار تومن"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار چنده"}, Tools: []LocalTool{priceTool(&got)}})
	if err != nil || res.Answer != "۴۲ هزار تومن" || got["query"] != "دلار" {
		t.Fatalf("%+v %v %v", res, err, got)
	}
	m := msgs(script.requests[1])
	if !strings.Contains(m[len(m)-1]["content"].(string), "you called no tool") {
		t.Errorf("the notice: %v", m[len(m)-1])
	}
	if q := res.CallQuality[""]; q.Stalled != 1 || q.Calls != 1 {
		t.Errorf("quality: %+v", q)
	}

	// Refused calls count too.
	a, _ = setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "lookup_price", `{"limit": 3}`)}},
		llm.Message{Role: "assistant", Content: "ok"},
	)
	res, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت"}, Tools: []LocalTool{priceTool(&got)}})
	if q := res.CallQuality[""]; q.Refused != 1 {
		t.Errorf("a call that cannot run: %+v", q)
	}
}

// A focused request is offered what its words point to, and more_tools
// brings every tool from the next step.
func TestFocusedTools(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("m1", ToolMoreTools, `{"need": "a chart"}`)}},
		llm.Message{Role: "assistant", Content: "done"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار امروز چنده؟"}, FocusTools: true})
	if err != nil || res.Answer != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	first, second := toolNames(script.requests[0]), toolNames(script.requests[1])
	if !slices.Contains(first, "web_search") || !slices.Contains(first, ToolMoreTools) || slices.Contains(first, "generate_chart") {
		t.Errorf("focused: %v", first)
	}
	if len(first) >= len(second) || !slices.Contains(second, "generate_chart") || slices.Contains(second, ToolMoreTools) {
		t.Errorf("then everything: %d → %d %v", len(first), len(second), second)
	}
	b, _ := json.Marshal(script.requests[0]["messages"])
	if !strings.Contains(string(b), "more_tools first") {
		t.Error("the model is told how its list works")
	}

	// A chart request gets the chart tool at once; without FocusTools, everything.
	if f := focusTools(allDefs(a), "یه نمودار از فروش بکش"); !hasTool(f, "generate_chart") || hasTool(f, "search_music") {
		t.Errorf("chart: %v", names(f))
	}
	if f := focusTools(allDefs(a), "this video https://youtu.be/x"); !hasTool(f, "download_media_ytdlp") || !hasTool(f, "web_fetch") {
		t.Errorf("video: %v", names(f))
	}
	a, script = setup(t, llm.Message{Role: "assistant", Content: "ok"})
	a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار"}})
	if slices.Contains(toolNames(script.requests[0]), ToolMoreTools) {
		t.Error("only a focused request has more_tools")
	}
}

func allDefs(a *Agent) []llm.Tool { return a.Registry.GetTools() }

func names(ts []llm.Tool) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Function.Name)
	}
	return out
}

func hasTool(ts []llm.Tool, n string) bool { return slices.Contains(names(ts), n) }

// A model without native calls writes one in a JSON block, with the name
// and a parameter off: it still runs.
func TestATextCallWithANameOffRuns(t *testing.T) {
	var got map[string]any
	a, _ := setup(t,
		llm.Message{Role: "assistant", Content: "```json\n{\"name\": \"lookupPrice\", \"arguments\": {\"q\": \"دلار\"}}\n```"},
		llm.Message{Role: "assistant", Content: "۴۲"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار"}, Tools: []LocalTool{priceTool(&got)}})
	if err != nil || res.Answer != "۴۲" || got["query"] != "دلار" {
		t.Fatalf("%+v %v %v", res, err, got)
	}
	// An answer that only shows a JSON example keeps it.
	if s := stripToolMarkup("```json\n{\"name\": \"search\", \"arguments\": {}}\n```", map[string]bool{"web_search": true}); !strings.Contains(s, "search") {
		t.Errorf("an example stays in an answer: %q", s)
	}
}

// A model that struggles hands the rest of the job to the stronger one, once:
// the context it is asked with changes and a focused request gets every tool.
func TestAStrugglingModelEscalates(t *testing.T) {
	type key struct{}
	var whys []string
	var escalated int
	escalate := func(ctx context.Context) context.Context {
		escalated++
		return context.WithValue(ctx, key{}, "strong")
	}
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "الان برات قیمت رو پیدا می‌کنم…"},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "generate_chart", `{}`)}},
		llm.Message{Role: "assistant", Content: "done"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت دلار امروز چنده؟"},
		FocusTools: true, Escalate: escalate, Listener: &EventListener{OnEscalate: func(why string) { whys = append(whys, why) }}})
	if err != nil || res.Answer != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	if !res.Escalated || escalated != 1 || len(whys) != 1 || !strings.Contains(whys[0], "announced") {
		t.Errorf("escalated %v, %d times, why %v", res.Escalated, escalated, whys)
	}
	if first, second := toolNames(script.requests[0]), toolNames(script.requests[1]); slices.Contains(first, "generate_chart") || !slices.Contains(second, "generate_chart") {
		t.Errorf("the stronger model chooses from every tool: %v → %v", first, second)
	}

	// Two failed steps; the third does not ask again.
	escalated, whys = 0, nil
	fail := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "flaky", Parameters: map[string]any{"type": "object"}}},
		Handler: func(ctx context.Context, _ map[string]any) (ToolOutput, error) {
			if ctx.Value(key{}) == "strong" {
				return ToolOutput{Text: "fine"}, nil
			}
			return ToolOutput{}, errors.New("connection refused")
		}}
	a, _ = setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "flaky", `{}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c2", "flaky", `{}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c3", "flaky", `{}`)}},
		llm.Message{Role: "assistant", Content: "ok"},
	)
	var outs []string
	res, err = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "do it"}, Tools: []LocalTool{fail},
		Escalate: escalate, Listener: &EventListener{OnEscalate: func(why string) { whys = append(whys, why) },
			OnToolEnd: func(_, _, result string, _ time.Duration) { outs = append(outs, result) }}})
	if err != nil || !res.Escalated || escalated != 1 || len(whys) != 1 || whys[0] != "steps failed" {
		t.Fatalf("%+v %v: %d %v", res, err, escalated, whys)
	}
	if len(outs) != 3 || !strings.Contains(outs[2], "fine") {
		t.Errorf("the steps after it run with the stronger model's context: %q", outs)
	}

	// Without Escalate nothing changes.
	a, _ = setup(t, llm.Message{Role: "assistant", Content: ""}, llm.Message{Role: "assistant", Content: "hi"})
	if res, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "hey"}}); res.Escalated {
		t.Error("escalated with nowhere to go")
	}
}
