// Package llm is a small client for chat-completion APIs: OpenAI-compatible
// ones (OpenAI, Azure, Gemini, DeepSeek, OpenRouter, Groq, Ollama…) and
// Anthropic's Messages API, with streaming, tools, images and automatic retries.
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

// ChatClient is anything that can complete a conversation: a single Client or
// a pool of providers with failover.
type ChatClient interface {
	Complete(ctx context.Context, req Request) (*Response, error)
}

// Request is one completion call.
type Request struct {
	Messages []Message
	Tools    []Tool
	// OnDelta, when set, enables streaming and receives the text generated so
	// far (the whole text, not just the new piece) as it arrives.
	OnDelta func(text string)
	// MaxTokens limits the answer length (0 = provider default).
	MaxTokens int
	// FailFast returns rate limits, overloads and network errors at once
	// instead of retrying: the caller has another provider to go to.
	FailFast bool

	effortCap   string // WithEffortCap of the call's context (Complete sets it)
	effortExact string // WithEffort of the call's context
	effortSet   bool
}

// Usage is the token accounting reported by the provider. CachedTokens is
// the part of PromptTokens read from the provider's prompt cache (cheaper).
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

// UnmarshalJSON reads the cached part too, wherever the provider puts it:
// prompt_tokens_details.cached_tokens (OpenAI and most gateways),
// prompt_cache_hit_tokens (DeepSeek) or cached_tokens.
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

// Add sums two usages.
func (u Usage) Add(o Usage) Usage {
	return Usage{PromptTokens: u.PromptTokens + o.PromptTokens, CompletionTokens: u.CompletionTokens + o.CompletionTokens,
		TotalTokens: u.TotalTokens + o.TotalTokens, CachedTokens: u.CachedTokens + o.CachedTokens}
}

// cachedWeight is what a cached prompt token counts for in Billable:
// providers charge about a tenth for them.
const cachedWeight = 0.1

// Billable is the tokens a user is charged: new prompt tokens and the
// answer in full, cached prompt tokens at a tenth.
func (u Usage) Billable() int {
	cached := min(u.CachedTokens, u.PromptTokens)
	return u.PromptTokens - cached + int(float64(cached)*cachedWeight+0.5) + u.CompletionTokens
}

// Response is the result of a completion.
type Response struct {
	Message      Message
	Usage        Usage
	Model        string
	FinishReason string
	Provider     string // set by multi-provider clients
}

// Chat is a convenience wrapper for callers that only need the message.
func Chat(ctx context.Context, c ChatClient, messages []Message, tools []Tool) (*Message, error) {
	resp, err := c.Complete(ctx, Request{Messages: messages, Tools: tools})
	if err != nil {
		return nil, err
	}
	return &resp.Message, nil
}

// The wire formats a Client speaks.
const (
	APIOpenAI    = "openai"    // POST /chat/completions
	APIAnthropic = "anthropic" // POST /v1/messages
	APIResponses = "responses" // POST /responses (OpenAI's Responses API)
)

// DetectAPI guesses the wire format from a base URL: Anthropic's own
// address speaks the Messages API, an address that ends in /responses the
// Responses API, everything else the chat-completions format.
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

// Client talks to a chat-completion endpoint.
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	API     string // APIOpenAI (default), APIResponses or APIAnthropic
	// NoStream asks for every answer whole, even where the caller shows the
	// text as it arrives (for a gateway whose streams break or stall): the
	// text then comes at once, when the answer is complete.
	NoStream bool
	// Temperature goes with OpenAI-compatible requests (default 0.3), not to
	// reasoning models or Anthropic, which want their own default.
	// TemperatureSet: the admin chose it, so it goes to every model (one that
	// refuses it is learned once and gets none after that).
	Temperature    float64
	TemperatureSet bool
	// Effort is how hard a reasoning model thinks ("" = its own default; see effort.go).
	Effort     string
	MaxRetries int // additional attempts after the first (default 2)
	HTTPClient *http.Client
	// StreamIdle aborts a streamed answer that sends nothing for this long
	// (default 3 minutes). A stream that keeps flowing may take as long as it needs.
	StreamIdle time.Duration
	// RequestTimeout bounds an answer that is not streamed (default 10 minutes).
	RequestTimeout time.Duration

	// sleep is replaced in tests.
	sleep func(context.Context, time.Duration) error

	// Learned from the provider's refusals (reasoning models): no
	// temperature, and max_completion_tokens instead of max_tokens.
	noTemperature, completionTokens atomic.Bool
	noStreamOptions                 atomic.Bool  // the gateway refuses stream_options: never sent again
	noStore                         atomic.Bool  // the Responses gateway refuses "store": never sent again
	maxOutput                       atomic.Int64 // Anthropic: the model's own output limit
	// The efforts the model refused: none at all, or its two highest.
	noEffort, noMaxEffort, noXHighEffort atomic.Bool
}

func (c *Client) api() string {
	switch c.API {
	case APIAnthropic, APIResponses:
		return c.API
	}
	return APIOpenAI
}

// Part is one element of a multimodal message.
type Part struct {
	Type     string // "text" or "image_url"
	Text     string
	ImageURL string // https:// or data:image/...;base64,...
}

// TextPart and ImagePart build message parts.
func TextPart(s string) Part        { return Part{Type: "text", Text: s} }
func ImagePart(dataURL string) Part { return Part{Type: "image_url", ImageURL: dataURL} }

// Message is one chat message. ReasoningContent is read from responses but is
// never sent back to the API. When Parts is set it replaces Content.
type Message struct {
	Role             string     `json:"role"`
	Content          string     `json:"content,omitempty"`
	Parts            []Part     `json:"-"`
	ReasoningContent string     `json:"reasoning_content,omitempty"`
	Reasoning        string     `json:"reasoning,omitempty"`
	ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID       string     `json:"tool_call_id,omitempty"`
	Name             string     `json:"name,omitempty"`
	// Stable is how much of Content (in bytes) is the same at every request,
	// for APIs that cache explicitly (Anthropic); it is not sent.
	Stable int `json:"-"`
}

// MarshalJSON produces the wire format. "content" is always present (APIs
// reject tool/user messages without it) except for assistant tool-call turns,
// where it is null. Reasoning is never sent.
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

// UnmarshalJSON accepts content as a string or as an array of parts.
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

// Thought returns the model's reasoning text, whichever field the provider used.
func (m Message) Thought() string {
	if m.ReasoningContent != "" {
		return m.ReasoningContent
	}
	return m.Reasoning
}

// Text returns the textual content, including text parts.
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
	Temperature   *float64       `json:"temperature,omitempty"` // 0 is a temperature too: nil leaves it out
	MaxTokens     int            `json:"max_tokens,omitempty"`
	// MaxCompletionTokens replaces MaxTokens for reasoning models.
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

// APIError is a non-2xx answer (or an error object) from the provider.
type APIError struct {
	Status     int
	Body       string
	RetryAfter time.Duration // from the Retry-After header, if any
}

func (e *APIError) Error() string {
	return fmt.Sprintf("llm api error (status %d): %s", e.Status, e.Body)
}

// Temporary reports whether retrying later may help.
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

// statusOverloaded is Anthropic's "overloaded" status.
const statusOverloaded = 529

// ErrPolicyViolation is a request the provider's safety filter refused; another
// provider may still answer it.
var ErrPolicyViolation = errors.New("refused by the provider's safety filter")

// policyMarkers are what providers write when their safety filter or a
// sensitive-word list refuses a request (Google/Gemini, Azure, OpenAI,
// DeepSeek and Chinese gateways).
var policyMarkers = []string{"prohibited use", "prohibited_content", "prohibited content", "sensitive words", "sensitive word",
	"safety filter", "content moderation", "safety guidelines", "content_filter", "content management policy",
	"responsibleaipolicyviolation", "blocked due to safety", "content exists risk", "violates our usage polic", "敏感"}

// IsPolicyViolationText reports whether an error text is a safety-filter refusal.
func IsPolicyViolationText(s string) bool {
	s = strings.ToLower(s)
	for _, m := range policyMarkers {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

// IsPolicyViolation reports whether err is a safety-filter refusal.
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

// filtered turns an empty answer stopped by a safety filter into ErrPolicyViolation.
func filtered(r *Response) error {
	switch strings.ToLower(r.FinishReason) {
	case "content_filter", "safety", "prohibited_content", "blocklist", "spii", "recitation":
		if strings.TrimSpace(r.Message.Content) == "" && len(r.Message.ToolCalls) == 0 {
			return fmt.Errorf("%w (finish_reason %s)", ErrPolicyViolation, r.FinishReason)
		}
	}
	return nil
}

// ErrPartialStream wraps failures that happened after text was already streamed;
// such a request must not be retried transparently.
var ErrPartialStream = errors.New("stream interrupted after output started")

const maxResponseBytes = 16 << 20

// maxStreamBytes bounds a streamed answer: a server that never stops (or
// is not a model at all) cannot fill the memory.
const maxStreamBytes = 64 << 20

// ErrTooLarge is an answer past maxStreamBytes.
var ErrTooLarge = errors.New("the answer is too large")

// maxStreamText bounds the text, reasoning and tool arguments of one answer
// (far beyond any model's output limit).
const maxStreamText = 4 << 20

// deltaGate hands a streamed answer to onDelta: every delta at first, then,
// for a long one, each time it grew by a few hundred bytes, so a flood of
// tiny deltas costs no more than a few big ones.
type deltaGate struct {
	on      func(string)
	n, last int
}

func (g *deltaGate) push(text string) {
	if errorPrefix(text) { // an error passed on as text is never shown
		return
	}
	if g.on == nil { // an answer asked for whole, from a gateway that streams it anyway
		return
	}
	g.n++
	if g.n > 500 && len(text)-g.last < 256 {
		return
	}
	g.last = len(text)
	g.on(text)
}

// capped reads r until maxStreamBytes, then fails.
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

// MaxRetryWait is the longest Retry-After a client waits for before retrying.
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

// NewHTTPClient is the client for model APIs. It has no overall time limit:
// long answers stream for minutes (a big file written in one tool call, a
// reasoning model). The headers must arrive within 10 minutes; a stream that
// stalls is cut by Client.StreamIdle, the caller's context ends everything else.
func NewHTTPClient() *http.Client {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.ResponseHeaderTimeout = 10 * time.Minute
	return &http.Client{Transport: t}
}

const (
	defaultStreamIdle     = 3 * time.Minute
	defaultRequestTimeout = 10 * time.Minute
)

// idleBody closes a response body that sends nothing for too long, so a
// stalled stream fails instead of hanging.
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

// contextMarkers are what providers write when the conversation no longer fits the model.
var contextMarkers = []string{"context_length_exceeded", "context length", "maximum context", "context window", "too many tokens",
	"prompt is too long", "input is too long", "tokens exceeds", "exceeds the maximum", "reduce the length", "string_above_max_length",
	"request too large", "maximum input length"}

// IsSizeRateLimit reports a request that a provider refused for its size per
// minute («Request too large … on tokens per minute (TPM): Limit 6000,
// Requested 18400», Groq's way, with a 413): the model's window is not the
// problem, this provider's allowance is, and another provider may take it.
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

// IsContextOverflow reports an error saying the request is too long for the model.
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

// Chat sends the conversation and returns the first choice.
func (c *Client) Chat(ctx context.Context, messages []Message, tools []Tool) (*Message, error) {
	return Chat(ctx, c, messages, tools)
}

// Complete performs a request. The conversation is fixed first so every API
// takes it (normalizeTranscript); an error a gateway passes on as the
// answer's text is an error; a refusal of how tool calls and results are
// paired is tried once more with them written as text. Transient failures
// (network errors, 429, 5xx) are retried with exponential backoff, unless
// streamed text was already shown.
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
		// Some strict gateways reject stream_options; try once more without it.
		var api *APIError
		if body.StreamOptions != nil && errors.As(err, &api) && api.Status == http.StatusBadRequest && strings.Contains(api.Body, "stream_options") {
			body.StreamOptions = nil
			c.noStreamOptions.Store(true)
			attempt--
			continue
		}
		// A model that refuses a parameter (reasoning models): drop or rename it and go again.
		if errors.As(err, &api) && api.Status == http.StatusBadRequest && (c.learnEffort(api.Body, c.wireEffort(req)) ||
			(c.api() == APIOpenAI && c.learn(api.Body)) || (c.api() == APIAnthropic && c.learnAnthropic(api.Body)) ||
			(c.api() == APIResponses && c.learnResponses(api.Body))) {
			attempt--
			continue
		}
		lastErr = err
		if ctx.Err() != nil || errors.Is(err, ErrPartialStream) || !isRetryable(err) || attempt == maxRetries || retryAfter > MaxRetryWait {
			break // a long Retry-After is not worth waiting for: let the caller decide
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

// encode builds the request body in the client's wire format.
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
	return json.Marshal(body)
}

// reasoningModel: OpenAI's reasoning models take no temperature and
// max_completion_tokens instead of max_tokens (o1, o3, o4-mini, gpt-5…).
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

// learn reads a refusal of a parameter; it reports whether the request
// should go again without it.
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

// endpoint is where requests go. A base URL may carry a query (Azure's
// api-version); the path goes before it.
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

// azureAPIVersion is used for Azure deployment URLs that name none.
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

// authorize sets the headers of the client's API.
func (c *Client) authorize(req *http.Request) {
	switch {
	case c.api() == APIAnthropic:
		req.Header.Set("anthropic-version", anthropicVersion)
		if c.APIKey != "" {
			req.Header.Set("x-api-key", c.APIKey)
			if DetectAPI(c.BaseURL) != APIAnthropic { // a relay: some read the usual bearer token
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

// IsRetryable reports whether an error is worth retrying (here or on another provider).
func IsRetryable(err error) bool { return isRetryable(err) }

func isRetryable(err error) bool {
	if errors.Is(err, ErrPartialStream) || IsPolicyViolation(err) {
		return false // the same request is refused again
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

// envelopeError reads the {"code": 402, "msg": "..."} some gateways answer with
// when they refuse a request before running it (kie.ai: 401 a bad key, 402 no
// credits left, 429 too many calls, 455 under maintenance), often with HTTP 200.
// It returns nil for anything else, an ordinary answer included.
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

// ErrBadAnswer is an answer that came but holds nothing usable: empty, or not
// in the format the API speaks.
var ErrBadAnswer = errors.New("empty or unreadable answer")

// IsUnavailable reports an error that means the model could not be reached or
// said nothing usable (a refused connection, a stall, a stream cut off, an empty
// or unreadable answer), as opposed to a refusal or a mistake of the request.
func IsUnavailable(err error) bool {
	return errors.Is(err, errTransport) || errors.Is(err, ErrPartialStream) || errors.Is(err, ErrBadAnswer)
}

func (c *Client) once(ctx context.Context, payload []byte, onDelta func(string)) (*Response, time.Duration, error) {
	idle := c.StreamIdle
	if idle <= 0 {
		idle = defaultStreamIdle
	}
	var (
		answered func() bool // stops the wait for a streamed answer's first byte
		silent   atomic.Bool // the wait ran out
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
		// A provider that takes the request and then says nothing is given up
		// on as soon as a stalled stream would be, so the next one is tried.
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
	// A gateway that streams even an answer asked for whole (some do) is read as
	// a stream all the same; the text then reaches no callback.
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
		onDelta(r.Message.Content) // the server ignored stream=true
	}
	return r, 0, nil
}

// readStream assembles a server-sent-events completion.
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
		calls     = map[int]*ToolCall{} // by order of arrival
		slot      = map[int]int{}       // a delta's index → its call
		started   bool
		gotData   bool
		done      bool // [DONE] came
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
			continue // tolerate keep-alive comments and vendor extensions
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
				// Some servers send every call whole without an index (all 0):
				// a new id starts a new call.
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
				if td.Function.Name != tc.Function.Name { // a repeated whole name is not doubled
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
		out.FinishReason = "length" // the stream stopped mid-call: its arguments may be cut, so it is not run
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

// parseRetryAfter reads the delay in seconds (bounded to a day). Callers
// decide whether it is short enough to wait for (MaxRetryWait).
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

// Models lists the models the endpoint offers (GET /models, in both the
// OpenAI and the Anthropic API), sorted.
func (c *Client) Models(ctx context.Context) ([]string, error) {
	u := joinURL(c.BaseURL, "/models")
	if c.api() == APIAnthropic {
		u = strings.TrimSuffix(c.endpoint(), "/messages") + "/models?limit=1000" // the whole list, not its first page
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
		Models []struct { // Ollama's own list
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
