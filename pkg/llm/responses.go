package llm

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

func CleanBaseURL(raw string) (base, api string) {
	base = strings.TrimSpace(raw)
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return base, ""
	}
	path := strings.TrimRight(u.Path, "/")
	for _, e := range []struct{ suffix, api string }{
		{"/responses", APIResponses},
		{"/chat/completions", APIOpenAI},
		{"/messages", APIAnthropic},
	} {
		if strings.HasSuffix(path, e.suffix) {
			u.Path = strings.TrimSuffix(path, e.suffix)
			u.RawPath = ""
			return strings.TrimRight(u.String(), "/"), e.api
		}
	}
	return base, ""
}

func (c *Client) responsesBody(req Request) map[string]any {
	var (
		instructions []string
		input        []map[string]any
	)
	leading := true
	callN := 0
	callID := func(id string) string {
		if id = strings.TrimSpace(id); id != "" {
			return id
		}
		callN++
		return "call_" + strconv.Itoa(callN)
	}
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if leading {
				if t := strings.TrimSpace(m.Text()); t != "" {
					instructions = append(instructions, t)
				}
				continue
			}
			input = append(input, responsesUser(Message{Role: "user", Content: m.Text()}))
		case "assistant":
			leading = false
			if t := m.Text(); strings.TrimSpace(t) != "" {
				input = append(input, map[string]any{"role": "assistant", "content": []map[string]any{{"type": "output_text", "text": t}}})
			}
			for _, tc := range m.ToolCalls {
				args := strings.TrimSpace(tc.Function.Arguments)
				if args == "" {
					args = "{}"
				}
				input = append(input, map[string]any{"type": "function_call", "call_id": callID(tc.ID), "name": tc.Function.Name, "arguments": args})
			}
		case "tool":
			leading = false
			out := m.Content
			if strings.TrimSpace(out) == "" {
				out = "(no output)"
			}
			input = append(input, map[string]any{"type": "function_call_output", "call_id": callID(m.ToolCallID), "output": out})
		default:
			leading = false
			input = append(input, responsesUser(m))
		}
	}
	if len(input) == 0 {
		input = append(input, responsesUser(Message{Role: "user", Content: "(continuing)"}))
	}

	body := map[string]any{"model": c.Model, "input": input}
	if len(instructions) > 0 {
		body["instructions"] = strings.Join(instructions, "\n\n")
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Function.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}

			tools = append(tools, map[string]any{"type": "function", "name": t.Function.Name, "description": t.Function.Description,
				"parameters": schema, "strict": false})
		}
		body["tools"] = tools
	}
	if req.MaxTokens > 0 {
		body["max_output_tokens"] = req.MaxTokens
	}
	if t := c.Temperature; !c.noTemperature.Load() && (c.TemperatureSet || !reasoningModel(c.Model) && !req.OwnTemperature) {
		body["temperature"] = t
	}
	if e := c.wireEffort(req); e != "" {
		body["reasoning"] = map[string]any{"effort": e}
	}
	if !c.noStore.Load() {
		body["store"] = false
	}

	body["stream"] = req.OnDelta != nil
	return body
}

func responsesUser(m Message) map[string]any {
	if len(m.Parts) == 0 {
		return map[string]any{"role": "user", "content": []map[string]any{{"type": "input_text", "text": nonEmpty(m.Content)}}}
	}
	var parts []map[string]any
	for _, p := range m.Parts {
		switch {
		case p.Type == "image_url":
			parts = append(parts, map[string]any{"type": "input_image", "image_url": p.ImageURL})
		case strings.TrimSpace(p.Text) != "":
			parts = append(parts, map[string]any{"type": "input_text", "text": p.Text})
		}
	}
	if len(parts) == 0 {
		parts = append(parts, map[string]any{"type": "input_text", "text": "(empty)"})
	}
	return map[string]any{"role": "user", "content": parts}
}

func nonEmpty(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(empty)"
	}
	return s
}

func (c *Client) learnResponses(body string) bool {
	b := strings.ToLower(body)
	switch {
	case strings.Contains(b, "temperature") && !c.noTemperature.Load() &&
		(strings.Contains(b, "unsupported") || strings.Contains(b, "not support") || strings.Contains(b, "only the default") || strings.Contains(b, "unknown")):
		c.noTemperature.Store(true)
		return true
	case strings.Contains(b, "store") && !c.noStore.Load() &&
		(strings.Contains(b, "unsupported") || strings.Contains(b, "not support") || strings.Contains(b, "unknown") || strings.Contains(b, "unrecognized")):
		c.noStore.Store(true)
		return true
	}
	return false
}

type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	TotalTokens        int `json:"total_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u responsesUsage) usage() Usage {
	total := u.TotalTokens
	if total == 0 {
		total = u.InputTokens + u.OutputTokens
	}
	return Usage{PromptTokens: u.InputTokens, CompletionTokens: u.OutputTokens, TotalTokens: total, CachedTokens: u.InputTokensDetails.CachedTokens}
}

type responsesError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
	Type    string `json:"type"`
}

func (e responsesError) api() *APIError {
	status := http.StatusBadGateway
	switch low := strings.ToLower(e.Code + " " + e.Type); {
	case strings.Contains(low, "rate_limit"), strings.Contains(low, "too_many"):
		status = http.StatusTooManyRequests
	case strings.Contains(low, "quota"), strings.Contains(low, "billing"), strings.Contains(low, "credit"):
		status = http.StatusPaymentRequired
	case strings.Contains(low, "overload"), strings.Contains(low, "unavailable"), strings.Contains(low, "capacity"):
		status = http.StatusServiceUnavailable
	case strings.Contains(low, "server_error"), strings.Contains(low, "internal"):
		status = http.StatusInternalServerError
	case strings.Contains(low, "auth"), strings.Contains(low, "invalid_api_key"):
		status = http.StatusUnauthorized
	case strings.Contains(low, "invalid"), strings.Contains(low, "context_length"), strings.Contains(low, "too_long"):
		status = http.StatusBadRequest
	}
	body := strings.TrimSpace(e.Code + ": " + e.Message)
	return &APIError{Status: status, Body: strings.Trim(body, ": ")}
}

type responsesItem struct {
	Type      string `json:"type"`
	ID        string `json:"id"`
	CallID    string `json:"call_id"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
	Content   []struct {
		Type    string `json:"type"`
		Text    string `json:"text"`
		Refusal string `json:"refusal"`
	} `json:"content"`
	Summary []struct {
		Text string `json:"text"`
	} `json:"summary"`
}

type responsesResult struct {
	Model             string          `json:"model"`
	Status            string          `json:"status"`
	Output            []responsesItem `json:"output"`
	Usage             *responsesUsage `json:"usage"`
	Error             *responsesError `json:"error"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
}

func (r responsesResult) finish(hasCalls bool) string {
	if r.IncompleteDetails != nil {
		switch r.IncompleteDetails.Reason {
		case "content_filter":
			return "content_filter"
		default:
			return "length"
		}
	}
	if hasCalls {
		return "tool_calls"
	}
	return "stop"
}

func (r responsesResult) message() Message {
	msg := Message{Role: "assistant"}
	var text, thought strings.Builder
	for _, it := range r.Output {
		switch it.Type {
		case "message":
			for _, c := range it.Content {
				switch c.Type {
				case "output_text", "text":
					text.WriteString(c.Text)
				case "refusal":
					text.WriteString(c.Refusal + c.Text)
				}
			}
		case "reasoning":
			for _, s := range it.Summary {
				thought.WriteString(s.Text)
			}
		case "function_call":
			msg.ToolCalls = append(msg.ToolCalls, ToolCall{ID: firstNonEmpty(it.CallID, it.ID), Type: "function",
				Function: FunctionCall{Name: it.Name, Arguments: orEmptyObject(it.Arguments)}})
		}
	}
	msg.Content, msg.ReasoningContent = text.String(), thought.String()
	return msg
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func orEmptyObject(args string) string {
	if strings.TrimSpace(args) == "" || args == "null" {
		return "{}"
	}
	return args
}

func readResponses(resp *http.Response, onDelta func(string), idle time.Duration) (*Response, time.Duration, error) {

	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
		body := newIdleBody(resp.Body, idle)
		defer body.Close()
		out, err := readResponsesStream(body, onDelta)
		return out, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: read response body: %w", errTransport, err)
	}
	if t := bytes.TrimSpace(data); bytes.HasPrefix(t, []byte("event:")) || bytes.HasPrefix(t, []byte("data:")) {
		out, err := readResponsesStream(bytes.NewReader(data), onDelta)
		return out, 0, err
	}
	if ae := envelopeError(data); ae != nil {
		return nil, 0, ae
	}
	var raw struct {
		responsesResult
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, 0, fmt.Errorf("%w: unmarshal response: %w (body: %s)", ErrBadAnswer, err, snippet(data, 200))
	}
	if raw.Error != nil && (raw.Error.Message != "" || raw.Error.Code != "") {
		return nil, 0, raw.Error.api()
	}
	if raw.Status == "failed" || raw.Status == "cancelled" {
		return nil, 0, &APIError{Status: http.StatusBadGateway, Body: "the response " + raw.Status}
	}
	if len(raw.Output) == 0 && len(raw.Choices) > 0 {
		var cc ChatResponse
		if json.Unmarshal(data, &cc) == nil && len(cc.Choices) > 0 {
			r := &Response{Message: cc.Choices[0].Message, Usage: cc.Usage, Model: cc.Model, FinishReason: cc.Choices[0].FinishReason}
			if onDelta != nil && r.Message.Content != "" && !looksLikeGatewayError(r.Message.Content) {
				onDelta(r.Message.Content)
			}
			return r, 0, nil
		}
	}
	if len(raw.Output) == 0 {
		return nil, 0, fmt.Errorf("%w: no output returned from llm (body: %s)", ErrBadAnswer, snippet(data, 200))
	}
	msg := raw.message()
	r := &Response{Message: msg, Model: raw.Model, FinishReason: raw.finish(len(msg.ToolCalls) > 0)}
	if raw.Usage != nil {
		r.Usage = raw.Usage.usage()
	}
	if onDelta != nil && msg.Content != "" && !looksLikeGatewayError(msg.Content) {
		onDelta(msg.Content)
	}
	return r, 0, nil
}

func readResponsesStream(body io.Reader, onDelta func(string)) (*Response, error) {
	type event struct {
		Type        string          `json:"type"`
		Delta       string          `json:"delta"`
		Arguments   string          `json:"arguments"`
		OutputIndex int             `json:"output_index"`
		Item        responsesItem   `json:"item"`
		Response    responsesResult `json:"response"`
		Message     string          `json:"message"`
		Code        string          `json:"code"`
		Error       *responsesError `json:"error"`
	}
	var (
		out       = &Response{Message: Message{Role: "assistant"}}
		text      strings.Builder
		thought   strings.Builder
		calls     = map[int]*ToolCall{}
		started   bool
		gotData   bool
		completed bool
		final     *responsesResult
	)
	fail := func(err error) (*Response, error) {
		if started {
			return nil, fmt.Errorf("%w: %w", ErrPartialStream, err)
		}
		return nil, err
	}
	call := func(idx int, it responsesItem) *ToolCall {
		tc := calls[idx]
		if tc == nil {
			tc = &ToolCall{Type: "function"}
			calls[idx] = tc
		}
		if id := firstNonEmpty(it.CallID, it.ID); id != "" {
			tc.ID = id
		}
		if it.Name != "" {
			tc.Function.Name = it.Name
		}
		return tc
	}
	gate := &deltaGate{on: onDelta}
	sc := bufio.NewScanner(capped(body))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	argBytes := 0
	name := ""
	for sc.Scan() && !completed {
		if text.Len()+thought.Len()+argBytes > maxStreamText {
			return fail(ErrTooLarge)
		}
		line := strings.TrimSpace(sc.Text())
		if rest, ok := strings.CutPrefix(line, "event:"); ok {
			name = strings.TrimSpace(rest)
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			completed = true
			break
		}
		var ev event
		if json.Unmarshal([]byte(data), &ev) != nil {
			continue
		}
		gotData = true
		typ := ev.Type
		if typ == "" {
			typ = name
		}
		switch typ {
		case "error", "response.failed":
			e := ev.Error
			if typ == "response.failed" {
				e = ev.Response.Error
			}
			if e == nil {
				e = &responsesError{Message: ev.Message, Code: ev.Code}
			}
			if e.Message == "" && e.Code == "" {
				e.Message = "stream error"
			}
			return fail(e.api())
		case "response.created", "response.in_progress":
			if ev.Response.Model != "" {
				out.Model = ev.Response.Model
			}
		case "response.output_item.added":
			if ev.Item.Type == "function_call" {
				tc := call(ev.OutputIndex, ev.Item)
				tc.Function.Arguments += ev.Item.Arguments
				argBytes += len(ev.Item.Arguments)
			}
		case "response.function_call_arguments.delta":
			if tc := calls[ev.OutputIndex]; tc != nil {
				tc.Function.Arguments += ev.Delta
				argBytes += len(ev.Delta)
			}
		case "response.function_call_arguments.done":
			if tc := calls[ev.OutputIndex]; tc != nil && ev.Arguments != "" {
				tc.Function.Arguments = ev.Arguments
			}
		case "response.output_item.done":
			if ev.Item.Type == "function_call" {
				tc := call(ev.OutputIndex, ev.Item)
				if ev.Item.Arguments != "" {
					tc.Function.Arguments = ev.Item.Arguments
				}
			}
		case "response.output_text.delta", "response.refusal.delta":
			text.WriteString(ev.Delta)
			if ev.Delta != "" {
				started = true
				gate.push(text.String())
			}
		case "response.reasoning_summary_text.delta", "response.reasoning_text.delta":
			thought.WriteString(ev.Delta)
		case "response.completed", "response.incomplete":
			completed = true
			r := ev.Response
			final = &r
		}
	}
	if err := sc.Err(); err != nil && !completed {
		return fail(fmt.Errorf("%w: %w", errTransport, err))
	}
	if !gotData {
		return nil, fmt.Errorf("%w: empty stream", errTransport)
	}
	if !completed {
		return fail(fmt.Errorf("%w: the stream ended before the answer did", errTransport))
	}

	hasCalls := len(calls) > 0
	if final != nil {
		if final.Model != "" {
			out.Model = final.Model
		}
		if final.Usage != nil {
			out.Usage = final.Usage.usage()
		}
		if text.Len() == 0 && len(calls) == 0 {
			m := final.message()
			text.WriteString(m.Content)
			thought.WriteString(m.ReasoningContent)
			if m.Content != "" {
				gate.push(m.Content)
			}
			for i, tc := range m.ToolCalls {
				calls[i] = &tc
			}
			hasCalls = len(calls) > 0
		}
		out.FinishReason = final.finish(hasCalls)
	} else {
		out.FinishReason = "stop"
		if hasCalls {
			out.FinishReason = "tool_calls"
		}
	}
	out.Message.Content, out.Message.ReasoningContent = text.String(), thought.String()
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		tc := *calls[i]
		tc.Function.Arguments = orEmptyObject(tc.Function.Arguments)
		out.Message.ToolCalls = append(out.Message.ToolCalls, tc)
	}
	return out, nil
}
