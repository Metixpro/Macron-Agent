package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestCheckSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "required": []any{"title", "items"}, "additionalProperties": false,
		"properties": map[string]any{
			"title": map[string]any{"type": "string", "minLength": 3.0},
			"items": map[string]any{"type": "array", "minItems": 1.0, "items": map[string]any{"type": "object", "required": []any{"price"},
				"properties": map[string]any{"price": map[string]any{"type": "number", "minimum": 0.0}, "unit": map[string]any{"enum": []any{"usd", "irr"}}}}},
		}}
	good, _ := reportJSON("Done.\n```json\n{\"title\": \"قیمت‌ها\", \"items\": [{\"price\": 12.5, \"unit\": \"usd\"}]}\n```")
	if errs := checkSchema(schema, good); len(errs) != 0 {
		t.Fatalf("good: %v", errs)
	}
	bad, _ := reportJSON(`{"title": "x", "items": [{"unit": "eur", "price": -1}], "extra": 1}`)
	errs := strings.Join(checkSchema(schema, bad), " | ")
	for _, want := range []string{"$.title: is shorter than 3", "$.items[0].unit: must be one of", "$.items[0].price: is below 0", `$: "extra" is not allowed`} {
		if !strings.Contains(errs, want) {
			t.Errorf("missing %q in %s", want, errs)
		}
	}
	if v, ok := reportJSON("text then [1, 2] at the end"); !ok || len(v.([]any)) != 2 {
		t.Fatalf("array: %v", v)
	}
	if _, ok := reportJSON("no json here"); ok {
		t.Fatal("nothing to find")
	}
}

// A sub-agent's report is held to the output_schema its main agent gave: a
// report that does not match is sent back once with what is wrong, and the
// main agent gets the checked JSON.
func TestDelegateHoldsTheReportToItsSchema(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("d1", ToolDelegate,
			`{"goal":"price of gold","output_schema":{"type":"object","required":["price","unit"],"properties":{"price":{"type":"number"},"unit":{"type":"string"}}}}`)}},
		llm.Message{Role: "assistant", Content: "Gold is about 2400 dollars."},                           // the sub-agent, no JSON
		llm.Message{Role: "assistant", Content: "```json\n{\"price\": 2400, \"unit\": \"usd/oz\"}\n```"}, // corrected
		llm.Message{Role: "assistant", Content: "طلا حدود ۲۴۰۰ دلاره"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "قیمت طلا"}, MaxSteps: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 4 {
		t.Fatalf("calls: %d", len(script.requests))
	}
	if !strings.Contains(llmText(script.requests[1]), "End your report with one JSON value") ||
		!strings.Contains(llmText(script.requests[2]), `$: \"price\" is required`) && !strings.Contains(llmText(script.requests[2]), "no JSON found") {
		t.Fatalf("the sub-agent is told the schema, then what is wrong:\n%s", llmText(script.requests[2]))
	}
	if !strings.Contains(llmText(script.requests[3]), "[output_schema] The report matches the schema") || !strings.Contains(llmText(script.requests[3]), "usd/oz") {
		t.Fatalf("the main agent gets the checked JSON:\n%s", llmText(script.requests[3]))
	}
	if res.Answer != "طلا حدود ۲۴۰۰ دلاره" {
		t.Fatalf("answer: %q", res.Answer)
	}
}

// A job that stopped to ask the user resumes with the answer: its own
// conversation with the model, tool results included, and its plan.
func TestASuspendedJobResumes(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("p1", ToolPlan, `{"items":[{"text":"پیدا کردن","status":"in_progress"},{"text":"ساختن","status":"pending"}]}`),
			toolCall("q1", "ask", `{"question":"کدوم رنگ؟"}`)}},
		llm.Message{Role: "assistant", Content: "رنگ آبی، ساختمش"},
	)
	ask := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "ask", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Handler: func(context.Context, map[string]any) (ToolOutput, error) {
			return ToolOutput{Text: "asked; the turn ends", EndTurn: true}, nil
		}}
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "یه لوگو بساز"}, Tools: []LocalTool{ask}})
	if err != nil || res.EndedBy != "ask" || res.Suspended == nil {
		t.Fatalf("suspended: %v %q %v", err, res.EndedBy, res.Suspended)
	}
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "آبی"}, Tools: []LocalTool{ask},
		Resume: res.Suspended, About: "The user answered your poll by choosing: آبی"}); err != nil {
		t.Fatal(err)
	}
	second := llmText(script.requests[1])
	if !strings.Contains(second, "یه لوگو بساز") || !strings.Contains(second, "asked; the turn ends") || !strings.Contains(second, "The user answered your question") ||
		!strings.Contains(second, "by choosing: آبی") || !strings.Contains(second, "پیدا کردن") {
		t.Fatalf("the resumed job sees its own conversation:\n%s", second)
	}
}
