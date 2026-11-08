package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func call(id, name string) ToolCall {
	return ToolCall{ID: id, Type: "function", Function: FunctionCall{Name: name, Arguments: `{"q":"x"}`}}
}

func TestNormalizeTranscript(t *testing.T) {
	in := []Message{
		{Role: "system", Content: "s"},
		{Role: "user", Content: "do it"},

		{Role: "assistant", ToolCalls: []ToolCall{call("", "web_search"), call("", "run_code")}},
		{Role: "tool", Content: "found"},
		{Role: "user", Content: "(a message that came meanwhile)"},
		{Role: "tool", Content: "ran"},

		{Role: "assistant", ToolCalls: []ToolCall{call("a1", "x"), call("a2", "y")}},
		{Role: "tool", ToolCallID: "a2", Content: "y done"},

		{Role: "tool", ToolCallID: "zz", Name: "z", Content: "lost"},

		{Role: "assistant", ToolCalls: []ToolCall{call("d", "p"), call("d", "q")}},
		{Role: "tool", ToolCallID: "d", Content: "p out"},
		{Role: "tool", ToolCallID: "d", Content: "q out"},
		{Role: "assistant", Content: "done"},
	}
	out := normalizeTranscript(in, true)

	for i, m := range out {
		if m.Role != "assistant" || len(m.ToolCalls) == 0 {
			continue
		}
		ids := map[string]bool{}
		for j, c := range m.ToolCalls {
			if c.ID == "" || ids[c.ID] {
				t.Fatalf("call %d of %d: id %q", j, i, c.ID)
			}
			ids[c.ID] = true
			r := out[i+1+j]
			if r.Role != "tool" || r.ToolCallID != c.ID {
				t.Fatalf("the result of %s must follow it: %+v", c.ID, r)
			}
		}
	}
	for _, m := range out {
		if m.Role == "tool" && m.ToolCallID == "zz" {
			t.Error("a result with no call is not sent as one")
		}
	}
	text := func(ms []Message) string { b, _ := json.Marshal(ms); return string(b) }
	got := text(out)
	for _, want := range []string{`"found"`, `"ran"`, "did not finish", "Earlier result of z", `"p out"`, `"q out"`, "came meanwhile"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in %s", want, got)
		}
	}
	if strings.Index(got, "came meanwhile") < strings.Index(got, `"ran"`) {
		t.Error("a message between a call and its results goes after them")
	}
	if in[2].ToolCalls[0].ID != "" {
		t.Error("the caller's messages are not changed")
	}

	flat := normalizeTranscript(in, false)
	for _, m := range flat {
		if m.Role == "tool" || len(m.ToolCalls) > 0 || m.ToolCallID != "" {
			t.Fatalf("a request without tools has no tool turns: %+v", m)
		}
	}
	if f := text(flat); !strings.Contains(f, "I used web_search") || !strings.Contains(f, "Result of run_code") {
		t.Errorf("they are written as text: %s", f)
	}
}

func TestGatewayErrorsAndToolPairing(t *testing.T) {
	for text, want := range map[string]bool{
		"[Error] No tool call found for function call output with call_id call_1_0.": true,
		"  [ERROR]: upstream timeout":                                true,
		"No tool call found for function call output with call_id x": true,
		"Error: invalid 'input[3].call_id'":                          true,
		"[1] First, open the file":                                   false,
		"خطای No tool call found یعنی اینکه پاسخ ابزار بدون فراخوانی خودش فرستاده شده و باید تاریخچه درست بشه؛ این کار رو می‌کنیم…": false,
		"Error handling in Go is explicit.": false,
	} {
		if got := looksLikeGatewayError(text); got != want {
			t.Errorf("%q: %v", text, got)
		}
	}
	for text, want := range map[string]bool{"[": true, "[Err": true, " [error] x": true, "[1] a": false, "Hello": false, "[e": true, "[ex": false} {
		if got := errorPrefix(text); got != want {
			t.Errorf("prefix %q: %v", text, got)
		}
	}

	var mu sync.Mutex
	var bodies []string
	mode := "strict"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		m := mode
		mu.Unlock()
		switch {
		case m == "strict" && strings.Contains(string(b), `"role":"tool"`):
			http.Error(w, `{"error":{"message":"No tool call found for function call output with call_id call_1_0."}}`, http.StatusBadRequest)
		case m == "text-error":
			w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"[Error] No tool call found for function call output with call_id call_1_0."}}]}`))
		case m == "stream-error":
			w.Header().Set("Content-Type", "text/event-stream")
			for _, d := range []string{"[Err", "or] No tool call found", " for function call output"} {
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": d}}}})
				w.Write([]byte("data: " + string(b) + "\n\n"))
			}
			w.Write([]byte("data: [DONE]\n\n"))
		default:
			w.Write([]byte(okBody()))
		}
	}))
	defer srv.Close()
	c := fastClient(srv.URL + "/")
	conv := []Message{{Role: "user", Content: "q"}, {Role: "assistant", ToolCalls: []ToolCall{call("", "web_search")}}, {Role: "tool", Content: "r"}}
	tools := []Tool{{Type: "function", Function: ToolDef{Name: "web_search"}}}

	resp, err := c.Complete(context.Background(), Request{Messages: conv, Tools: tools})
	if err != nil || resp.Message.Content != "hello" {
		t.Fatalf("a refused pairing goes again as text: %v", err)
	}
	if len(bodies) != 2 || strings.Contains(bodies[1], `"role":"tool"`) {
		t.Errorf("the second try has no tool turns: %d", len(bodies))
	}

	mode = "default"
	bodies = nil
	if _, err := c.Complete(context.Background(), Request{Messages: conv}); err != nil || strings.Contains(bodies[0], `"role":"tool"`) || strings.Contains(bodies[0], "tool_calls") {
		t.Errorf("a request without tools sends no tool turns: %v %s", err, bodies[0])
	}

	mode = "text-error"
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "q"}}})
	var api *APIError
	if !errors.As(err, &api) || api.Status != http.StatusBadGateway {
		t.Fatalf("an error sent as the answer is an error: %v", err)
	}

	mode = "stream-error"
	var shown []string
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "q"}}, OnDelta: func(s string) { shown = append(shown, s) }})
	if err == nil || len(shown) != 0 {
		t.Errorf("a streamed error is never shown: %v %q", err, shown)
	}
}
