package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStreamDeliversThrottledTailAtCompletion(t *testing.T) {
	streams := []struct {
		name     string
		read     func(io.Reader, func(string), *toolGate) (*Response, error)
		delta    string
		terminal string
	}{
		{"chat", readStream, `{"choices":[{"delta":{"content":"x"}}]}`, "[DONE]"},
		{"anthropic", readAnthropicStream, `{"type":"content_block_delta","delta":{"type":"text_delta","text":"x"}}`, `{"type":"message_stop"}`},
		{"responses", readResponsesStream, `{"type":"response.output_text.delta","delta":"x"}`, `{"type":"response.completed","response":{"status":"completed"}}`},
	}
	for _, s := range streams {
		t.Run(s.name, func(t *testing.T) {
			var fixture strings.Builder
			for range 509 {
				fmt.Fprintf(&fixture, "data: %s\n\n", s.delta)
			}
			fmt.Fprintf(&fixture, "data: %s\n\n", s.terminal)
			readPastEnd := false
			body := io.MultiReader(strings.NewReader(fixture.String()), readerFunc(func([]byte) (int, error) {
				readPastEnd = true
				return 0, io.ErrUnexpectedEOF
			}))
			var last string
			notifications := 0
			result, err := s.read(body, func(text string) {
				last = text
				notifications++
			}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if readPastEnd || len(last) != 509 || last != result.Message.Content || notifications != 501 {
				t.Fatalf("read after terminal=%t, last length=%d, notifications=%d", readPastEnd, len(last), notifications)
			}
		})
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(b []byte) (int, error) { return f(b) }

func TestAnthropicMax4CacheControlAndMaxTokensClamp(t *testing.T) {
	c := NewClient("https://api.anthropic.com/v1/messages", "k", "claude-sonnet-4-6")
	c.maxOutput.Store(4096)

	if got := c.anthropicMaxTokens(Request{MaxTokens: 16000}); got != 4096 {
		t.Fatalf("anthropicMaxTokens(16000) = %d, want clamped 4096", got)
	}

	req := Request{
		Messages: []Message{
			{Role: "system", Content: "sys", Stable: 3},
			{Role: "user", Content: "u1", Cache: true},
			{Role: "assistant", Content: "a1", Cache: true},
			{Role: "user", Content: "u2", Cache: true},
			{Role: "assistant", Content: "a2", Cache: true},
			{Role: "user", Content: "u3", Cache: true},
		},
		Tools: []Tool{{Type: "function", Function: ToolDef{Name: "ping", Description: "d"}}},
	}
	body := c.anthropicBody(req)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	count := bytes.Count(raw, []byte(`"cache_control"`))
	if count > 4 {
		t.Fatalf("anthropicBody produced %d cache_control blocks, limit is 4", count)
	}
	if count < 4 {
		t.Fatalf("expected 4 cache_control blocks, got %d", count)
	}
}

func TestReadStreamReasoningNoDuplicateAndCumulativeToolName(t *testing.T) {
	sse := strings.Join([]string{
		`data: {"model":"deepseek-r1","choices":[{"delta":{"reasoning_content":"step 1 ","reasoning":"step 1 "}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"bash","arguments":"{\"cmd\":"}}]}}]}`,
		`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"bash_execute","arguments":"\"ls\"}"}}]}}]}`,
		`data: {"choices":[{"delta":{"content":"done"},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}, "\n") + "\n"

	resp, err := readStream(strings.NewReader(sse), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.ReasoningContent != "step 1 " {
		t.Fatalf("reasoning doubled or mangled: %q", resp.Message.ReasoningContent)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].Function.Name != "bash_execute" {
		t.Fatalf("unexpected tool calls: %+v", resp.Message.ToolCalls)
	}
}

func TestReadAnthropicSSESniffFallback(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"claude-sonnet-4-6\",\"usage\":{\"input_tokens\":10}}}\n" +
		"data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n" +
		"data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":5}}\n" +
		"data: {\"type\":\"message_stop\"}\n"

	resp := &http.Response{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(sse)),
	}
	out, _, err := readAnthropic(resp, nil, nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if out.Message.Content != "hello" || out.Usage.CompletionTokens != 5 {
		t.Fatalf("unexpected response: %+v", out)
	}
}
