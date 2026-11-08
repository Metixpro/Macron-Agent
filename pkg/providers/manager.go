// Package providers manages several LLM endpoints stored in the database:
// one is active, the others take over automatically when it fails.
package providers

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

// ErrNoProvider means no enabled provider is configured.
var ErrNoProvider = errors.New("no LLM provider is configured")

const (
	cooldownAfter  = 3                // consecutive failures before a provider is skipped
	cooldownPeriod = 90 * time.Second // how long it is skipped
)

type entry struct {
	p      store.Provider
	client *llm.Client

	mu        sync.Mutex
	streak    int
	coolUntil time.Time
}

func (e *entry) cooling(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return now.Before(e.coolUntil)
}

// reset forgets the failures held against the provider.
func (e *entry) reset() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.streak, e.coolUntil = 0, time.Time{}
}

// coolFor skips the provider for d (a rate limit or an overload).
func (e *entry) coolFor(d time.Duration, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if until := now.Add(d); until.After(e.coolUntil) {
		e.coolUntil = until
	}
}

func (e *entry) record(ok bool, now time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if ok {
		e.streak, e.coolUntil = 0, time.Time{}
		return
	}
	if e.streak++; e.streak >= cooldownAfter {
		e.coolUntil = now.Add(cooldownPeriod)
	}
}

// UsageFunc receives token usage per provider.
type UsageFunc func(provider string, usage llm.Usage, failed bool)

// Account is who the requests sent with a context are for: every model call
// made for them (the agent, its sub-agents, tools, the memory) is added up
// on it, and reported to the manager's OnAccount with the provider it went to.
type Account struct {
	UserID int64
	Tier   string

	mu    sync.Mutex
	usage llm.Usage
	calls int
}

type accountKey struct{}

// WithAccount meters the requests sent with the returned context on acc.
func WithAccount(ctx context.Context, acc *Account) context.Context {
	if acc == nil {
		return ctx
	}
	return context.WithValue(ctx, accountKey{}, acc)
}

// AccountOf is the account a context is metered on (nil: none).
func AccountOf(ctx context.Context) *Account {
	acc, _ := ctx.Value(accountKey{}).(*Account)
	return acc
}

func (a *Account) add(u llm.Usage) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.usage, a.calls = a.usage.Add(u), a.calls+1
}

// Usage is what the account's requests used so far.
func (a *Account) Usage() llm.Usage {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.usage
}

// Billable is the tokens the account's requests are charged so far.
func (a *Account) Billable() int { return a.Usage().Billable() }

// Manager is an llm.ChatClient over the providers in the store. Safe for concurrent use.
type Manager struct {
	store   *store.Store
	http    *http.Client
	onUsage UsageFunc
	now     func() time.Time
	// OnAccount, when set, hears each answered call made for an account,
	// with the provider that answered it.
	OnAccount func(acc *Account, p store.Provider, usage llm.Usage)

	mu      sync.RWMutex
	entries []*entry // priority order
}

// New creates a manager and loads the providers.
func New(ctx context.Context, st *store.Store, httpClient *http.Client, onUsage UsageFunc) (*Manager, error) {
	if httpClient == nil {
		httpClient = llm.NewHTTPClient()
	}
	m := &Manager{store: st, http: httpClient, onUsage: onUsage, now: time.Now}
	return m, m.Reload(ctx)
}

// Reload re-reads providers from the database (call after every change).
func (m *Manager) Reload(ctx context.Context) error {
	list, err := m.store.Providers(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := map[int64]*entry{}
	for _, e := range m.entries {
		old[e.p.ID] = e
	}
	entries := make([]*entry, 0, len(list))
	for _, p := range list {
		c := llm.NewClient(p.BaseURL, p.APIKey, p.Model)
		c.HTTPClient, c.API = m.http, API(p)
		c.MaxRetries = 1 // failover to the next provider is faster than long retries
		withTemperature(c, p)
		c.Effort, c.NoStream = p.Effort, p.NoStream
		e := &entry{p: p, client: c}
		if prev, ok := old[p.ID]; ok {
			prev.mu.Lock()
			e.streak, e.coolUntil = prev.streak, prev.coolUntil
			prev.mu.Unlock()
		}
		entries = append(entries, e)
	}
	m.entries = entries
	return nil
}

// ActiveID returns the id of the provider requests go to first (0 if none).
func (m *Manager) ActiveID() int64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e := m.activeLocked(); e != nil {
		return e.p.ID
	}
	return 0
}

// Active returns a copy of the active provider.
func (m *Manager) Active() (store.Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e := m.activeLocked(); e != nil {
		return e.p, true
	}
	return store.Provider{}, false
}

// preferKey carries the provider a request should go to first.
type preferKey struct{}

// Prefer makes the requests sent with the returned context go to provider
// id first (a user's tier has its own model); the others still take over
// when it fails. 0, or a provider that is gone or disabled, leaves the
// panel's active provider.
func Prefer(ctx context.Context, id int64) context.Context {
	if id <= 0 {
		return ctx
	}
	return context.WithValue(ctx, preferKey{}, id)
}

type onlyKey struct{}

// Only makes the requests sent with the returned context go to provider id
// and nowhere else: no failover (a multi-model preset's slot, whose failure
// is reported, not hidden behind another model). A provider that is gone or
// disabled gets ErrNoProvider.
func Only(ctx context.Context, id int64) context.Context {
	return context.WithValue(Prefer(ctx, id), onlyKey{}, id)
}

// Preferred is the provider id Prefer set on ctx (0: none).
func Preferred(ctx context.Context) int64 {
	id, _ := ctx.Value(preferKey{}).(int64)
	return id
}

// ActiveFor returns a copy of the provider requests sent with ctx go to first.
func (m *Manager) ActiveFor(ctx context.Context) (store.Provider, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if e := m.firstLocked(ctx); e != nil {
		return e.p, true
	}
	return store.Provider{}, false
}

// VisionFor reports whether the provider requests sent with ctx go to accepts images.
func (m *Manager) VisionFor(ctx context.Context) bool {
	p, ok := m.ActiveFor(ctx)
	return ok && p.Vision
}

// firstLocked is the provider preferred on ctx, else the active one.
func (m *Manager) firstLocked(ctx context.Context) *entry {
	if id := Preferred(ctx); id > 0 {
		for _, e := range m.entries {
			if e.p.ID == id && e.p.Enabled {
				return e
			}
		}
	}
	return m.activeLocked()
}

func (m *Manager) activeLocked() *entry {
	want, _ := strconv.ParseInt(m.settingOr(store.SettingActiveProvider, "0"), 10, 64)
	var first *entry
	for _, e := range m.entries {
		if !e.p.Enabled {
			continue
		}
		if e.p.ID == want {
			return e
		}
		if first == nil {
			first = e
		}
	}
	return first
}

func (m *Manager) settingOr(key, def string) string {
	if v, ok := m.store.Setting(key); ok {
		return v
	}
	return def
}

// SupportsVision reports whether the active provider accepts images.
func (m *Manager) SupportsVision() bool {
	p, ok := m.Active()
	return ok && p.Vision
}

// Configured reports whether at least one enabled provider exists.
func (m *Manager) Configured() bool {
	_, ok := m.Active()
	return ok
}

// candidates returns the providers to try, the preferred or active one first.
func (m *Manager) candidates(ctx context.Context) []*entry {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if id, ok := ctx.Value(onlyKey{}).(int64); ok {
		for _, e := range m.entries {
			if e.p.ID == id && e.p.Enabled {
				return []*entry{e}
			}
		}
		return nil
	}
	active := m.firstLocked(ctx)
	if active == nil {
		return nil
	}
	out := []*entry{active}
	if !m.store.SettingBool(store.SettingFailover, true) {
		return out
	}
	now := m.now()
	var healthy, cooling []*entry
	for _, e := range append([]*entry{active}, m.entries...) {
		if (e == active && len(healthy)+len(cooling) > 0) || !e.p.Enabled {
			continue
		}
		if e.cooling(now) {
			cooling = append(cooling, e)
			continue
		}
		healthy = append(healthy, e)
	}
	// A rate-limited active provider waits behind healthy ones; a cooling
	// provider is still better than giving up.
	return append(healthy, cooling...)
}

// cooldownFor says how long a provider is skipped after err: rate limits for
// their Retry-After (at least 20 s), overloads briefly; 0 = use the streak rule.
func cooldownFor(err error) time.Duration {
	var api *llm.APIError
	if !errors.As(err, &api) {
		return 0
	}
	if api.Status == http.StatusRequestEntityTooLarge && llm.IsSizeRateLimit(err) {
		return min(max(api.RetryAfter, time.Minute), 5*time.Minute) // a per-minute allowance is back in a minute
	}
	switch api.Status {
	case 429:
		return min(max(api.RetryAfter, 20*time.Second), 5*time.Minute)
	case 503, 529:
		return min(max(api.RetryAfter, 15*time.Second), 2*time.Minute)
	}
	return 0
}

// Complete sends the request to the active provider and fails over to the
// others. Every provider but the last answers fail-fast, so a rate limit or an
// overload moves the request on at once instead of waiting and retrying; the
// user never sees it. Only the last candidate retries.
func (m *Manager) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	cands := m.candidates(ctx)
	if len(cands) == 0 {
		return nil, ErrNoProvider
	}
	var lastErr, keyErr error // keyErr: an overflow or a refusal, which says more than a later busy provider
	for i, e := range cands {
		req := req
		req.FailFast = i < len(cands)-1
		if !e.p.Vision {
			req.Messages = stripImages(req.Messages)
		}
		streamed := false
		if req.OnDelta != nil {
			inner := req.OnDelta
			req.OnDelta = func(s string) { streamed = true; inner(s) }
		}

		start := m.now()
		resp, err := e.client.Complete(ctx, req)
		took := m.now().Sub(start)
		if err == nil {
			e.record(true, m.now())
			if resp.Usage.TotalTokens == 0 && resp.Usage.PromptTokens == 0 && resp.Usage.CompletionTokens == 0 {
				resp.Usage = estimateUsage(req, resp) // a stream that told nothing still costs: quotas and costs need it
			}
			m.report(e, true, took, "", resp.Usage)
			if acc := AccountOf(ctx); acc != nil {
				acc.add(resp.Usage)
				if m.OnAccount != nil {
					m.OnAccount(acc, e.p, resp.Usage)
				}
			}
			resp.Provider = e.p.Name
			return resp, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		if !llm.IsPolicyViolation(err) && !requestError(err) { // a refused or invalid request says nothing about the provider's health
			e.record(false, m.now())
		}
		if keyErr == nil && (llm.IsContextOverflow(err) || llm.IsPolicyViolation(err)) {
			keyErr = err
		}
		if d := cooldownFor(err); d > 0 {
			e.coolFor(d, m.now())
		}
		m.report(e, false, took, err.Error(), llm.Usage{})
		lastErr = err
		if !failoverWorthy(err) {
			return nil, firstOf(keyErr, err)
		}
		if i+1 < len(cands) {
			log.Printf("providers: %q failed (%v), switching to %q", e.p.Name, shortErr(err), cands[i+1].p.Name)
			if streamed && req.OnDelta != nil {
				req.OnDelta("") // the streamed text is cumulative: the next provider starts it again
			}
		}
	}
	return nil, firstOf(keyErr, lastErr)
}

func firstOf(errs ...error) error {
	for _, e := range errs {
		if e != nil {
			return e
		}
	}
	return nil
}

// requestError is a request the server refused as it is (too big, invalid):
// not a sign of a sick provider.
func requestError(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && (api.Status == 400 || api.Status == 413 || api.Status == 422)
}

// estimateUsage guesses the tokens of an answer that came without usage.
func estimateUsage(req llm.Request, resp *llm.Response) llm.Usage {
	in := 0
	for _, msg := range req.Messages {
		in += 4 + estimateText(msg.Text())
		for _, p := range msg.Parts {
			if p.Type == "image_url" {
				in += 1200
			}
		}
	}
	for _, t := range req.Tools {
		in += estimateText(t.Function.Name+t.Function.Description) + 50
	}
	out := estimateText(resp.Message.Content + resp.Message.ReasoningContent)
	for _, tc := range resp.Message.ToolCalls {
		out += 8 + estimateText(tc.Function.Name+tc.Function.Arguments)
	}
	return llm.Usage{PromptTokens: in, CompletionTokens: out, TotalTokens: in + out}
}

// estimateText approximates tokens: about 4 characters each for Latin text, 2 for Persian.
func estimateText(s string) int {
	ascii, other := 0, 0
	for _, r := range s {
		if r < 128 {
			ascii++
		} else {
			other++
		}
	}
	return ascii/4 + (other+1)/2
}

// failoverWorthy reports errors another provider may not have. A stream that
// broke off is retried elsewhere too: streamed text is cumulative, so the next
// provider's answer simply replaces it.
func failoverWorthy(err error) bool {
	if errors.Is(err, llm.ErrPartialStream) || llm.IsPolicyViolation(err) {
		return true // another provider's filter may let it through
	}
	var api *llm.APIError
	if errors.As(err, &api) {
		switch {
		case api.Temporary(), api.Status == 401, api.Status == 402, api.Status == 403, api.Status == 404:
			return true
		case api.Status == 400, api.Status == http.StatusRequestEntityTooLarge:
			// Model-specific rejections (unknown model, unsupported images…) may work elsewhere,
			// and so may a request one provider calls too large (a smaller window, or a
			// per-minute allowance): the next provider may take it. If none does, the
			// overflow is what the caller hears (firstOf(keyErr…)) and it folds the conversation.
			return true
		}
		return false
	}
	return true // transport errors, timeouts, malformed answers
}

func (m *Manager) report(e *entry, ok bool, took time.Duration, errMsg string, usage llm.Usage) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := m.store.RecordProviderResult(ctx, e.p.ID, ok, took, shortErr(errors.New(errMsg))); err != nil {
		log.Printf("providers: cannot record result: %v", err)
	}
	if m.onUsage != nil {
		m.onUsage(e.p.Name, usage, !ok)
	}
}

// ResetHealth forgets what is known of every provider's health (a streak of
// failures, a cooldown): a person pressed «try again», and what failed minutes
// ago may well work now, so every provider is tried as a first choice again. A
// provider that still fails is counted afresh.
func (m *Manager) ResetHealth() {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		e.reset()
	}
}

func shortErr(err error) string {
	s := err.Error()
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}

// stripImages replaces images with a note for providers without vision.
func stripImages(msgs []llm.Message) []llm.Message {
	var out []llm.Message
	changed := false
	for _, msg := range msgs {
		if len(msg.Parts) == 0 {
			out = append(out, msg)
			continue
		}
		var sb strings.Builder
		images := 0
		for _, p := range msg.Parts {
			if p.Type == "image_url" {
				images++
				continue
			}
			sb.WriteString(p.Text)
		}
		if images > 0 {
			changed = true
			fmt.Fprintf(&sb, "\n[%d image(s) were attached, but the current model cannot see images — tell the user briefly]", images)
		}
		msg.Parts, msg.Content = nil, sb.String()
		out = append(out, msg)
	}
	if !changed {
		return msgs
	}
	return out
}

// TestResult is the outcome of a connectivity check.
type TestResult struct {
	Latency time.Duration
	Reply   string
	Model   string
}

// Test sends a tiny request to a provider configuration (saved or not).
func (m *Manager) Test(ctx context.Context, p store.Provider) (*TestResult, error) {
	if err := ValidateBaseURL(p.BaseURL); err != nil {
		return nil, err
	}
	c := llm.NewClient(p.BaseURL, p.APIKey, p.Model)
	c.HTTPClient, c.MaxRetries, c.API = m.http, 0, API(p)
	withTemperature(c, p) // the test goes as the requests will
	c.Effort, c.NoStream = p.Effort, p.NoStream
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := c.Complete(ctx, llm.Request{
		Messages:  []llm.Message{{Role: "user", Content: "Reply with exactly one word: pong"}},
		MaxTokens: 20,
	})
	if err != nil {
		return nil, err
	}
	return &TestResult{Latency: time.Since(start), Reply: strings.TrimSpace(resp.Message.Content), Model: resp.Model}, nil
}

// Models lists the models a provider configuration offers.
func (m *Manager) Models(ctx context.Context, p store.Provider) ([]string, error) {
	if err := ValidateBaseURL(p.BaseURL); err != nil {
		return nil, err
	}
	c := llm.NewClient(p.BaseURL, p.APIKey, p.Model)
	c.HTTPClient, c.API = m.http, API(p)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return c.Models(ctx)
}

// withTemperature gives a client the provider's own temperature, if it has one.
func withTemperature(c *llm.Client, p store.Provider) {
	if p.Temperature != nil {
		c.Temperature, c.TemperatureSet = *p.Temperature, true
	}
}

// API is the wire format a provider speaks: its own choice, else a guess from its address.
func API(p store.Provider) string {
	if p.API == llm.APIOpenAI || p.API == llm.APIAnthropic || p.API == llm.APIResponses {
		return p.API
	}
	return llm.DetectAPI(p.BaseURL)
}

// ValidateBaseURL checks an OpenAI-compatible base URL.
func ValidateBaseURL(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("the address must look like https://host/v1")
	}
	return nil
}

// Snapshot lists providers with their live cooldown state.
type Snapshot struct {
	store.Provider
	Active  bool
	Cooling bool
}

func (m *Manager) Snapshot() []Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	active := m.activeLocked()
	now := m.now()
	out := make([]Snapshot, 0, len(m.entries))
	for _, e := range m.entries {
		out = append(out, Snapshot{Provider: e.p, Active: e == active, Cooling: e.cooling(now)})
	}
	return out
}

// Secrets returns values that must never appear in messages to users.
func (m *Manager) Secrets() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	var out []string
	for _, e := range m.entries {
		if len(e.p.APIKey) >= 8 {
			out = append(out, e.p.APIKey)
		}
		if u, err := url.Parse(e.p.BaseURL); err == nil && u.Host != "" {
			out = append(out, u.Host)
		}
	}
	return out
}
