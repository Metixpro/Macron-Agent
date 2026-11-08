package providers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), store.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

type fakeProvider struct {
	name   string
	status atomic.Int32
	calls  atomic.Int32
	mu     sync.Mutex
	last   map[string]any
	body   string // the error text (default "down")
}

func (f *fakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls.Add(1)
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.last = body
	f.mu.Unlock()
	if s := f.status.Load(); s != 0 {
		f.mu.Lock()
		msg := f.body
		f.mu.Unlock()
		if msg == "" {
			msg = "down"
		}
		http.Error(w, msg, int(s))
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"model": f.name,
		"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "from " + f.name}}},
		"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 3, "total_tokens": 13}})
}

func addProvider(t *testing.T, s *store.Store, name string, priority int, vision bool) (*fakeProvider, int64) {
	f := &fakeProvider{name: name}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	id, err := s.AddProvider(context.Background(), store.Provider{Name: name, BaseURL: srv.URL, APIKey: "key-" + name + "-1234", Model: "m", Priority: priority, Enabled: true, Vision: vision})
	if err != nil {
		t.Fatal(err)
	}
	return f, id
}

var ask = llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}

func TestNoProvider(t *testing.T) {
	s := openStore(t)
	m, _ := New(context.Background(), s, nil, nil)
	if _, err := m.Complete(context.Background(), ask); !errors.Is(err, ErrNoProvider) || m.Configured() {
		t.Fatalf("got %v", err)
	}
}

func TestActiveSwitchAndFailover(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a, aID := addProvider(t, s, "alpha", 10, true)
	b, bID := addProvider(t, s, "beta", 20, false)

	var usage []string
	var mu sync.Mutex
	m, err := New(ctx, s, nil, func(p string, u llm.Usage, failed bool) {
		mu.Lock()
		usage = append(usage, p)
		mu.Unlock()
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.ActiveID() != aID || !m.SupportsVision() {
		t.Fatalf("default active must be the highest priority: %d", m.ActiveID())
	}
	resp, err := m.Complete(ctx, ask)
	if err != nil || resp.Message.Content != "from alpha" || resp.Provider != "alpha" {
		t.Fatalf("%+v %v", resp, err)
	}

	// Switch the active provider.
	s.SetSetting(ctx, store.SettingActiveProvider, "0")
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(bID))
	if resp, _ := m.Complete(ctx, ask); resp.Provider != "beta" || m.SupportsVision() {
		t.Fatalf("switch: %+v", resp)
	}

	// beta fails → alpha takes over.
	b.status.Store(503)
	resp, err = m.Complete(ctx, ask)
	if err != nil || resp.Provider != "alpha" {
		t.Fatalf("failover: %+v %v", resp, err)
	}
	if b.calls.Load() < 2 { // first call worked, then the failed attempt(s)
		t.Errorf("beta should have been tried first")
	}
	p, _ := s.Provider(ctx, bID)
	if p.Failures == 0 || p.LastError == "" {
		t.Errorf("failure not recorded: %+v", p)
	}

	// Failover can be disabled.
	s.SetSetting(ctx, store.SettingFailover, "false")
	if _, err := m.Complete(ctx, ask); err == nil {
		t.Fatal("without failover the active provider's error must surface")
	}
	s.SetSetting(ctx, store.SettingFailover, "true")

	// All down → last error.
	a.status.Store(500)
	if _, err := m.Complete(ctx, ask); err == nil {
		t.Fatal("expected an error when every provider fails")
	}
	a.status.Store(0)
	b.status.Store(0)

	// A disabled provider is never used, even when selected.
	p.Enabled = false
	s.UpdateProvider(ctx, *p)
	m.Reload(ctx)
	if resp, _ := m.Complete(ctx, ask); resp.Provider != "alpha" || m.ActiveID() != aID {
		t.Fatalf("disabled provider used: %+v", resp)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(usage) == 0 {
		t.Error("usage callback never called")
	}
}

func TestClientErrorsDoNotFailOverForBadRequestsAfterStreaming(t *testing.T) {
	if !failoverWorthy(&llm.APIError{Status: 401}) || !failoverWorthy(&llm.APIError{Status: 503}) || failoverWorthy(&llm.APIError{Status: 422}) {
		t.Error("failoverWorthy classification")
	}
	// Streamed text is cumulative, so another provider can simply start over.
	if !failoverWorthy(llm.ErrPartialStream) {
		t.Error("a broken stream fails over too")
	}
}

func TestRateLimitFailsOverAtOnceAndCoolsDown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a, aID := addProvider(t, s, "alpha", 10, true)
	b, _ := addProvider(t, s, "beta", 20, true)
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(aID))
	m, _ := New(ctx, s, nil, nil)
	ask := llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}}

	a.status.Store(429) // rate limited, no Retry-After
	start := time.Now()
	resp, err := m.Complete(ctx, ask)
	if err != nil || resp.Provider != "beta" {
		t.Fatalf("a rate limit must go straight to the next provider: %+v %v", resp, err)
	}
	if took := time.Since(start); took > 900*time.Millisecond || a.calls.Load() != 1 {
		t.Errorf("no waiting or retrying on the limited provider: %v, %d calls", took, a.calls.Load())
	}
	// While it cools down, the limited provider is not even tried first.
	a.status.Store(0)
	before := a.calls.Load()
	if resp, _ := m.Complete(ctx, ask); resp.Provider != "beta" || a.calls.Load() != before {
		t.Errorf("a rate-limited active provider waits behind healthy ones: %+v", resp)
	}
	// Overloaded too: the last provider still answers.
	b.status.Store(503)
	if resp, err := m.Complete(ctx, ask); err != nil || resp.Provider != "alpha" {
		t.Errorf("a cooling provider is still better than giving up: %+v %v", resp, err)
	}
}

func TestBrokenStreamFailsOverAndRestartsTheText(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"half an ans\"}}]}\n\n"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // the connection dies mid-answer
	}))
	defer broken.Close()
	good := &fakeProvider{name: "good"}
	goodSrv := httptest.NewServer(good)
	defer goodSrv.Close()
	id1, _ := s.AddProvider(ctx, store.Provider{Name: "broken", BaseURL: broken.URL, APIKey: "k", Model: "m", Priority: 1, Enabled: true})
	_, _ = s.AddProvider(ctx, store.Provider{Name: "good", BaseURL: goodSrv.URL, APIKey: "k", Model: "m", Priority: 2, Enabled: true})
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(id1))
	m, _ := New(ctx, s, nil, nil)
	var seen []string
	resp, err := m.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "hi"}}, OnDelta: func(t string) { seen = append(seen, t) }})
	if err != nil || resp.Provider != "good" || resp.Message.Content != "from good" {
		t.Fatalf("%+v %v", resp, err)
	}
	if strings.Join(seen, "|") != "half an ans||from good" {
		t.Errorf("the stream restarts on the next provider: %q", seen)
	}
}

func TestCooldownSkipsBrokenProvider(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a, _ := addProvider(t, s, "alpha", 10, true)
	b, bID := addProvider(t, s, "beta", 20, true)
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(bID))
	m, _ := New(ctx, s, nil, nil)
	b.status.Store(503)
	for i := 0; i < cooldownAfter; i++ {
		m.Complete(ctx, ask)
	}
	before := b.calls.Load()
	snap := m.Snapshot()
	var cooling bool
	for _, x := range snap {
		if x.Name == "beta" {
			cooling = x.Cooling
		}
	}
	if !cooling {
		t.Fatal("beta should be cooling down")
	}
	// The active provider is still tried first (admin's choice), but alpha answers.
	if resp, err := m.Complete(ctx, ask); err != nil || resp.Provider != "alpha" {
		t.Fatalf("%+v %v", resp, err)
	}
	_ = before
	_ = a
}

func TestImagesAreStrippedForNonVisionProviders(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	f, _ := addProvider(t, s, "textonly", 10, false)
	m, _ := New(ctx, s, nil, nil)
	_, err := m.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Parts: []llm.Part{llm.TextPart("what is this?"), llm.ImagePart("data:image/png;base64,AAA")}}}})
	if err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	raw, _ := json.Marshal(f.last)
	if strings.Contains(string(raw), "image_url") || !strings.Contains(string(raw), "cannot see images") {
		t.Fatalf("images must be replaced by a note: %s", raw)
	}
}

func TestTestAndSecrets(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	f, _ := addProvider(t, s, "alpha", 10, true)
	m, _ := New(ctx, s, nil, nil)
	p := m.Snapshot()[0].Provider
	res, err := m.Test(ctx, p)
	if err != nil || res.Reply != "from alpha" || res.Latency <= 0 {
		t.Fatalf("%+v %v", res, err)
	}
	f.status.Store(401)
	if _, err := m.Test(ctx, p); err == nil {
		t.Fatal("a failing provider must fail the test")
	}
	if _, err := m.Test(ctx, store.Provider{BaseURL: "ftp://x"}); err == nil {
		t.Fatal("bad URL")
	}
	sec := strings.Join(m.Secrets(), ",")
	if !strings.Contains(sec, "key-alpha-1234") || !strings.Contains(sec, "127.0.0.1") {
		t.Errorf("secrets: %s", sec)
	}
	_ = time.Second
}

func itoa(n int64) string { return strconvFormat(n) }

func strconvFormat(n int64) string { return strconv.FormatInt(n, 10) }

func TestSafetyRefusalFailsOverWithoutHurtingTheProvider(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":{"message":"input contains sensitive words"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(refusing.Close)
	aID, _ := s.AddProvider(ctx, store.Provider{Name: "strict", BaseURL: refusing.URL, APIKey: "k-strict-1234", Model: "m", Priority: 10, Enabled: true})
	b, _ := addProvider(t, s, "relaxed", 20, true)
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(aID))
	m, _ := New(ctx, s, nil, nil)
	for i := 0; i < cooldownAfter+1; i++ {
		resp, err := m.Complete(ctx, ask)
		if err != nil || resp.Provider != "relaxed" {
			t.Fatalf("a refusal goes to the next provider: %+v %v", resp, err)
		}
	}
	if b.calls.Load() != int32(cooldownAfter+1) {
		t.Errorf("calls: %d", b.calls.Load())
	}
	for _, e := range m.candidates(context.Background()) {
		if e.p.Name == "strict" && !e.coolUntil.IsZero() {
			t.Error("refusals must not bench a healthy provider")
		}
	}
	// When every provider refuses, the caller can tell.
	b.status.Store(400)
	b2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "blocked by the safety filter", http.StatusForbidden)
	}))
	t.Cleanup(b2.Close)
	s2 := openStore(t)
	s2.AddProvider(ctx, store.Provider{Name: "only", BaseURL: b2.URL, APIKey: "k-only-1234", Model: "m", Priority: 1, Enabled: true})
	m2, _ := New(ctx, s2, nil, nil)
	if _, err := m2.Complete(ctx, ask); !llm.IsPolicyViolation(err) {
		t.Errorf("last refusal: %v", err)
	}
}

func TestPreferredProviderPerRequest(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	addProvider(t, s, "free", 10, false)
	vip, vipID := addProvider(t, s, "vip", 20, true)
	m, _ := New(ctx, s, nil, nil)

	if resp, _ := m.Complete(ctx, ask); resp.Provider != "free" {
		t.Fatalf("no preference: the active one: %+v", resp)
	}
	pctx := Prefer(ctx, vipID)
	if resp, _ := m.Complete(pctx, ask); resp.Provider != "vip" || !m.VisionFor(pctx) || m.VisionFor(ctx) {
		t.Fatalf("preferred: %+v", resp)
	}
	if p, _ := m.ActiveFor(pctx); p.ID != vipID || m.ActiveID() == vipID {
		t.Errorf("the panel's active provider stays: %+v", p)
	}
	// The preferred one fails: the others take over.
	vip.status.Store(503)
	if resp, err := m.Complete(pctx, ask); err != nil || resp.Provider != "free" {
		t.Fatalf("failover: %+v %v", resp, err)
	}
	// A disabled or unknown preference falls back to the active provider.
	if p, _ := m.ActiveFor(Prefer(ctx, 9999)); p.Name != "free" {
		t.Errorf("unknown: %+v", p)
	}
	if Prefer(ctx, 0) != ctx || Preferred(ctx) != 0 {
		t.Error("0 changes nothing")
	}
}

// When the first provider says the conversation is too long and the next one
// is only busy, the caller hears about the length (it can shorten the
// conversation; waiting would not help), and a request refused as invalid does
// not count against the provider's health.
func TestTheTellingErrorWins(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a, _ := addProvider(t, s, "alpha", 10, false)
	b, _ := addProvider(t, s, "beta", 20, false)
	m, err := New(ctx, s, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	a.status.Store(400)
	a.mu.Lock()
	a.body = `{"error":{"message":"This model's maximum context length is 128000 tokens"}}`
	a.mu.Unlock()
	b.status.Store(503)
	_, err = m.Complete(ctx, ask)
	if !llm.IsContextOverflow(err) {
		t.Fatalf("the overflow is reported, not the busy provider: %v", err)
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, e := range m.entries {
		if e.p.Name == "alpha" && e.streak != 0 {
			t.Errorf("an invalid request does not make alpha look sick: streak %d", e.streak)
		}
	}
}

// Groq's way of a per-minute allowance too small for the request is a 413
// «Request too large … on tokens per minute (TPM)». It is not the model's
// window: the next provider takes the request at once, and the limited one is
// left alone for a minute instead of failing every request first.
func TestATooLargeRequestFailsOverAndASizeRateLimitCoolsDown(t *testing.T) {
	ctx := context.Background()
	s := openStore(t)
	a, aID := addProvider(t, s, "alpha", 10, true)
	b, _ := addProvider(t, s, "beta", 20, true)
	s.SetSetting(ctx, store.SettingActiveProvider, itoa(aID))
	m, _ := New(ctx, s, nil, nil)

	a.status.Store(413)
	a.mu.Lock()
	a.body = `{"error":{"message":"Request too large for model ` + "`x`" + ` in organization on tokens per minute (TPM): Limit 6000, Requested 18400","type":"tokens","code":"rate_limit_exceeded"}}`
	a.mu.Unlock()
	resp, err := m.Complete(ctx, ask)
	if err != nil || resp.Provider != "beta" {
		t.Fatalf("a request one provider calls too large goes to the next: %+v %v", resp, err)
	}
	before := a.calls.Load()
	if resp, _ := m.Complete(ctx, ask); resp.Provider != "beta" || a.calls.Load() != before {
		t.Errorf("the provider with a per-minute limit is not tried again for a minute: %+v (%d calls)", resp, a.calls.Load())
	}

	// A real overflow (a smaller window) goes on to a provider with a bigger one too,
	// and when none takes it, the overflow is what the caller hears.
	a2, _ := addProvider(t, s, "gamma", 5, true)
	a2.status.Store(413)
	a2.mu.Lock()
	a2.body = `{"error":{"message":"This model's maximum context length is 32768 tokens"}}`
	a2.mu.Unlock()
	m.Reload(ctx)
	if resp, err := m.Complete(ctx, ask); err != nil || resp.Provider != "beta" {
		t.Fatalf("a 413 from the first goes on: %+v %v", resp, err)
	}
	b.status.Store(413)
	b.mu.Lock()
	b.body = `{"error":{"message":"This model's maximum context length is 8192 tokens"}}`
	b.mu.Unlock()
	a.status.Store(413)
	if _, err := m.Complete(ctx, ask); !llm.IsContextOverflow(err) {
		t.Fatalf("when none takes it, the overflow is reported (the caller folds the conversation): %v", err)
	}
}

func TestIsSizeRateLimit(t *testing.T) {
	groq := &llm.APIError{Status: 413, Body: `{"error":{"message":"Request too large for model qwen on tokens per minute (TPM): Limit 6000, Requested 18400","code":"rate_limit_exceeded"}}`}
	window := &llm.APIError{Status: 413, Body: `{"error":{"message":"maximum context length is 8192 tokens"}}`}
	perDay := &llm.APIError{Status: 429, Body: `Rate limit reached for tokens per day (TPD)`}
	if !llm.IsSizeRateLimit(groq) || llm.IsSizeRateLimit(window) || !llm.IsSizeRateLimit(perDay) || llm.IsSizeRateLimit(errors.New("x")) {
		t.Error("only a per-minute or per-day allowance is a size rate limit, not a window")
	}
	if d := cooldownFor(groq); d < time.Minute {
		t.Errorf("a per-minute allowance rests for a minute: %v", d)
	}
	if d := cooldownFor(window); d != 0 {
		t.Errorf("a window is no reason to rest: %v", d)
	}
}
