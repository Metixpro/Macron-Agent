package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"mime"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

type ChatClient interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

type Request struct {
	Messages []Message
	Tools    []Tool

	OnDelta func(text string)

	MaxTokens int

	FailFast bool

	effortCap   string
	effortExact string
	effortSet   bool
}

type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

func (u *Usage) UnmarshalJSON(b []byte) error {
	var raw struct {
		PromptTokens     int  `json:"prompt_tokens"`
		CompletionTokens int  `json:"completion_tokens"`
		TotalTokens      int  `json:"total_tokens"`
		CachedTokens     *int `json:"cached_tokens"`
		CacheHit         *int `json:"prompt_cache_hit_tokens"`
		Details          *struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*u = Usage{PromptTokens: raw.PromptTokens, CompletionTokens: raw.CompletionTokens, TotalTokens: raw.TotalTokens}
	switch {
	case raw.Details != nil && raw.Details.CachedTokens > 0:
		u.CachedTokens = raw.Details.CachedTokens
	case raw.CacheHit != nil:
		u.CachedTokens = *raw.CacheHit
	case raw.CachedTokens != nil:
		u.CachedTokens = *raw.CachedTokens
	}
	u.CachedTokens = min(max(u.CachedTokens, 0), u.PromptTokens)
	return nil
}

func (u Usage) Add(o Usage) Usage {
	return Usage{PromptTokens: u.PromptTokens + o.PromptTokens, CompletionTokens: u.CompletionTokens + o.CompletionTokens,
		TotalTokens: u.TotalTokens + o.TotalTokens, CachedTokens: u.CachedTokens + o.CachedTokens}
}

const cachedWeight = 0.1

func (u Usage) Billable() int {
	cached := min(u.CachedTokens, u.PromptTokens)
	return u.PromptTokens - cached + int(float64(cached)*cachedWeight+0.5) + u.CompletionTokens
}

type Response struct {
	Message      Message
	Usage        Usage
	Model        string
	FinishReason string
	Provider     string
}

func Chat(ctx context.Context, c ChatClient, messages []Message, tools []Tool) (*Message, error) {
	resp, err := c.Complete(ctx, Request{Messages: messages, Tools: tools})
	if err != nil {
		return nil, err
	}
	return &resp.Message, nil
}

const (
	APIOpenAI    = "openai"
	APIAnthropic = "anthropic"
	APIResponses = "responses"
)

func DetectAPI(baseURL string) string {
	if u, err := url.Parse(strings.TrimSpace(baseURL)); err == nil {
		if h := strings.ToLower(u.Hostname()); h == "anthropic.com" || strings.HasSuffix(h, ".anthropic.com") {
			return APIAnthropic
		}
		if strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/responses") {
			return APIResponses
		}
	}
	return APIOpenAI
}

type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	API     string

	NoStream bool

	Temperature    float64
	TemperatureSet bool

	Effort     string
	MaxRetries int
	HTTPClient *http.Client

	StreamIdle time.Duration

	RequestTimeout time.Duration

	sleep func(context.Context, time.Duration) error

	noTemperature, completionTokens atomic.Bool
	noStreamOptions                 atomic.Bool
	noStore                         atomic.Bool
	maxOutput                       atomic.Int64

	noEffort, noMaxEffort, noXHighEffort atomic.Bool
}

func (c *Client) api() string {
	switch c.API {
	case APIAnthropic, APIResponses:
		return c.API
	}
	return APIOpenAI
}

type Part struct {
	Type     string
	Text     string
	ImageURL string
}

func TextPart(s string) Part        { return Part{Type: "text", Text: s} }
func ImagePart(dataURL string) Part { return Part{Type: "image_url", ImageURL: dataURL} }

type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	Parts            []Part     `json:"-"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Reasoning        string     `json:"reasoning,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`

	Stable int  `json:"-"`
	Cache  bool `json:"-"`

	cacheWire bool
}

func (m Message) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role       string     `json:"role"`
		Content    any        `json:"content"`
		ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID string     `json:"tool_call_id,omitempty"`
		Name       string     `json:"name,omitempty"`
	}
	w := wire{Role: m.Role, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Name: m.Name}
	switch {
	case m.cacheWire:
		parts := m.Parts
		if len(parts) == 0 {
			parts = []Part{TextPart(m.Content)}
		}
		wire := make([]map[string]any, 0, len(parts))
		for _, p := range parts {
			if p.Type == "image_url" {
				wire = append(wire, map[string]any{"type": "image_url", "image_url": map[string]string{"url": p.ImageURL}})
			} else {
				wire = append(wire, map[string]any{"type": "text", "text": p.Text})
			}
		}
		wire[len(wire)-1]["cache_control"] = ephemeral
		w.Content = wire
	case len(m.Parts) > 0:
		parts := make([]map[string]any, 0, len(m.Parts))
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				parts = append(parts, map[string]any{"type": "image_url", "image_url": map[string]string{"url": p.ImageURL}})
			} else {
				parts = append(parts, map[string]any{"type": "text", "text": p.Text})
			}
		}
		w.Content = parts
	case m.Role == "assistant" && len(m.ToolCalls) > 0 && m.Content == "":
		w.Content = nil
	default:
		w.Content = m.Content
	}
	return json.Marshal(w)
}

func (m *Message) UnmarshalJSON(b []byte) error {
	type alias Message
	var raw struct {
		alias
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	*m = Message(raw.alias)
	if len(raw.Content) == 0 || string(raw.Content) == "null" {
		return nil
	}
	var s string
	if json.Unmarshal(raw.Content, &s) == nil {
		m.Content = s
		return nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(raw.Content, &parts); err != nil {
		return fmt.Errorf("unsupported message content: %w", err)
	}
	var text strings.Builder
	for _, p := range parts {
		if p.Type == "image_url" {
			m.Parts = append(m.Parts, ImagePart(p.ImageURL.URL))
			continue
		}
		m.Parts = append(m.Parts, TextPart(p.Text))
		text.WriteString(p.Text)
	}
	m.Content = text.String()
	return nil
}

func (m Message) Thought() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

func (m Message) Text() string {
	if len(m.Parts) == 0 {
		return m.Content
	}
	var sb strings.Builder
	for _, p := range m.Parts {
		if p.Type == "text" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Function FunctionCall `json:"function"`
}

type FunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type Tool struct {
	Type     string  `json:"type"`
	Function ToolDef `json:"function"`
}

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ChatRequest struct {
	Model         string         `json:"model"`
	Messages      []Message      `json:"messages"`
	Tools         []Tool         `json:"tools,omitempty"`
	Stream        bool           `json:"stream"`
	StreamOptions map[string]any `json:"stream_options,omitempty"`
	Temperature   *float64       `json:"temperature,omitempty"`
	MaxTokens     int            `json:"max_tokens,omitempty"`

	MaxCompletionTokens int    `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string `json:"reasoning_effort,omitempty"`
}

type ChatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Index        int     `json:"index"`
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm api error (status %d): %s", e.Status, e.Body)
}

func (e *APIError) Temporary() bool { return retryableStatus(e.Status) }

func retryableStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooEarly, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout,
		statusOverloaded:
		return true
	}
	return false
}

const statusOverloaded = 529

var ErrPolicyViolation = errors.New("refused by the provider's safety filter")

var policyMarkers = []string{"prohibited use", "prohibited_content", "prohibited content", "sensitive words", "sensitive word",
	"safety filter", "content moderation", "safety guidelines", "content_filter", "content management policy",
	"responsibleaipolicyviolation", "blocked due to safety", "content exists risk", "violates our usage polic", "敏感"}

func IsPolicyViolationText(s string) bool {
	s = strings.ToLower(s)
	for _, m := range policyMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func IsPolicyViolation(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrPolicyViolation) {
		return true
	}
	var api *APIError
	if errors.As(err, &api) {
		return IsPolicyViolationText(api.Body)
	}
	return IsPolicyViolationText(err.Error())
}

func filtered(r *Response) error {
	switch strings.ToLower(r.FinishReason) {
	case "content_filter", "safety", "prohibited_content", "blocklist", "spii", "recitation":
		if strings.TrimSpace(r.Message.Content) == "" && len(r.Message.ToolCalls) == 0 {
			return fmt.Errorf("%w (finish_reason %s)", ErrPolicyViolation, r.FinishReason)
		}
	}
	return nil
}

var ErrPartialStream = errors.New("stream interrupted after output started")

const maxResponseBytes = 16 << 20

const maxStreamBytes = 64 << 20

var ErrTooLarge = errors.New("the answer is too large")

const maxStreamText = 4 << 20

type deltaGate struct {
	on      func(string)
	n, last int
}

func (g *deltaGate) push(text string) {
	if errorPrefix(text) {
		return
	}
	if g.on == nil {
		return
	}
	g.n++
	if g.n > 500 && len(text)-g.last < 256 {
		return
	}
	g.last = len(text)
	g.on(text)
}

func capped(r io.Reader) io.Reader { return &capReader{r: r, left: maxStreamBytes} }

type capReader struct {
	r    io.Reader
	left int64
}

func (c *capReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, ErrTooLarge
	}
	if int64(len(p)) > c.left {
		p = p[:c.left]
	}
	n, err := c.r.Read(p)
	c.left -= int64(n)
	return n, err
}

const MaxRetryWait = 20 * time.Second

func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL:     strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		APIKey:      apiKey,
		Model:       model,
		Temperature: 0.3,
		MaxRetries:  2,
		HTTPClient:  NewHTTPClient(),
	}
}

func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 10 * time.Minute
	return &http.Client{Transport: t}
}

const (
	defaultStreamIdle     = 3 * time.Minute
	defaultRequestTimeout = 10 * time.Minute
)

type idleBody struct {
	io.ReadCloser
	idle    time.Duration
	timer   *time.Timer
	stalled atomic.Bool
}

func newIdleBody(rc io.ReadCloser, idle time.Duration) *idleBody {
	b := &idleBody{ReadCloser: rc, idle: idle}
	b.timer = time.AfterFunc(idle, func() { b.stalled.Store(true); rc.Close() })
	return b
}

func (b *idleBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if n > 0 {
		b.timer.Reset(b.idle)
	}
	if err != nil && b.stalled.Load() {
		err = fmt.Errorf("the stream sent nothing for %s", b.idle)
	}
	return n, err
}

func (b *idleBody) Close() error {
	b.timer.Stop()
	return b.ReadCloser.Close()
}

var contextMarkers = []string{"context_length_exceeded", "context length", "maximum context", "context window", "too many tokens",
	"prompt is too long", "input is too long", "tokens exceeds", "exceeds the maximum", "reduce the length", "string_above_max_length",
	"request too large", "maximum input length"}

func IsSizeRateLimit(err error) bool {
	var api *APIError
	if !errors.As(err, &api) || (api.Status != http.StatusRequestEntityTooLarge && api.Status != http.StatusTooManyRequests) {
		return false
	}
	body := strings.ToLower(api.Body)
	for _, m := range []string{"tokens per minute", "(tpm)", "requests per minute", "rate_limit_exceeded", "tokens per day"} {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

func IsContextOverflow(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	if api.Status == http.StatusRequestEntityTooLarge {
		return true
	}
	body := strings.ToLower(api.Body)
	for _, m := range contextMarkers {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*Message, error) {
	return Chat(ctx, c, messages, tools)
}

func (c *Client) Complete(ctx context.Context, req Request) (*Response, error) {
	if c.NoStream && req.OnDelta != nil {
		deliver := req.OnDelta
		req.OnDelta = nil
		resp, err := c.complete1(ctx, req)
		if err == nil && resp.Message.Content != "" {
			deliver(resp.Message.Content)
		}
		return resp, err
	}
	return c.complete1(ctx, req)
}

func (c *Client) complete1(ctx context.Context, req Request) (*Response, error) {
	req.effortCap = effortCapOf(ctx)
	req.effortExact, req.effortSet = effortOf(ctx)
	req.Messages = normalizeTranscript(req.Messages, len(req.Tools) > 0)
	resp, err := c.complete(ctx, req)
	if err == nil {
		if gerr := gatewayError(resp); gerr != nil {
			resp, err = nil, gerr
		}
	}
	if err != nil && toolPairingError(err) && hasToolTurns(req.Messages) && ctx.Err() == nil {
		log.Printf("llm: %s refused the tool calls of the conversation (%v); sending them as text", c.Model, err)
		req.Messages = flattenTools(req.Messages)
		if resp, err = c.complete(ctx, req); err == nil {
			if gerr := gatewayError(resp); gerr != nil {
				resp, err = nil, gerr
			}
		}
	}
	return resp, err
}

func (c *Client) complete(ctx context.Context, req Request) (*Response, error) {
	body := ChatRequest{Model: c.Model, Messages: req.Messages, Tools: req.Tools, MaxTokens: req.MaxTokens}
	if req.OnDelta != nil {
		body.Stream = true
		if !c.noStreamOptions.Load() {
			body.StreamOptions = map[string]any{"include_usage": true}
		}
	}

	maxRetries := c.MaxRetries
	if req.FailFast {
		maxRetries = 0
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		payload, err := c.encode(body, req)
		if err != nil {
			return nil, fmt.Errorf("marshal request: %w", err)
		}
		resp, retryAfter, err := c.once(ctx, payload, req.OnDelta)
		if err == nil {
			if ferr := filtered(resp); ferr != nil {
				return nil, ferr
			}
			return resp, nil
		}

		var api *APIError
		if body.StreamOptions != nil && errors.As(err, &api) && api.Status == http.StatusBadRequest && strings.Contains(api.Body, "stream_options") {
			body.StreamOptions = nil
			c.noStreamOptions.Store(true)
			attempt--
			continue
		}

		if errors.As(err, &api) && api.Status == http.StatusBadRequest && (c.learnEffort(api.Body, c.wireEffort(req)) ||
			(c.api() == APIOpenAI && c.learn(api.Body)) || (c.api() == APIAnthropic && c.learnAnthropic(api.Body)) ||
			(c.api() == APIResponses && c.learnResponses(api.Body))) {
			attempt--
			continue
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, ErrPartialStream) || !isRetryable(err) || attempt == maxRetries || retryAfter > MaxRetryWait {
			break
		}
		delay := retryAfter
		if delay <= 0 {
			delay = time.Duration(1<<attempt) * time.Second
		}
		delay += time.Duration(rand.Int64N(int64(300 * time.Millisecond)))
		if err := c.wait(ctx, delay); err != nil {
			break
		}
	}
	return nil, lastErr
}

func (c *Client) encode(body ChatRequest, req Request) ([]byte, error) {
	switch c.api() {
	case APIAnthropic:
		return json.Marshal(c.anthropicBody(req))
	case APIResponses:
		return json.Marshal(c.responsesBody(req))
	}
	reasoning := reasoningModel(c.Model)
	if t := c.Temperature; !c.noTemperature.Load() && (c.TemperatureSet || !reasoning) {
		body.Temperature = &t
	}
	if body.MaxTokens > 0 && (reasoning || c.completionTokens.Load()) {
		body.MaxCompletionTokens, body.MaxTokens = body.MaxTokens, 0
	}
	body.ReasoningEffort = c.wireEffort(req)
	if c.cacheMarks() {
		body.Messages = markCache(body.Messages)
	}
	return json.Marshal(body)
}

func (c *Client) cacheMarks() bool {
	u, err := url.Parse(c.BaseURL)
	if err != nil || !strings.HasSuffix(strings.ToLower(u.Hostname()), "openrouter.ai") {
		return false
	}
	m := strings.ToLower(c.Model)
	return strings.Contains(m, "claude") || strings.HasPrefix(m, "anthropic/")
}

func markCache(msgs []Message) []Message {
	out := append([]Message(nil), msgs...)
	marks := 0
	textual := func(m Message) bool { return len(m.Parts) > 0 || strings.TrimSpace(m.Content) != "" }
	mark := func(i int) {
		if i >= 0 && i < len(out) && !out[i].cacheWire && textual(out[i]) && marks < 4 {
			out[i].cacheWire = true
			marks++
		}
	}
	if len(out) > 0 && out[0].Role == "system" {
		mark(0)
	}
	for i := range out {
		if out[i].Cache {
			mark(i)
		}
	}
	last := len(out) - 1
	for last > 0 && !textual(out[last]) {
		last--
	}
	mark(last)
	return out
}

func reasoningModel(model string) bool {
	m := strings.ToLower(model)
	if i := strings.LastIndex(m, "/"); i >= 0 {
		m = m[i+1:]
	}
	if len(m) >= 2 && m[0] == 'o' && m[1] >= '1' && m[1] <= '9' {
		return true
	}
	return strings.HasPrefix(m, "gpt-5") || strings.HasPrefix(m, "gpt-6")
}

func (c *Client) learn(body string) bool {
	b := strings.ToLower(body)
	switch {
	case strings.Contains(b, "temperature") && !c.noTemperature.Load() &&
		(strings.Contains(b, "unsupported") || strings.Contains(b, "not support") || strings.Contains(b, "only the default")):
		c.noTemperature.Store(true)
		return true
	case strings.Contains(b, "max_tokens") && strings.Contains(b, "max_completion_tokens") && !c.completionTokens.Load():
		c.completionTokens.Store(true)
		return true
	}
	return false
}

func (c *Client) endpoint() string {
	if c.api() == APIAnthropic {
		if u, err := url.Parse(c.BaseURL); err == nil && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/v1") {
			return joinURL(c.BaseURL, "/messages")
		}
		return joinURL(c.BaseURL, "/v1/messages")
	}
	if c.api() == APIResponses {
		return joinURL(c.BaseURL, "/responses")
	}
	out := joinURL(c.BaseURL, "/chat/completions")
	if u, err := url.Parse(out); err == nil && azureHost(u.Hostname()) && strings.Contains(u.Path, "/deployments/") && u.Query().Get("api-version") == "" {
		q := u.Query()
		q.Set("api-version", azureAPIVersion)
		u.RawQuery = q.Encode()
		out = u.String()
	}
	return out
}

const azureAPIVersion = "2024-10-21"

func azureHost(h string) bool {
	h = strings.ToLower(h)
	return strings.HasSuffix(h, ".openai.azure.com") || strings.HasSuffix(h, ".cognitiveservices.azure.com") ||
		strings.HasSuffix(h, ".services.ai.azure.com")
}

func joinURL(base, path string) string {
	u, err := url.Parse(base)
	if err != nil {
		return strings.TrimRight(base, "/") + path
	}
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawPath = ""
	return u.String()
}

func (c *Client) authorize(req *http.Request) {
	switch {
	case c.api() == APIAnthropic:
		req.Header.Set("anthropic-version", anthropicVersion)
		if c.APIKey != "" {
			req.Header.Set("x-api-key", c.APIKey)
			if DetectAPI(c.BaseURL) != APIAnthropic {
				req.Header.Set("Authorization", "Bearer "+c.APIKey)
			}
		}
	case c.APIKey == "":
	case azureHost(req.URL.Hostname()):
		req.Header.Set("api-key", c.APIKey)
	default:
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
}

func (c *Client) wait(ctx context.Context, d time.Duration) error {
	if c.sleep != nil {
		return c.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func IsRetryable(err error) bool { return isRetryable(err) }

func isRetryable(err error) bool {
	if errors.Is(err, ErrPartialStream) || IsPolicyViolation(err) {
		return false
	}
	var api *APIError
	if errors.As(err, &api) {
		return api.Temporary()
	}
	var tmp interface{ Timeout() bool }
	if errors.As(err, &tmp) {
		return true
	}
	return errors.Is(err, errTransport)
}

var errTransport = errors.New("transport error")

func envelopeError(data []byte) *APIError {
	var e struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	}
	if len(data) == 0 || data[0] != '{' || json.Unmarshal(data, &e) != nil || e.Code == nil || e.Msg == "" || *e.Code == 200 || *e.Code == 0 {
		return nil
	}
	status := *e.Code
	switch {
	case status == 455:
		status = http.StatusServiceUnavailable
	case status < 400 || status > 599:
		status = http.StatusBadGateway
	}
	return &APIError{Status: status, Body: strings.TrimSpace(e.Msg)}
}

var ErrBadAnswer = errors.New("empty or unreadable answer")

func IsUnavailable(err error) bool {
	return errors.Is(err, errTransport) || errors.Is(err, ErrPartialStream) || errors.Is(err, ErrBadAnswer)
}

func (c *Client) once(ctx context.Context, payload []byte, onDelta func(string)) (*Response, time.Duration, error) {
	idle := c.StreamIdle
	if idle <= 0 {
		idle = defaultStreamIdle
	}
	var (
		answered func() bool
		silent   atomic.Bool
	)
	if onDelta == nil {
		limit := c.RequestTimeout
		if limit <= 0 {
			limit = defaultRequestTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, limit)
		defer cancel()
	} else {

		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		defer cancel()
		answered = time.AfterFunc(idle, func() { silent.Store(true); cancel() }).Stop
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
	if err != nil {
		return nil, 0, fmt.Errorf("create http request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if onDelta != nil {
		req.Header.Set("Accept", "text/event-stream")
	}
	c.authorize(req)

	resp, err := c.HTTPClient.Do(req)
	if answered != nil {
		answered()
		if silent.Load() {
			if err == nil {
				resp.Body.Close()
			}
			return nil, 0, fmt.Errorf("%w: no answer within %s", errTransport, idle)
		}
	}
	if err != nil {
		return nil, 0, fmt.Errorf("%w: %w", errTransport, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		ra := parseRetryAfter(resp.Header.Get("Retry-After"))
		return nil, ra, &APIError{Status: resp.StatusCode, Body: snippet(body, 500), RetryAfter: ra}
	}

	switch c.api() {
	case APIAnthropic:
		return readAnthropic(resp, onDelta, idle)
	case APIResponses:
		return readResponses(resp, onDelta, idle)
	}

	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt == "text/event-stream" {
		body := newIdleBody(resp.Body, idle)
		defer body.Close()
		out, err := readStream(body, onDelta)
		return out, 0, err
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, 0, fmt.Errorf("%w: read response body: %w", errTransport, err)
	}
	if ae := envelopeError(body); ae != nil {
		return nil, 0, ae
	}
	var out ChatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, 0, fmt.Errorf("%w: unmarshal response: %w (body: %s)", ErrBadAnswer, err, snippet(body, 200))
	}
	if out.Error != nil && out.Error.Message != "" {
		return nil, 0, &APIError{Status: http.StatusBadGateway, Body: out.Error.Message}
	}
	if len(out.Choices) == 0 {
		return nil, 0, fmt.Errorf("%w: no choices returned from llm", ErrBadAnswer)
	}
	r := &Response{Message: out.Choices[0].Message, Usage: out.Usage, Model: out.Model, FinishReason: out.Choices[0].FinishReason}
	if onDelta != nil && r.Message.Content != "" && !looksLikeGatewayError(r.Message.Content) {
		onDelta(r.Message.Content)
	}
	return r, 0, nil
}

func readStream(body io.Reader, onDelta func(string)) (*Response, error) {
	type toolDelta struct {
		Index    int    `json:"index"`
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	type chunk struct {
		Model   string `json:"model"`
		Choices []struct {
			Delta struct {
				Content          string      `json:"content"`
				ReasoningContent string      `json:"reasoning_content"`
				Reasoning        string      `json:"reasoning"`
				ToolCalls        []toolDelta `json:"tool_calls"`
			} `json:"delta"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *Usage `json:"usage"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	var (
		out       = &Response{Message: Message{Role: "assistant"}}
		text      strings.Builder
		reasoning strings.Builder
		calls     = map[int]*ToolCall{}
		slot      = map[int]int{}
		started   bool
		gotData   bool
		done      bool
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
	for sc.Scan() {
		if text.Len()+reasoning.Len()+argBytes > maxStreamText {
			return fail(ErrTooLarge)
		}
		line := strings.TrimSpace(sc.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var ch chunk
		if err := json.Unmarshal([]byte(data), &ch); err != nil {
			continue
		}
		gotData = true
		if ch.Error != nil && ch.Error.Message != "" {
			return fail(&APIError{Status: http.StatusBadGateway, Body: ch.Error.Message})
		}
		if ch.Model != "" {
			out.Model = ch.Model
		}
		if ch.Usage != nil {
			out.Usage = *ch.Usage
		}
		for _, c := range ch.Choices {
			if c.FinishReason != "" {
				out.FinishReason = c.FinishReason
			}
			reasoning.WriteString(c.Delta.ReasoningContent + c.Delta.Reasoning)
			for _, td := range c.Delta.ToolCalls {

				key, ok := slot[td.Index]
				if !ok || (td.ID != "" && calls[key].ID != "" && calls[key].ID != td.ID) {
					key = len(calls)
					slot[td.Index], calls[key] = key, &ToolCall{Type: "function"}
				}
				tc := calls[key]
				if td.ID != "" {
					tc.ID = td.ID
				}
				if td.Type != "" {
					tc.Type = td.Type
				}
				if td.Function.Name != tc.Function.Name {
					tc.Function.Name += td.Function.Name
				}
				tc.Function.Arguments += td.Function.Arguments
				argBytes += len(td.Function.Arguments)
			}
			if c.Delta.Content != "" {
				text.WriteString(c.Delta.Content)
				started = true
				gate.push(text.String())
			}
		}
	}
	if err := sc.Err(); err != nil {
		return fail(fmt.Errorf("%w: %w", errTransport, err))
	}
	if !gotData {
		return nil, fmt.Errorf("%w: empty stream", errTransport)
	}
	if !done && out.FinishReason == "" && len(calls) > 0 {
		out.FinishReason = "length"
	}

	out.Message.Content = text.String()
	out.Message.ReasoningContent = reasoning.String()
	idx := make([]int, 0, len(calls))
	for i := range calls {
		idx = append(idx, i)
	}
	sort.Ints(idx)
	for _, i := range idx {
		out.Message.ToolCalls = append(out.Message.ToolCalls, *calls[i])
	}
	return out, nil
}

func parseRetryAfter(v string) time.Duration {
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs > 0 {
		return time.Duration(min(secs, 86400)) * time.Second
	}
	return 0
}

func snippet(b []byte, n int) string {
	s := strings.ToValidUTF8(string(b), "")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func (c *Client) Models(ctx context.Context) ([]string, error) {
	u := joinURL(c.BaseURL, "/models")
	if c.api() == APIAnthropic {
		u = strings.TrimSuffix(c.endpoint(), "/messages") + "/models?limit=1000"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	c.authorize(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errTransport, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Body: snippet(body, 300)}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("unexpected model list: %s", snippet(body, 120))
	}
	var ids []string
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	for _, m := range out.Models {
		if m.Name != "" {
			ids = append(ids, m.Name)
		}
	}
	sort.Strings(ids)
	return ids, nil
}
