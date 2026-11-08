package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestMessageWireFormat(t *testing.T) {
	cases := []struct {
		name string
		msg  Message
		want string
	}{
		{"tool with empty result keeps content", Message{Role: "tool", ToolCallID: "c1", Name: "x"}, `"content":""`},
		{"user", Message{Role: "user", Content: "hi"}, `"content":"hi"`},
		{"assistant tool call has null content", Message{Role: "assistant", ToolCalls: []ToolCall{{ID: "1", Type: "function"}}}, `"content":null`},
	}
	for _, c := range cases {
		b, err := json.Marshal(c.msg)
		if err != nil || !strings.Contains(string(b), c.want) {
			t.Errorf("%s: %s (err %v), want %s", c.name, b, err, c.want)
		}
	}
	b, _ := json.Marshal(Message{Role: "assistant", Content: "a", ReasoningContent: "secret thoughts", Reasoning: "more"})
	if strings.Contains(string(b), "thoughts") || strings.Contains(string(b), "reasoning") {
		t.Errorf("reasoning must never be sent back: %s", b)
	}
	var m Message
	_ = json.Unmarshal([]byte(`{"role":"assistant","content":"x","reasoning":"think"}`), &m)
	if m.Thought() != "think" {
		t.Errorf("Thought() = %q", m.Thought())
	}
}

func fastClient(url string) *Client {
	c := NewClient(url, "k", "m")
	c.sleep = func(context.Context, time.Duration) error { return nil }
	return c
}

func okBody() string {
	return `{"choices":[{"message":{"role":"assistant","content":"hello"}}]}`
}

func TestChatRetriesTransientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer k" {
			t.Error("missing auth header")
		}
		if calls.Add(1) < 3 {
			http.Error(w, "overloaded", http.StatusServiceUnavailable)
			return
		}
		w.Write([]byte(okBody()))
	}))
	defer srv.Close()

	msg, err := fastClient(srv.URL+"/").Chat(context.Background(), []Message{{Role: "user", Content: "x"}}, nil)
	if err != nil || msg.Content != "hello" || calls.Load() != 3 {
		t.Fatalf("msg=%v err=%v calls=%d", msg, err, calls.Load())
	}
}

func TestChatDoesNotRetryClientErrors(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"error":"bad key"}`, http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := fastClient(srv.URL).Chat(context.Background(), nil, nil)
	var api *APIError
	if !errors.As(err, &api) || api.Status != 401 || api.Temporary() || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestChatGivesUpAfterMaxRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, "boom", http.StatusBadGateway)
	}))
	defer srv.Close()
	c := fastClient(srv.URL)
	c.MaxRetries = 1
	if _, err := c.Chat(context.Background(), nil, nil); err == nil || calls.Load() != 2 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestChatRespectsContext(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // hang until the test finishes
	}))
	defer srv.Close()
	defer close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := NewClient(srv.URL, "", "m").Chat(ctx, nil, nil); err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("expected quick context error, got %v after %v", err, time.Since(start))
	}
}

func TestProviderErrorObjectAndEmptyChoices(t *testing.T) {
	bodies := []string{`{"error":{"message":"quota"}}`, `{"choices":[]}`}
	for _, b := range bodies {
		b := b
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(b)) }))
		c := fastClient(srv.URL)
		c.MaxRetries = 0
		if _, err := c.Chat(context.Background(), nil, nil); err == nil {
			t.Errorf("body %s should error", b)
		}
		srv.Close()
	}
}

func TestMultimodalWireFormat(t *testing.T) {
	m := Message{Role: "user", Parts: []Part{TextPart("what is this?"), ImagePart("data:image/png;base64,AAAA")}}
	b, _ := json.Marshal(m)
	want := `{"role":"user","content":[{"text":"what is this?","type":"text"},{"image_url":{"url":"data:image/png;base64,AAAA"},"type":"image_url"}]}`
	if string(b) != want {
		t.Fatalf("got  %s\nwant %s", b, want)
	}
	var back Message
	if err := json.Unmarshal(b, &back); err != nil || len(back.Parts) != 2 || back.Content != "what is this?" || back.Parts[1].ImageURL != "data:image/png;base64,AAAA" {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if back.Text() != "what is this?" {
		t.Error("Text()")
	}
	var plain Message
	json.Unmarshal([]byte(`{"role":"assistant","content":null,"tool_calls":[{"id":"1","type":"function","function":{"name":"x","arguments":"{}"}}]}`), &plain)
	if len(plain.ToolCalls) != 1 || plain.Content != "" {
		t.Errorf("null content: %+v", plain)
	}
}

func sse(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, c := range chunks {
		fmt.Fprintf(w, "data: %s\n\n", c)
		w.(http.Flusher).Flush()
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestStreamingTextAndToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if !req.Stream || req.StreamOptions["include_usage"] != true {
			t.Errorf("stream flags: %+v", req)
		}
		sse(w,
			`{"model":"m-1","choices":[{"delta":{"reasoning_content":"thinking "}}]}`,
			`{"choices":[{"delta":{"content":"سلام"}}]}`,
			`: keep-alive`,
			`{"choices":[{"delta":{"content":" دنیا"}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_","arguments":""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"search","arguments":"{\"query\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_2","function":{"name":"list_files","arguments":"{}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"go\"}"}}]}}],"usage":null}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
			`{"choices":[],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18}}`,
		)
	}))
	defer srv.Close()

	var deltas []string
	resp, err := NewClient(srv.URL, "k", "m").Complete(context.Background(), Request{OnDelta: func(s string) { deltas = append(deltas, s) }})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(deltas, "|") != "سلام|سلام دنیا" {
		t.Errorf("deltas: %q", deltas)
	}
	m := resp.Message
	if m.Content != "سلام دنیا" || m.Thought() != "thinking " || resp.Model != "m-1" || resp.FinishReason != "tool_calls" || resp.Usage.TotalTokens != 18 {
		t.Errorf("response: %+v", resp)
	}
	if len(m.ToolCalls) != 2 || m.ToolCalls[0].Function.Name != "web_search" || m.ToolCalls[0].Function.Arguments != `{"query":"go"}` ||
		m.ToolCalls[0].ID != "call_1" || m.ToolCalls[1].Function.Name != "list_files" || m.ToolCalls[1].Type != "function" {
		t.Errorf("tool calls: %+v", m.ToolCalls)
	}
}

func TestStreamFallbacks(t *testing.T) {
	// Server ignores stream=true and answers with plain JSON.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(okBody())) }))
	defer srv.Close()
	var got string
	resp, err := NewClient(srv.URL, "k", "m").Complete(context.Background(), Request{OnDelta: func(s string) { got = s }})
	if err != nil || resp.Message.Content != "hello" || got != "hello" {
		t.Fatalf("json fallback: %v %q", err, got)
	}

	// Gateway rejects stream_options: retried once without it.
	var calls atomic.Int32
	strict := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		var req ChatRequest
		json.NewDecoder(r.Body).Decode(&req)
		if req.StreamOptions != nil {
			http.Error(w, `{"error":"unknown field stream_options"}`, 400)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`)
	}))
	defer strict.Close()
	resp, err = fastClient(strict.URL).Complete(context.Background(), Request{OnDelta: func(string) {}})
	if err != nil || resp.Message.Content != "ok" || calls.Load() != 2 {
		t.Fatalf("stream_options fallback: %v calls=%d", err, calls.Load())
	}

	// An error after output started is not retried.
	var n atomic.Int32
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		sse(w, `{"choices":[{"delta":{"content":"part"}}]}`, `{"error":{"message":"overloaded"}}`)
	}))
	defer broken.Close()
	_, err = fastClient(broken.URL).Complete(context.Background(), Request{OnDelta: func(string) {}})
	if !errors.Is(err, ErrPartialStream) || n.Load() != 1 || IsRetryable(err) {
		t.Fatalf("partial stream: %v attempts=%d", err, n.Load())
	}

	// An error before any output is retried.
	var m atomic.Int32
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.Add(1) == 1 {
			sse(w, `{"error":{"message":"overloaded"}}`)
			return
		}
		sse(w, `{"choices":[{"delta":{"content":"fine"}}]}`)
	}))
	defer flaky.Close()
	resp, err = fastClient(flaky.URL).Complete(context.Background(), Request{OnDelta: func(string) {}})
	if err != nil || resp.Message.Content != "fine" || m.Load() != 2 {
		t.Fatalf("early stream error must be retried: %v attempts=%d", err, m.Load())
	}
}

func TestFailFastAndLongRetryAfter(t *testing.T) {
	var calls atomic.Int32
	status, retryAfter := atomic.Int32{}, atomic.Value{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if v, _ := retryAfter.Load().(string); v != "" {
			w.Header().Set("Retry-After", v)
		}
		http.Error(w, "busy", int(status.Load()))
	}))
	defer srv.Close()
	c := fastClient(srv.URL)
	ask := Request{Messages: []Message{{Role: "user", Content: "hi"}}}

	status.Store(503)
	retryAfter.Store("")
	_, err := c.Complete(context.Background(), Request{Messages: ask.Messages, FailFast: true})
	if calls.Load() != 1 || !IsRetryable(err) {
		t.Errorf("fail-fast returns at once: %d calls, %v", calls.Load(), err)
	}

	calls.Store(0)
	status.Store(429)
	retryAfter.Store("3600")
	_, err = c.Complete(context.Background(), ask)
	var api *APIError
	if calls.Load() != 1 || !errors.As(err, &api) || api.RetryAfter != time.Hour {
		t.Errorf("an hour-long Retry-After is not waited for: %d calls, %v", calls.Load(), err)
	}

	calls.Store(0)
	retryAfter.Store("2")
	_, _ = c.Complete(context.Background(), ask)
	if calls.Load() != int32(c.MaxRetries)+1 {
		t.Errorf("a short Retry-After is honoured: %d calls", calls.Load())
	}
}

func TestPolicyViolations(t *testing.T) {
	for _, s := range []string{"Request blocked: Prohibited Use policy", "your input contains sensitive words",
		"The response was filtered due to the prompt triggering Azure OpenAI's content management policy", "Content Exists Risk",
		"blocked by the safety filter", "violates our Usage Policies", "检测到敏感词"} {
		if !IsPolicyViolationText(s) {
			t.Errorf("%q is a refusal", s)
		}
	}
	for _, s := range []string{"rate limit exceeded", "model not found", "invalid api key", ""} {
		if IsPolicyViolationText(s) {
			t.Errorf("%q is not a refusal", s)
		}
	}
	// An error object (status 502 inside the client) is refused once, not retried.
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Write([]byte(`{"error":{"message":"Your request was blocked: it violates the Prohibited Use policy"}}`))
	}))
	defer srv.Close()
	_, err := fastClient(srv.URL).Chat(context.Background(), nil, nil)
	if !IsPolicyViolation(err) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
	// An empty answer stopped by the filter is a refusal too.
	filteredSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":""},"finish_reason":"content_filter"}]}`))
	}))
	defer filteredSrv.Close()
	if _, err := fastClient(filteredSrv.URL).Chat(context.Background(), nil, nil); !errors.Is(err, ErrPolicyViolation) {
		t.Fatalf("content_filter: %v", err)
	}
	if IsPolicyViolation(nil) || IsPolicyViolation(errors.New("timeout")) {
		t.Error("ordinary errors")
	}
}

func TestStreamsMayBeLongButNotStalled(t *testing.T) {
	chunk := func(w http.ResponseWriter, s string) {
		fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", s)
		w.(http.Flusher).Flush()
	}
	flowing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 8; i++ { // 8 × 100 ms: longer than the idle limit in total
			chunk(w, "a")
			time.Sleep(100 * time.Millisecond)
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer flowing.Close()
	c := fastClient(flowing.URL)
	c.StreamIdle = 300 * time.Millisecond
	resp, err := c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}, OnDelta: func(string) {}})
	if err != nil || resp.Message.Content != "aaaaaaaa" {
		t.Fatalf("a flowing stream must finish: %+v %v", resp, err)
	}

	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunk(w, "a")
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer stalled.Close()
	c = fastClient(stalled.URL)
	c.StreamIdle, c.MaxRetries = 300*time.Millisecond, 0
	start := time.Now()
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}, OnDelta: func(string) {}})
	if err == nil || time.Since(start) > 2*time.Second || !strings.Contains(err.Error(), "sent nothing") {
		t.Fatalf("a stalled stream must fail fast: %v after %s", err, time.Since(start))
	}

	// A provider that takes the request and never answers is given up on as
	// soon as a stalled stream would be (not after the 10-minute header wait),
	// as a transport error the provider list fails over on.
	silent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer silent.Close()
	c = fastClient(silent.URL)
	c.StreamIdle, c.MaxRetries = 300*time.Millisecond, 0
	start = time.Now()
	_, err = c.Complete(context.Background(), Request{Messages: []Message{{Role: "user", Content: "x"}}, OnDelta: func(string) {}})
	if err == nil || time.Since(start) > 2*time.Second || !strings.Contains(err.Error(), "no answer within 300ms") || !errors.Is(err, errTransport) {
		t.Fatalf("a silent provider must fail fast: %v after %s", err, time.Since(start))
	}
}

func TestContextOverflow(t *testing.T) {
	for _, body := range []string{"This model's maximum context length is 128000 tokens", `{"error":{"code":"context_length_exceeded"}}`,
		"prompt is too long: 250000 tokens > 200000 maximum", "Input is too long for requested model"} {
		if !IsContextOverflow(&APIError{Status: 400, Body: body}) {
			t.Errorf("%q", body)
		}
	}
	if IsContextOverflow(&APIError{Status: 400, Body: "invalid model"}) || IsContextOverflow(errors.New("context length")) {
		t.Error("false positive")
	}
	if !IsContextOverflow(&APIError{Status: 413, Body: ""}) {
		t.Error("413")
	}
}

func TestUsageReadsCachedTokens(t *testing.T) {
	for raw, want := range map[string]Usage{
		`{"prompt_tokens":1000,"completion_tokens":50,"total_tokens":1050,"prompt_tokens_details":{"cached_tokens":800}}`: {1000, 50, 1050, 800},
		`{"prompt_tokens":1000,"completion_tokens":50,"prompt_cache_hit_tokens":600,"prompt_cache_miss_tokens":400}`:      {1000, 50, 0, 600},
		`{"prompt_tokens":10,"completion_tokens":5,"cached_tokens":99}`:                                                   {10, 5, 0, 10},
		`{"prompt_tokens":10,"completion_tokens":5}`:                                                                      {10, 5, 0, 0},
	} {
		var u Usage
		if err := json.Unmarshal([]byte(raw), &u); err != nil || u != want {
			t.Errorf("%s: %+v %v", raw, u, err)
		}
	}
	// Cached prompt tokens count a tenth; new ones and the answer in full.
	if b := (Usage{PromptTokens: 1000, CachedTokens: 800, CompletionTokens: 50}).Billable(); b != 200+80+50 {
		t.Errorf("billable: %d", b)
	}
	if s := (Usage{PromptTokens: 1, CachedTokens: 1}).Add(Usage{PromptTokens: 2, CompletionTokens: 3, TotalTokens: 5}); s != (Usage{3, 3, 5, 1}) {
		t.Errorf("add: %+v", s)
	}
}

// Some servers send every call whole, all with index 0: a new id is a new
// call. A stream that stops in the middle of a call (no finish, no [DONE])
// is reported as cut, so the call is not run.
func TestStreamCallsWithoutIndexAndCutStreams(t *testing.T) {
	cut := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cut {
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write_file","arguments":"{\"content\":\"ha"}}]}}]}`+"\n\n")
			return
		}
		sse(w,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"a\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c2","type":"function","function":{"name":"web_fetch","arguments":"{\"url\":\"b\"}"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k", "m")
	resp, err := c.Complete(context.Background(), Request{OnDelta: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	calls := resp.Message.ToolCalls
	if len(calls) != 2 || calls[0].Function.Name != "web_search" || calls[1].Function.Name != "web_fetch" ||
		calls[1].Function.Arguments != `{"url":"b"}` || calls[1].ID != "c2" {
		t.Errorf("two calls: %+v", calls)
	}
	cut = true
	resp, err = c.Complete(context.Background(), Request{OnDelta: func(string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.FinishReason != "length" {
		t.Errorf("a stream cut in a call: finish %q", resp.FinishReason)
	}
}
