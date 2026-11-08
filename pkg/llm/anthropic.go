package llm

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Anthropic's Messages API. Requests and answers keep the package's
// OpenAI-shaped types: system messages become the system blocks, tool calls
// tool_use blocks and tool results tool_result blocks. Prompt caching is on:
// the tools, the stable start of the system prompt (Message.Stable) and the
// conversation so far are cached, so each step of a job only pays for what
// is new.

const anthropicVersion = "2023-06-01"

var ephemeral = map[string]any{"type": "ephemeral"}

type anthropicMsg struct {
	Role    string           `json:"role"`
	Content []map[string]any `json:"content"`
}

func (c *Client) anthropicBody(req Request) map[string]any {
	var (
		system []map[string]any
		msgs   []anthropicMsg
	)
	add := func(role string, blocks ...map[string]any) {
		if len(blocks) == 0 {
			return
		}
		if n := len(msgs); n > 0 && msgs[n-1].Role == role {
			msgs[n-1].Content = append(msgs[n-1].Content, blocks...)
			return
		}
		msgs = append(msgs, anthropicMsg{Role: role, Content: blocks})
	}
	leading := true
	for _, m := range req.Messages {
		switch m.Role {
		case "system":
			if !leading { // a note in the middle of the conversation
				add("user", textBlock(m.Text()))
				continue
			}
			text := m.Text()
			if m.Stable > 0 && m.Stable < len(text) && len(system) == 0 {
				system = append(system, map[string]any{"type": "text", "text": text[:m.Stable], "cache_control": ephemeral})
				text = text[m.Stable:]
			} else if m.Stable >= len(text) && len(system) == 0 {
				system = append(system, map[string]any{"type": "text", "text": text, "cache_control": ephemeral})
				continue
			}
			if strings.TrimSpace(text) != "" {
				system = append(system, map[string]any{"type": "text", "text": text})
			}
		case "assistant":
			leading = false
			var blocks []map[string]any
			if t := m.Text(); strings.TrimSpace(t) != "" {
				blocks = append(blocks, textBlock(t))
			}
			for _, tc := range m.ToolCalls {
				blocks = append(blocks, map[string]any{"type": "tool_use", "id": toolID(tc.ID), "name": tc.Function.Name, "input": toolInput(tc.Function.Arguments)})
			}
			add("assistant", blocks...)
		case "tool":
			leading = false
			content := m.Content
			if strings.TrimSpace(content) == "" {
				content = "(no output)"
			}
			add("user", map[string]any{"type": "tool_result", "tool_use_id": toolID(m.ToolCallID), "content": content})
		default:
			leading = false
			add("user", userBlocks(m)...)
		}
	}
	if len(msgs) == 0 || msgs[0].Role != "user" {
		msgs = append([]anthropicMsg{{Role: "user", Content: []map[string]any{textBlock("(continuing)")}}}, msgs...)
	}

	body := map[string]any{"model": c.Model, "max_tokens": c.anthropicMaxTokens(req), "messages": msgs}
	if len(system) > 0 {
		body["system"] = system
	}
	if len(req.Tools) > 0 {
		tools := make([]map[string]any, 0, len(req.Tools))
		for _, t := range req.Tools {
			schema := t.Function.Parameters
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"name": t.Function.Name, "description": t.Function.Description, "input_schema": schema})
		}
		tools[len(tools)-1]["cache_control"] = ephemeral
		body["tools"] = tools
		// A job's conversation grows step by step: cache it up to here.
		last := msgs[len(msgs)-1].Content
		last[len(last)-1]["cache_control"] = ephemeral
	}
	// No temperature unless the admin chose one: some Claude models refuse
	// any but the default (a 400; then it is learned and left out).
	if c.TemperatureSet && !c.noTemperature.Load() {
		body["temperature"] = c.Temperature
	}
	if e := c.wireEffort(req); e != "" {
		body["output_config"] = map[string]any{"effort": e}
	}
	// Said either way: some gateways (kie.ai) stream unless told not to.
	body["stream"] = req.OnDelta != nil
	return body
}

func textBlock(s string) map[string]any {
	if strings.TrimSpace(s) == "" {
		s = "(empty)"
	}
	return map[string]any{"type": "text", "text": s}
}

func userBlocks(m Message) []map[string]any {
	if len(m.Parts) == 0 {
		return []map[string]any{textBlock(m.Content)}
	}
	var out []map[string]any
	for _, p := range m.Parts {
		switch {
		case p.Type == "image_url":
			out = append(out, imageBlock(p.ImageURL))
		case strings.TrimSpace(p.Text) != "":
			out = append(out, map[string]any{"type": "text", "text": p.Text})
		}
	}
	if len(out) == 0 {
		out = append(out, textBlock(""))
	}
	return out
}

// imageBlock turns a data: URL into a base64 image and anything else into a URL image.
func imageBlock(src string) map[string]any {
	if rest, ok := strings.CutPrefix(src, "data:"); ok {
		if meta, data, ok := strings.Cut(rest, ","); ok && strings.HasSuffix(meta, ";base64") {
			return map[string]any{"type": "image", "source": map[string]any{"type": "base64",
				"media_type": strings.TrimSuffix(meta, ";base64"), "data": data}}
		}
	}
	return map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": src}}
}

// toolID makes a call id from another provider (a job that failed over in
// the middle) one Anthropic accepts: letters, digits, _ and -.
func toolID(id string) string {
	clean := strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, id)
	if clean == "" {
		clean = "call"
	}
	return clean
}

// toolInput is a call's arguments as the object Anthropic wants.
func toolInput(args string) map[string]any {
	var in map[string]any
	if json.Unmarshal([]byte(args), &in) != nil || in == nil {
		in = map[string]any{}
	}
	return in
}

// anthropicMaxTokens: the API wants a limit. Streamed answers get room for
// big files written in one call; older models allow less. A refusal that
// names the model's own limit is remembered.
func (c *Client) anthropicMaxTokens(req Request) int {
	if req.MaxTokens > 0 {
		return req.MaxTokens
	}
	if n := c.maxOutput.Load(); n > 0 {
		return int(n)
	}
	m := strings.ToLower(c.Model)
	switch {
	case strings.Contains(m, "claude-3-opus"), strings.Contains(m, "claude-3-sonnet"), strings.Contains(m, "claude-3-haiku"):
		return 4096
	case strings.Contains(m, "claude-3-5"):
		return 8192
	case req.OnDelta == nil && (c.effortFor(req) == EffortHigh || c.effortFor(req) == EffortUltra):
		return 16000 // room to think
	case req.OnDelta == nil:
		return 8192
	case c.effortFor(req) == EffortUltra:
		return 64000
	}
	return 32000
}

var maxTokensRefusal = regexp.MustCompile(`max_tokens: \d+ > (\d+)`)

// learnAnthropic reads a refusal of the output limit ("max_tokens: 32000 >
// 8192, which is the maximum allowed"); it reports whether to go again.
func (c *Client) learnAnthropic(body string) bool {
	if b := strings.ToLower(body); strings.Contains(b, "temperature") && c.TemperatureSet && !c.noTemperature.Load() {
		c.noTemperature.Store(true) // the model takes only its default
		return true
	}
	m := maxTokensRefusal.FindStringSubmatch(body)
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 || int64(n) == c.maxOutput.Load() {
		return false
	}
	c.maxOutput.Store(int64(n))
	return true
}

type anthropicUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

func (u anthropicUsage) usage() Usage {
	in := u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
	return Usage{PromptTokens: in, CompletionTokens: u.OutputTokens, TotalTokens: in + u.OutputTokens, CachedTokens: u.CacheReadInputTokens}
}

type anthropicBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Thinking string          `json:"thinking"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

func (e anthropicError) api() *APIError {
	status := http.StatusInternalServerError
	switch e.Type {
	case "overloaded_error":
		status = statusOverloaded
	case "rate_limit_error":
		status = http.StatusTooManyRequests
	case "invalid_request_error":
		status = http.StatusBadRequest
	case "authentication_error":
		status = http.StatusUnauthorized
	case "permission_error":
		status = http.StatusForbidden
	}
	return &APIError{Status: status, Body: e.Type + ": " + e.Message}
}

// stopReason maps Anthropic's stop reasons to the OpenAI ones the agent reads.
func stopReason(s string) string {
	switch s {
	case "tool_use":
		return "tool_calls"
	case "max_tokens", "model_context_window_exceeded": // cut off either way
		return "length"
	case "refusal":
		return "content_filter"
	case "":
		return ""
	}
	return "stop"
}

func readAnthropic(resp *http.Response, onDelta func(string), idle time.Duration) (*Response, time.Duration, error) {
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
		body := newIdleBody(resp.Body, idle)
		defer body.Close()
		out, err := readAnthropicStream(body, onDelta)
		return out, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: read response body: %w", errTransport, err)
	}
	if ae := envelopeError(data); ae != nil {
		return nil, 0, ae
	}
	var out struct {
		Model      string           `json:"model"`
		Content    []anthropicBlock `json:"content"`
		StopReason string           `json:"stop_reason"`
		Usage      anthropicUsage   `json:"usage"`
		Error      *anthropicError  `json:"error"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, 0, fmt.Errorf("%w: unmarshal response: %w (body: %s)", ErrBadAnswer, err, snippet(data, 200))
	}
	if out.Error != nil {
		return nil, 0, out.Error.api()
	}
	r := &Response{Message: Message{Role: "assistant"}, Model: out.Model, Usage: out.Usage.usage(), FinishReason: stopReason(out.StopReason)}
	var text, thought strings.Builder
	for _, b := range out.Content {
		switch b.Type {
		case "text":
			text.WriteString(b.Text)
		case "thinking":
			thought.WriteString(b.Thinking)
		case "tool_use":
			args := string(b.Input)
			if strings.TrimSpace(args) == "" || args == "null" {
				args = "{}"
			}
			r.Message.ToolCalls = append(r.Message.ToolCalls, ToolCall{ID: b.ID, Type: "function", Function: FunctionCall{Name: b.Name, Arguments: args}})
		}
	}
	r.Message.Content, r.Message.ReasoningContent = text.String(), thought.String()
	if onDelta != nil && r.Message.Content != "" && !looksLikeGatewayError(r.Message.Content) {
		onDelta(r.Message.Content)
	}
	return r, 0, nil
}

func readAnthropicStream(body io.Reader, onDelta func(string)) (*Response, error) {
	type event struct {
		Type    string `json:"type"`
		Index   int    `json:"index"`
		Message struct {
			Model string         `json:"model"`
			Usage anthropicUsage `json:"usage"`
		} `json:"message"`
		ContentBlock anthropicBlock `json:"content_block"`
		Delta        struct {
			Type        string `json:"type"`
			Text        string `json:"text"`
			PartialJSON string `json:"partial_json"`
			Thinking    string `json:"thinking"`
			StopReason  string `json:"stop_reason"`
		} `json:"delta"`
		Usage *anthropicUsage `json:"usage"`
		Error *anthropicError `json:"error"`
	}
	var (
		out       = &Response{Message: Message{Role: "assistant"}}
		usage     anthropicUsage
		text      strings.Builder
		thought   strings.Builder
		calls     = map[int]*ToolCall{}
		started   bool
		gotData   bool
		completed bool
	)
	fail := func(err error) (*Response, error) {
		if started {
			return nil, fmt.Errorf("%w: %w", ErrPartialStream, err)
		}
		return nil, err
	}
	gate := &deltaGate{on: onDelta}
	sc := bufio.NewScanner(capped(body))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	argBytes := 0
	for sc.Scan() && !completed {
		if text.Len()+thought.Len()+argBytes > maxStreamText {
			return fail(ErrTooLarge)
		}
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		var ev event
		if json.Unmarshal([]byte(strings.TrimSpace(strings.TrimPrefix(line, "data:"))), &ev) != nil {
			continue
		}
		gotData = true
		switch ev.Type {
		case "error":
			if ev.Error != nil {
				return fail(ev.Error.api())
			}
			return fail(&APIError{Status: http.StatusInternalServerError, Body: "stream error"})
		case "message_start":
			out.Model, usage = ev.Message.Model, ev.Message.Usage
		case "content_block_start":
			if ev.ContentBlock.Type == "tool_use" {
				calls[ev.Index] = &ToolCall{ID: ev.ContentBlock.ID, Type: "function", Function: FunctionCall{Name: ev.ContentBlock.Name}}
			}
		case "content_block_delta":
			switch ev.Delta.Type {
			case "text_delta":
				text.WriteString(ev.Delta.Text)
				started = true
				gate.push(text.String())
			case "input_json_delta":
				if tc := calls[ev.Index]; tc != nil {
					tc.Function.Arguments += ev.Delta.PartialJSON
					argBytes += len(ev.Delta.PartialJSON)
				}
			case "thinking_delta":
				thought.WriteString(ev.Delta.Thinking)
			}
		case "message_delta":
			if ev.Delta.StopReason != "" {
				out.FinishReason = stopReason(ev.Delta.StopReason)
			}
			if ev.Usage != nil {
				usage.OutputTokens = ev.Usage.OutputTokens
			}
		case "message_stop":
			completed = true
		}
	}
	if err := sc.Err(); err != nil && !completed {
		return fail(fmt.Errorf("%w: %w", errTransport, err))
	}
	if !gotData {
		return nil, fmt.Errorf("%w: empty stream", errTransport)
	}
	if !completed && out.FinishReason == "" {
		return fail(fmt.Errorf("%w: %w", errTransport, errors.New("the stream ended before the answer did")))
	}
	out.Usage = usage.usage()
	out.Message.Content, out.Message.ReasoningContent = text.String(), thought.String()
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		tc := calls[i]
		if strings.TrimSpace(tc.Function.Arguments) == "" {
			tc.Function.Arguments = "{}"
		}
		out.Message.ToolCalls = append(out.Message.ToolCalls, *tc)
	}
	return out, nil
}
