package llm

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestStreamReportsToolDrafts(t *testing.T) {
	args := []byte(`{"filename":"site/index.html","content":"` + strings.Repeat("<p>x</p>", 400) + `"}`)
	var b strings.Builder
	write := func(v any) {
		raw, _ := json.Marshal(v)
		b.WriteString("data: " + string(raw) + "\n\n")
	}
	write(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "Writing it now."}}}})
	write(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "id": "c1", "type": "function", "function": map[string]any{"name": "write_file", "arguments": ""}}}}}}})
	for i := 0; i < len(args); i += 100 {
		end := min(i+100, len(args))
		write(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"tool_calls": []any{map[string]any{"index": 0, "function": map[string]any{"arguments": string(args[i:end])}}}}}}})
	}
	write(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": "tool_calls"}}})
	b.WriteString("data: [DONE]\n\n")

	var names []string
	var heads []string
	gate := &toolGate{on: func(name string, size int, head string) {
		if size < 0 || size > len(args) {
			t.Errorf("size %d out of range", size)
		}
		names = append(names, name)
		heads = append(heads, head)
	}}
	resp, err := readStream(strings.NewReader(b.String()), func(string) {}, gate)
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Arguments != string(args) {
		t.Fatalf("tool call not assembled: %+v", resp.Message.ToolCalls)
	}
	if len(names) == 0 || names[0] != "write_file" {
		t.Fatalf("no draft reported: %v", names)
	}
	if !strings.Contains(heads[len(heads)-1], "site/index.html") {
		t.Fatalf("head lacks the file name: %q", heads[len(heads)-1])
	}
	if len(heads[len(heads)-1]) > 600 {
		t.Fatalf("head not clipped: %d bytes", len(heads[len(heads)-1]))
	}
}

func TestToolDraftFromContext(t *testing.T) {
	if toolDraftOf(context.Background()) != nil {
		t.Fatal("empty context has a draft callback")
	}
	called := false
	ctx := WithToolDraft(context.Background(), func(string, int, string) { called = true })
	toolDraftOf(ctx)("x", 1, "")
	if !called {
		t.Fatal("callback not carried by the context")
	}
	if WithToolDraft(context.Background(), nil) != context.Background() {
		t.Fatal("nil callback changed the context")
	}
	var g *toolGate
	g.push("x", "y")
}
