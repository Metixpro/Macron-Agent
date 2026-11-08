package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func responsesClient(url string) *Client {
	c := fastClient(url)
	c.API, c.Model = APIResponses, "gpt-5.2"
	return c
}

func responsesSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, e := range events {
		var probe struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal([]byte(e), &probe)
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", probe.Type, e)
	}
}

func TestCleanBaseURLAndDetect(t *testing.T) {
	for in, want := range map[string][2]string{
		"https://api.kie.ai/openai/v1/responses":       {"https://api.kie.ai/openai/v1", APIResponses},
		"https://api.kie.ai/openai/v1/responses/":      {"https://api.kie.ai/openai/v1", APIResponses},
		"https://api.openai.com/v1/chat/completions":   {"https://api.openai.com/v1", APIOpenAI},
		"https://api.anthropic.com/v1/messages":        {"https://api.anthropic.com/v1", APIAnthropic},
		"https://api.openai.com/v1":                    {"https://api.openai.com/v1", ""},
		"https://x.example/v1/responses?api-version=2": {"https://x.example/v1?api-version=2", APIResponses},
		" https://gw.example/ ":                        {"https://gw.example/", ""},
		"not a url":                                    {"not a url", ""},
	} {
		base, api := CleanBaseURL(in)
		if base != want[0] || api != want[1] {
			t.Errorf("%q → %q, %q; want %q, %q", in, base, api, want[0], want[1])
		}
	}
	if DetectAPI("https://api.kie.ai/openai/v1/responses") != APIResponses || DetectAPI("https://api.kie.ai/openai/v1") != APIOpenAI ||
		DetectAPI("https://api.anthropic.com/v1") != APIAnthropic {
		t.Error("DetectAPI")
	}
}

func TestResponsesAPIStreamedWithToolCalls(t *testing.T) {
	var got map[string]any
	var header http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Clone()
		if r.URL.Path != "/openai/v1/responses" {
			t.Errorf("path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		responsesSSE(w,
			`{"type":"response.created","response":{"id":"resp_1","model":"gpt-5.2","status":"in_progress"}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"reasoning","id":"rs_1"}}`,
			`{"type":"response.reasoning_summary_text.delta","output_index":0,"delta":"Thinking it over. "}`,
			`{"type":"response.output_item.added","output_index":1,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Let me "}`,
			`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"check."}`,
			`{"type":"response.output_item.added","output_index":2,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"web_search","arguments":""}}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":"{\"query\": \"go"}`,
			`{"type":"response.function_call_arguments.delta","output_index":2,"delta":" 1.26\"}"}`,
			`{"type":"response.function_call_arguments.done","output_index":2,"arguments":"{\"query\": \"go 1.26\"}"}`,
			`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.2-2026","status":"completed","output":[],"usage":{"input_tokens":9420,"input_tokens_details":{"cached_tokens":9000},"output_tokens":42,"total_tokens":9462}}}`)
	}))
	defer srv.Close()

	msgs := []Message{
		{Role: "system", Content: "You are a helpful assistant."},
		{Role: "system", Content: "[memory] earlier: the user likes Go"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello!"},
		{Role: "system", Content: "[context] name: Ali"},
		{Role: "user", Parts: []Part{TextPart("what is in this picture?"), ImagePart("data:image/png;base64,AAAA")}},
		{Role: "assistant", ToolCalls: []ToolCall{{ID: "call_0", Type: "function", Function: FunctionCall{Name: "view_image", Arguments: `{"file":"a.png"}`}}}},
		{Role: "tool", ToolCallID: "call_0", Content: "a cat"},
	}
	tools := []Tool{{Type: "function", Function: ToolDef{Name: "web_search", Description: "Search the web",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"}}}}}}
	var streamed []string
	c := responsesClient(srv.URL + "/openai/v1")
	c.APIKey = "sk-test"
	resp, err := c.Complete(context.Background(), Request{Messages: msgs, Tools: tools, MaxTokens: 4000, OnDelta: func(s string) { streamed = append(streamed, s) }})
	if err != nil {
		t.Fatal(err)
	}

	if header.Get("Authorization") != "Bearer sk-test" || header.Get("Accept") != "text/event-stream" {
		t.Errorf("headers: %v", header)
	}
	if got["model"] != "gpt-5.2" || got["stream"] != true || got["store"] != false || got["max_output_tokens"].(float64) != 4000 {
		t.Errorf("body: %v", got)
	}
	if got["messages"] != nil || got["max_tokens"] != nil || got["stream_options"] != nil {
		t.Errorf("chat-completions fields do not belong in a Responses request: %v", got)
	}
	if got["temperature"] != nil {
		t.Errorf("a reasoning model gets no temperature unless one was chosen: %v", got["temperature"])
	}
	if got["instructions"] != "You are a helpful assistant.\n\n[memory] earlier: the user likes Go" {
		t.Errorf("instructions: %v", got["instructions"])
	}
	wire, _ := json.Marshal(got["input"])
	for _, want := range []string{
		`{"content":[{"text":"hi","type":"input_text"}],"role":"user"}`,
		`{"content":[{"text":"hello!","type":"output_text"}],"role":"assistant"}`,
		`{"content":[{"text":"[context] name: Ali","type":"input_text"}],"role":"user"}`,
		`{"image_url":"data:image/png;base64,AAAA","type":"input_image"}`,
		`{"arguments":"{\"file\":\"a.png\"}","call_id":"call_0","name":"view_image","type":"function_call"}`,
		`{"call_id":"call_0","output":"a cat","type":"function_call_output"}`,
	} {
		if !strings.Contains(string(wire), want) {
			t.Errorf("input misses %s:\n%s", want, wire)
		}
	}
	tl := got["tools"].([]any)[0].(map[string]any)
	if tl["type"] != "function" || tl["name"] != "web_search" || tl["parameters"] == nil || tl["strict"] != false || tl["function"] != nil {
		t.Errorf("a function tool is flat, with strict off: %v", tl)
	}

	if resp.Message.Content != "Let me check." || len(streamed) != 2 || streamed[1] != "Let me check." || resp.Message.ReasoningContent != "Thinking it over. " {
		t.Errorf("text %q (streamed %q) reasoning %q", resp.Message.Content, streamed, resp.Message.ReasoningContent)
	}
	if len(resp.Message.ToolCalls) != 1 || resp.Message.ToolCalls[0].ID != "call_1" || resp.Message.ToolCalls[0].Function.Name != "web_search" ||
		resp.Message.ToolCalls[0].Function.Arguments != `{"query": "go 1.26"}` {
		t.Errorf("tool calls: %+v", resp.Message.ToolCalls)
	}
	if resp.FinishReason != "tool_calls" || resp.Usage.PromptTokens != 9420 || resp.Usage.CachedTokens != 9000 || resp.Usage.CompletionTokens != 42 || resp.Usage.TotalTokens != 9462 || resp.Model != "gpt-5.2-2026" {
		t.Errorf("finish %q usage %+v model %q", resp.FinishReason, resp.Usage, resp.Model)
	}
}

func TestResponsesAPIWholeAnswerAndTheOtherShapes(t *testing.T) {
	var calls atomic.Int32
	var last map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		last = nil
		_ = json.Unmarshal(body, &last)
		w.Header().Set("Content-Type", "application/json")
		switch calls.Add(1) {
		case 1:
			fmt.Fprint(w, `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.2","output":[
				{"type":"reasoning","id":"rs_1","summary":[{"type":"summary_text","text":"hmm"}]},
				{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"سلام! ","annotations":[]},{"type":"output_text","text":"چطوری؟"}]},
				{"type":"function_call","id":"fc_1","call_id":"call_9","name":"list_files","arguments":""}],
				"usage":{"input_tokens":50,"output_tokens":7,"input_tokens_details":{"cached_tokens":10}}}`)
		case 2:
			fmt.Fprint(w, `{"status":"incomplete","incomplete_details":{"reason":"max_output_tokens"},"output":[{"type":"message","content":[{"type":"output_text","text":"long long"}]}]}`)
		case 3:
			fmt.Fprint(w, okBody())
		case 4:
			fmt.Fprint(w, `{"status":"failed","error":{"code":"rate_limit_exceeded","message":"slow down"}}`)
		case 5:
			fmt.Fprint(w, `{"status":"completed","output":[]}`)
		}
	}))
	defer srv.Close()
	c := responsesClient(srv.URL + "/v1")
	req := Request{Messages: []Message{{Role: "user", Content: "hi"}}}

	r, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if r.Message.Content != "سلام! چطوری؟" || r.Message.ReasoningContent != "hmm" || r.FinishReason != "tool_calls" || r.Usage.CachedTokens != 10 || r.Usage.TotalTokens != 57 {
		t.Errorf("whole answer: %+v finish %q usage %+v", r.Message, r.FinishReason, r.Usage)
	}
	if tc := r.Message.ToolCalls; len(tc) != 1 || tc[0].ID != "call_9" || tc[0].Function.Arguments != "{}" {
		t.Errorf("a call with no arguments has an empty object: %+v", tc)
	}
	if last["stream"] != nil && last["stream"] != false {
		t.Errorf("not streamed unless asked: %v", last["stream"])
	}
	if r, err = c.Complete(context.Background(), req); err != nil || r.FinishReason != "length" || r.Message.Content != "long long" {
		t.Errorf("cut off: %+v %v", r, err)
	}
	if r, err = c.Complete(context.Background(), req); err != nil || r.Message.Content != "hello" {
		t.Errorf("chat-shaped answer: %+v %v", r, err)
	}
	var api *APIError
	if _, err = c.Complete(context.Background(), Request{Messages: req.Messages, FailFast: true}); !errors.As(err, &api) || api.Status != http.StatusTooManyRequests || !strings.Contains(api.Body, "slow down") {
		t.Errorf("an error in a 200 keeps its kind: %v", err)
	}
	if _, err = c.Complete(context.Background(), Request{Messages: req.Messages, FailFast: true}); err == nil || !strings.Contains(err.Error(), "no output") {
		t.Errorf("an empty answer is an error: %v", err)
	}
}

func TestResponsesAPIStreamErrorsAndGatewayHabits(t *testing.T) {
	var mode atomic.Int32
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		bodies = append(bodies, b)
		switch mode.Load() {
		case 0:
			responsesSSE(w, `{"type":"response.created","response":{}}`, `{"type":"error","code":"server_error","message":"upstream exploded"}`)
		case 1:
			responsesSSE(w, `{"type":"response.output_text.delta","delta":"half an ans"}`, `{"type":"response.failed","response":{"error":{"code":"server_error","message":"cut"}}}`)
		case 2:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "event: response.completed\ndata: {\"response\":{\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"all at once\"}]}],\"usage\":{\"input_tokens\":3,\"output_tokens\":2}}}\n\n")
		case 3:
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\ndata: [DONE]\n\n")
		case 4:
			responsesSSE(w, `{"type":"response.output_text.delta","delta":"half"}`)
		case 5:
			if _, has := b["store"]; has {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":{"message":"Unknown parameter: 'store'."}}`)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"fine"}]}]}`)
		}
	}))
	defer srv.Close()
	c := responsesClient(srv.URL)
	req := func() Request {
		return Request{Messages: []Message{{Role: "user", Content: "hi"}}, OnDelta: func(string) {}, FailFast: true}
	}
	var api *APIError

	if _, err := c.Complete(context.Background(), req()); !errors.As(err, &api) || api.Status != http.StatusInternalServerError || !strings.Contains(api.Body, "upstream exploded") || errors.Is(err, ErrPartialStream) {
		t.Errorf("an error event before any text: %v", err)
	}
	mode.Store(1)
	if _, err := c.Complete(context.Background(), req()); !errors.Is(err, ErrPartialStream) {
		t.Errorf("an error after text is a partial stream: %v", err)
	}
	mode.Store(2)
	if r, err := c.Complete(context.Background(), req()); err != nil || r.Message.Content != "all at once" || r.Usage.TotalTokens != 5 {
		t.Errorf("event name on the event: line, final answer only: %+v %v", r, err)
	}
	mode.Store(3)
	if r, err := c.Complete(context.Background(), req()); err != nil || r.Message.Content != "ok" || r.FinishReason != "stop" {
		t.Errorf("[DONE]: %+v %v", r, err)
	}
	mode.Store(4)
	if _, err := c.Complete(context.Background(), req()); err == nil || !errors.Is(err, ErrPartialStream) {
		t.Errorf("a stream that stops before the end is an error: %v", err)
	}
	mode.Store(5)
	bodies = nil
	if r, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil || r.Message.Content != "fine" {
		t.Fatalf("store refused, learned, retried: %+v %v", r, err)
	}
	if len(bodies) != 2 || bodies[0]["store"] != false || bodies[1]["store"] != nil {
		t.Errorf("the second try leaves \"store\" out: %v", bodies)
	}
	bodies = nil
	if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "again"}}}); err != nil || len(bodies) != 1 || bodies[0]["store"] != nil {
		t.Errorf("and it is remembered: %d bodies, %v", len(bodies), err)
	}
}

func TestResponsesAPITemperatureAndEffort(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got = nil
		_ = json.Unmarshal(raw, &got)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
	}))
	defer srv.Close()
	req := Request{Messages: []Message{{Role: "user", Content: "hi"}}}

	c := responsesClient(srv.URL)
	c.Model = "gpt-4.1"
	c.Effort = EffortHigh
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got["temperature"] != 0.3 || got["reasoning"] != nil {
		t.Errorf("a non-reasoning model: temperature %v reasoning %v", got["temperature"], got["reasoning"])
	}

	c = responsesClient(srv.URL)
	c.Effort = EffortHigh
	if _, err := c.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if r, _ := got["reasoning"].(map[string]any); r["effort"] != "high" || got["temperature"] != nil {
		t.Errorf("a reasoning model: %v temperature %v", got["reasoning"], got["temperature"])
	}
	if p, _, _ := EffortWire(APIResponses, "gpt-5.2", EffortHigh); p != "reasoning.effort" {
		t.Errorf("the parameter's name: %q", p)
	}
}

func TestNoStreamAsksForTheWholeAnswer(t *testing.T) {
	for _, api := range []string{APIOpenAI, APIResponses, APIAnthropic} {
		var got map[string]any
		fail := false
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			got = nil
			_ = json.Unmarshal(raw, &got)
			if fail {
				http.Error(w, `{"error":{"message":"boom"}}`, http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch api {
			case APIResponses:
				fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"whole answer"}]}]}`)
			case APIAnthropic:
				fmt.Fprint(w, `{"model":"m","content":[{"type":"text","text":"whole answer"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":2}}`)
			default:
				fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"whole answer"},"finish_reason":"stop"}]}`)
			}
		}))
		c := fastClient(srv.URL + "/v1")
		c.API, c.NoStream = api, true
		var deltas []string
		req := Request{Messages: []Message{{Role: "user", Content: "hi"}}, OnDelta: func(s string) { deltas = append(deltas, s) }}
		resp, err := c.Complete(context.Background(), req)
		if err != nil || resp.Message.Content != "whole answer" {
			t.Fatalf("%s: %+v %v", api, resp, err)
		}
		if got["stream"] == true {
			t.Errorf("%s: asked for a stream although streaming is off", api)
		}
		if len(deltas) != 1 || deltas[0] != "whole answer" {
			t.Errorf("%s: the text arrives once, whole: %q", api, deltas)
		}
		fail, deltas = true, nil
		if _, err := c.Complete(context.Background(), req); err == nil || len(deltas) != 0 {
			t.Errorf("%s: an error brings no text: %v %q", api, err, deltas)
		}
		c.NoStream = false
		fail = false
		if _, err := c.Complete(context.Background(), req); err != nil || got["stream"] != true {
			t.Errorf("%s: with streaming on it is asked for: %v %v", api, got["stream"], err)
		}
		srv.Close()
	}
}

func TestIsUnavailable(t *testing.T) {
	var mode atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch mode.Load() {
		case 0:
			fmt.Fprint(w, `{"choices":[]}`)
		case 1:
			fmt.Fprint(w, `<html>not json</html>`)
		case 2:
			http.Error(w, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
		}
	}))
	c := fastClient(srv.URL)
	req := Request{Messages: []Message{{Role: "user", Content: "hi"}}, FailFast: true}
	if _, err := c.Complete(context.Background(), req); !IsUnavailable(err) || !strings.Contains(err.Error(), "no choices returned from llm") {
		t.Errorf("an answer with no choices: %v", err)
	}
	mode.Store(1)
	if _, err := c.Complete(context.Background(), req); !IsUnavailable(err) {
		t.Errorf("an unreadable answer: %v", err)
	}
	mode.Store(2)
	if _, err := c.Complete(context.Background(), req); IsUnavailable(err) {
		t.Errorf("a refusal of the request is not an outage: %v", err)
	}
	srv.Close()
	if _, err := c.Complete(context.Background(), req); !IsUnavailable(err) {
		t.Errorf("a refused connection: %v", err)
	}
	if !IsUnavailable(fmt.Errorf("%w: cut", ErrPartialStream)) || IsUnavailable(errors.New("something else")) || IsUnavailable(nil) {
		t.Error("partial streams count, other errors and nil do not")
	}
}

func TestResponsesGatewayThatAlwaysStreams(t *testing.T) {
	ok, _ := os.ReadFile("testdata/kie_responses_ok.sse")
	failed, _ := os.ReadFile("testdata/kie_responses_failed.sse")
	var body atomic.Value
	var asked []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		asked = append(asked, b["stream"] == true)
		w.Header().Set("Content-Type", "text/event-stream;charset=UTF-8")
		_, _ = w.Write(body.Load().([]byte))
	}))
	defer srv.Close()
	c := responsesClient(srv.URL + "/openai/v1")
	c.Model = "gpt-6.1-sol"
	req := Request{Messages: []Message{{Role: "user", Content: "Reply with exactly one word: pong"}}, MaxTokens: 20, FailFast: true}

	body.Store(ok)
	resp, err := c.Complete(context.Background(), req)
	if err != nil {
		t.Fatalf("a stream answering a request that did not ask for one: %v", err)
	}
	if resp.Message.Content != "pong" || resp.FinishReason != "stop" || resp.Usage.TotalTokens != 5 || resp.Model != "gpt-6.1-sol" {
		t.Errorf("whole: %q finish %q usage %+v model %q", resp.Message.Content, resp.FinishReason, resp.Usage, resp.Model)
	}
	if asked[0] {
		t.Error("the request did not ask for a stream")
	}

	var got []string
	req.OnDelta = func(s string) { got = append(got, s) }
	if resp, err = c.Complete(context.Background(), req); err != nil || resp.Message.Content != "pong" || len(got) == 0 || got[len(got)-1] != "pong" {
		t.Errorf("streamed: %+v %v %q", resp, err, got)
	}

	c.NoStream, got = true, nil
	if resp, err = c.Complete(context.Background(), req); err != nil || resp.Message.Content != "pong" || len(got) != 1 {
		t.Errorf("no stream: %+v %v %q", resp, err, got)
	}

	body.Store(failed)
	c.NoStream, req.OnDelta = false, nil
	var api *APIError
	if _, err = c.Complete(context.Background(), req); !errors.As(err, &api) || !strings.Contains(api.Body, "Service temporarily unavailable") || !api.Temporary() {
		t.Errorf("a failure inside the stream: %v", err)
	}

	body.Store(ok)
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write(ok)
	}))
	defer srv2.Close()
	c2 := responsesClient(srv2.URL)
	if resp, err = c2.Complete(context.Background(), Request{Messages: req.Messages, FailFast: true}); err != nil || resp.Message.Content != "pong" {
		t.Errorf("a stream under text/plain: %+v %v", resp, err)
	}
}

func TestAnyFormatReadsAStreamItWasNotAskedFor(t *testing.T) {
	for api, stream := range map[string]string{
		APIOpenAI:    "data: {\"choices\":[{\"delta\":{\"content\":\"hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n\n",
		APIAnthropic: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"m\",\"usage\":{\"input_tokens\":3}}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hello\"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, stream)
		}))
		c := fastClient(srv.URL + "/v1")
		c.API = api
		resp, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}, FailFast: true})
		srv.Close()
		if err != nil || resp.Message.Content != "hello" {
			t.Errorf("%s: %+v %v", api, resp, err)
		}
	}
}

func TestLiveResponses(t *testing.T) {
	key := os.Getenv("WAZE_TEST_RESPONSES_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_RESPONSES_KEY is not set")
	}
	base, model := os.Getenv("WAZE_TEST_RESPONSES_URL"), os.Getenv("WAZE_TEST_RESPONSES_MODEL")
	if base == "" {
		base = "https://api.kie.ai/openai/v1"
	}
	if model == "" {
		model = "gpt-6.1-sol"
	}
	c := NewClient(base, key, model)
	c.API = APIResponses
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	if ids, err := c.Models(ctx); err != nil || len(ids) == 0 {
		t.Errorf("the model list: %v %v", ids, err)
	} else {
		t.Logf("models: %v", ids)
	}
	resp, err := c.Complete(ctx, Request{Messages: []Message{{Role: "user", Content: "Reply with exactly one word: pong"}}, MaxTokens: 50})
	if err != nil || !strings.Contains(strings.ToLower(resp.Message.Content), "pong") {
		t.Fatalf("a whole answer: %+v %v", resp, err)
	}
	t.Logf("whole: %q usage %+v finish %q", resp.Message.Content, resp.Usage, resp.FinishReason)

	var deltas []string
	resp, err = c.Complete(ctx, Request{Messages: []Message{{Role: "system", Content: "Answer in one short sentence."}, {Role: "user", Content: "Say hello in Persian."}},
		OnDelta: func(s string) { deltas = append(deltas, s) }})
	if err != nil || resp.Message.Content == "" || len(deltas) == 0 {
		t.Fatalf("a streamed answer: %+v %v (%d deltas)", resp, err, len(deltas))
	}
	t.Logf("streamed: %q (%d deltas) usage %+v", resp.Message.Content, len(deltas), resp.Usage)

	tools := []Tool{{Type: "function", Function: ToolDef{Name: "get_time", Description: "The current time in a city",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"city": map[string]any{"type": "string"}}, "required": []string{"city"}}}}}
	msgs := []Message{{Role: "system", Content: "You are a helpful assistant. Use tools when they help."}, {Role: "user", Content: "What time is it in Tehran? Use the tool."}}
	resp, err = c.Complete(ctx, Request{Messages: msgs, Tools: tools, OnDelta: func(string) {}})
	if err != nil || len(resp.Message.ToolCalls) == 0 {
		t.Fatalf("a tool call: %+v %v", resp, err)
	}
	call := resp.Message.ToolCalls[0]
	t.Logf("tool call: %+v finish %q", call, resp.FinishReason)
	msgs = append(msgs, resp.Message, Message{Role: "tool", ToolCallID: call.ID, Content: "It is 14:30 in Tehran."})
	resp, err = c.Complete(ctx, Request{Messages: msgs, Tools: tools})
	if err != nil || !strings.Contains(resp.Message.Content, "14:30") {
		t.Fatalf("the answer after the tool: %+v %v", resp, err)
	}
	t.Logf("after the tool: %q", resp.Message.Content)
}

func TestGatewayErrorEnvelope(t *testing.T) {
	cases := []struct {
		body   string
		status int
		temp   bool
		failov bool
	}{
		{`{"code":402,"msg":"Credits insufficient : Your current balance isn’t enough","data":null}`, 402, false, true},
		{`{"code":401,"msg":"The api key does not exist.","data":null}`, 401, false, true},
		{`{"code":429,"msg":"Your call frequency is too high.","data":null}`, 429, true, true},
		{`{"code":455,"msg":"Service under maintenance","data":null}`, 503, true, true},
		{`{"code":500,"msg":"Server exception","data":null}`, 500, true, true},
	}
	for _, api := range []string{APIOpenAI, APIResponses, APIAnthropic} {
		for _, tc := range cases {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, tc.body)
			}))
			c := fastClient(srv.URL + "/v1")
			c.API = api
			_, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}, FailFast: true})
			srv.Close()
			var ae *APIError
			if !errors.As(err, &ae) || ae.Status != tc.status || IsUnavailable(err) {
				t.Errorf("%s %s: %v", api, tc.body[:14], err)
				continue
			}
			if ae.Temporary() != tc.temp {
				t.Errorf("%s status %d: temporary %v, want %v", api, tc.status, ae.Temporary(), tc.temp)
			}
		}
	}

	for _, body := range []string{`{"choices":[{"message":{"role":"assistant","content":"hello"}}],"code":200,"msg":"success"}`, `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`} {
		if envelopeError([]byte(body)) != nil {
			t.Errorf("not an envelope: %s", body)
		}
	}
}

func TestWholeAnswersSayStreamFalse(t *testing.T) {
	for _, api := range []string{APIResponses, APIAnthropic} {
		var got map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &got)
			w.Header().Set("Content-Type", "application/json")
			if api == APIAnthropic {
				fmt.Fprint(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
			} else {
				fmt.Fprint(w, `{"status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"ok"}]}]}`)
			}
		}))
		c := fastClient(srv.URL)
		c.API = api
		if _, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "hi"}}}); err != nil {
			t.Fatal(err)
		}
		if v, has := got["stream"]; !has || v != false {
			t.Errorf("%s: a whole answer says stream:false, got %v (present %v)", api, v, has)
		}
		srv.Close()
	}
}
