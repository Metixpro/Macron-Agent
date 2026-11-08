package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func anthropicClient(url string) *Client {
	c := fastClient(url)
	c.API, c.Model = APIAnthropic, "claude-sonnet-5"
	return c
}

func anthropicSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &probe)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, e)
	}
}

func TestAnthropicMessagesAPI(t *testing.T) {
	var got map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		if r.URL.Path != "/v1/messages" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		anthropicSSE(w,
			`{"type":"message_start","message":{"model":"claude-sonnet-5","usage":{"input_tokens":120,"cache_read_input_tokens":9000,"cache_creation_input_tokens":300,"output_tokens":1}}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Let me "}}`,
			`{"type":"ping"}`,
			`{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"check."}}`,
			`{"type":"content_block_stop","index":0}`,
			`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"web_search","input":{}}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"query\": \"go"}}`,
			`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":" 1.26\"}"}}`,
			`{"type":"content_block_stop","index":1}`,
			`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}`,
			`{"type":"message_stop"}`)
	}))
	defer srv.Close()

	prompt := "You are a helpful assistant. [lots of stable rules]"
	msgs := []Message{
		{Role: "system", Content: prompt + "\n\nCurrent date and time: Monday 09:30", Stable: len(prompt)},
		{Role: "system", Content: "[memory] earlier: the user likes Go"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello!"},
		{Role: "system", Content: "[context] name: Ali"},
		{Role: "user", Parts: []Part{TextPart("what is in this picture?"), ImagePart("data:image/png;base64,AAAA")}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "toolu_0", Type: "function", Function: FunctionCall{Name: "view_image", Arguments: `{"file":"a.png"}`}}}},
		{Role: "tool", ToolCallID: "toolu_0", Content: "a cat"},
	}
	tools := []Tool{{Type: "function", Function: ToolDef{Name: "web_search", Description: "Search the web",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}, "required": []string{"query"}}}}}
	var streamed []string
	c := anthropicClient(srv.URL)
	c.APIKey = "sk-ant-test"
	resp, err := c.Complete(context.Background(), Request{Messages: msgs, Tools: tools, OnDelta: func(s string) { streamed = append(streamed, s) }})
	if err != nil {
		t.Fatal(err)
	}

	if header.Get("x-api-key") != "sk-ant-test" || header.Get("anthropic-version") != anthropicVersion {
		t.Errorf("headers: %v", header)
	}
	if got["model"] != "claude-sonnet-5" || got["stream"] != true || got["max_tokens"].(float64) != 32000 {
		t.Errorf("body: model %v stream %v max_tokens %v", got["model"], got["stream"], got["max_tokens"])
	}
	system := got["system"].([]any)
	if len(system) != 3 || system[0].(map[string]any)["text"] != prompt || system[0].(map[string]any)["cache_control"] == nil ||
		!strings.Contains(system[1].(map[string]any)["text"].(string), "Current date and time") || system[1].(map[string]any)["cache_control"] != nil ||
		!strings.HasPrefix(system[2].(map[string]any)["text"].(string), "[memory]") {
		t.Fatalf("the stable start of the prompt is cached, the clock and the memory follow: %v", system)
	}
	wire, _ := json.Marshal(got["messages"])
	for _, want := range []string{
		`{"content":[{"text":"hi","type":"text"}],"role":"user"}`,
		`{"content":[{"text":"hello!","type":"text"}],"role":"assistant"}`,
		`{"text":"[context] name: Ali","type":"text"}`,
		`{"source":{"data":"AAAA","media_type":"image/png","type":"base64"},"type":"image"}`,
		`{"id":"toolu_0","input":{"file":"a.png"},"name":"view_image","type":"tool_use"}`,
		`{"cache_control":{"type":"ephemeral"},"content":"a cat","tool_use_id":"toolu_0","type":"tool_result"}`,
	} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("messages miss %s:\n%s", want, wire)
		}
	}
	if n := strings.Count(string(wire), `"role":"user"`); n != 3 {
		t.Errorf("notes and tool results join the neighbouring user turn: %d user turns\n%s", n, wire)
	}
	tl := got["tools"].([]any)[0].(map[string]any)
	if tl["name"] != "web_search" || tl["input_schema"] == nil || tl["cache_control"] == nil {
		t.Errorf("tools: %v", tl)
	}

	if resp.Message.Content != "Let me check." || len(streamed) != 2 || streamed[1] != "Let me check." {
		t.Errorf("text: %q (streamed %q)", resp.Message.Content, streamed)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "toolu_1" || resp.Message.ToolCalls[0].Function.Arguments != `{"query": "go 1.26"}` {
		t.Errorf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.PromptTokens != 9420 || resp.Usage.CachedTokens != 9000 || resp.Usage.CompletionTokens != 42 || resp.Model != "claude-sonnet-5" {
		t.Errorf("finish %q usage %+v model %q", resp.FinishReason, resp.Usage, resp.Model)
	}
}

func TestAnthropicPlainAnswerErrorsAndLimits(t *testing.T) {
	var calls atomic.Int32
	var maxTokens []float64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		maxTokens = append(maxTokens, body["max_tokens"].(float64))
		switch calls.Add(1) {
		case 1:
			w.WriteHeader(529)
			fmt.Fprint(w, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
		case 2:
			w.WriteHeader(400)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: 8192 > 4096, which is the maximum allowed number of output tokens for claude-x"}}`)
		default:
			fmt.Fprint(w, `{"model":"claude-x","content":[{"type":"thinking","thinking":"hmm"},{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":10,"output_tokens":2}}`)
		}
	}))
	defer srv.Close()
	c := anthropicClient(srv.URL + "/v1")
	resp, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "ping"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "pong" || resp.Message.Thought() != "hmm" || resp.FinishReason != "stop" || resp.Usage.TotalTokens != 12 {
		t.Errorf("answer: %+v", resp)
	}
	if fmt.Sprint(maxTokens) != "[8192 8192 4096]" {
		t.Errorf("an overload is retried, a refused output limit is learned: %v", maxTokens)
	}

	// An error inside a stream is an API error, so the next provider takes over.
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anthropicSSE(w, `{"type":"message_start","message":{"model":"m","usage":{}}}`, `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`)
	}))
	defer broken.Close()
	c = anthropicClient(broken.URL)
	c.MaxRetries = 0
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}, OnDelta: func(string) {}})
	var api *APIError
	if !errors.As(err, &api) || api.Status != statusOverloaded || !api.Temporary() {
		t.Fatalf("stream error: %v", err)
	}
}

func TestOpenAICompatibleDetails(t *testing.T) {
	for base, want := range map[string]string{
		"https://api.anthropic.com": APIAnthropic, "https://api.anthropic.com/v1": APIAnthropic,
		"https://api.openai.com/v1": APIOpenAI, "https://openrouter.ai/api/v1": APIOpenAI, "http://localhost:11434/v1": APIOpenAI,
	} {
		if got := DetectAPI(base); got != want {
			t.Errorf("DetectAPI(%s) = %s", base, got)
		}
	}
	for base, want := range map[string]string{
		"https://api.openai.com/v1":                                                     "https://api.openai.com/v1/chat/completions",
		"https://gw.example/v1?key=1":                                                   "https://gw.example/v1/chat/completions?key=1",
		"https://res.openai.azure.com/openai/deployments/gpt-4o":                        "https://res.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=" + azureAPIVersion,
		"https://res.openai.azure.com/openai/deployments/gpt-4o?api-version=2025-01-01": "https://res.openai.azure.com/openai/deployments/gpt-4o/chat/completions?api-version=2025-01-01",
		"https://res.openai.azure.com/openai/v1":                                        "https://res.openai.azure.com/openai/v1/chat/completions",
	} {
		if got := (&Client{BaseURL: base}).endpoint(); got != want {
			t.Errorf("endpoint(%s) = %s, want %s", base, got, want)
		}
	}
	if got := (&Client{BaseURL: "https://x.com", API: APIAnthropic}).endpoint(); got != "https://x.com/v1/messages" {
		t.Errorf("anthropic endpoint: %s", got)
	}
	azure := &Client{BaseURL: "https://res.openai.azure.com/openai/v1", APIKey: "az"}
	req, _ := http.NewRequest(http.MethodPost, azure.endpoint(), nil)
	azure.authorize(req)
	if req.Header.Get("api-key") != "az" || req.Header.Get("Authorization") != "" {
		t.Errorf("Azure takes its key in api-key: %v", req.Header)
	}

	// Reasoning models get no temperature and max_completion_tokens.
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["temperature"]; ok && body["model"] == "custom-reasoner" {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"error":{"message":"Unsupported value: 'temperature' does not support 0.3 with this model. Only the default (1) value is supported."}}`)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	c := fastClient(srv.URL)
	c.Model, c.Temperature = "o4-mini", 0.3
	if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}, MaxTokens: 50}); err != nil {
		t.Fatal(err)
	}
	if _, ok := bodies[0]["temperature"]; ok || bodies[0]["max_completion_tokens"] != float64(50) || bodies[0]["max_tokens"] != nil {
		t.Errorf("reasoning model body: %v", bodies[0])
	}
	// An unknown model that refuses the temperature is learned once.
	bodies = nil
	c = fastClient(srv.URL)
	c.Model, c.Temperature = "custom-reasoner", 0.3
	for range 2 {
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 {
		t.Errorf("one refusal, then no temperature: %d requests", len(bodies))
	}
}

// A temperature the admin chose goes to every model, 0 included; without
// one, OpenAI-compatible models get 0.3 and Claude and reasoning models none.
// A model that refuses it is learned once.
func TestTheChosenTemperature(t *testing.T) {
	var bodies []map[string]any
	refuse := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		if _, ok := body["temperature"]; ok && refuse {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"temperature is not supported for this model"}}`)
			return
		}
		if strings.HasSuffix(r.URL.Path, "/messages") {
			fmt.Fprint(w, `{"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		fmt.Fprint(w, okBody())
	}))
	defer srv.Close()
	ask := func(c *Client) map[string]any {
		bodies = nil
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}}); err != nil {
			t.Fatal(err)
		}
		return bodies[len(bodies)-1]
	}
	c := fastClient(srv.URL)
	if b := ask(c); b["temperature"] != 0.3 {
		t.Errorf("the default: %v", b["temperature"])
	}
	c.Temperature, c.TemperatureSet = 0, true
	if b := ask(c); b["temperature"] != float64(0) {
		t.Errorf("0 is sent as 0: %v", b["temperature"])
	}
	c = fastClient(srv.URL)
	c.Model = "o4-mini"
	c.Temperature, c.TemperatureSet = 1, true
	if b := ask(c); b["temperature"] != float64(1) {
		t.Errorf("a reasoning model gets the chosen one: %v", b["temperature"])
	}

	cl := fastClient(srv.URL)
	cl.API, cl.Model = APIAnthropic, "claude-sonnet-5"
	if b := ask(cl); b["temperature"] != nil {
		t.Errorf("Claude gets none by default: %v", b["temperature"])
	}
	cl.Temperature, cl.TemperatureSet = 0.5, true
	if b := ask(cl); b["temperature"] != 0.5 {
		t.Errorf("Claude gets the chosen one: %v", b["temperature"])
	}
	refuse = true
	ask(cl)
	if len(bodies) != 2 || bodies[1]["temperature"] != nil {
		t.Errorf("a refusal is learned and the request goes again without it: %d requests", len(bodies))
	}
	if b := ask(cl); b["temperature"] != nil || len(bodies) != 1 {
		t.Errorf("then none at all: %v", b)
	}
}
