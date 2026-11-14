package integration

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func providerStore(t *testing.T) *store.Store {
	t.Helper()
	st, e := store.Open(context.Background(), filepath.Join(t.TempDir(), "providers.db"), store.Options{SecretKey: "provider-fixture-secret"})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	return st
}
func TestProviderWireSettings(t *testing.T) {
	for _, api := range []string{llm.APIOpenAI, llm.APIResponses, llm.APIAnthropic} {
		t.Run(api, func(t *testing.T) {
			st := providerStore(t)
			var body map[string]any
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "GET" {
					json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "z"}, {"id": "a"}}})
					return
				}
				if api == llm.APIAnthropic {
					if r.Header.Get("X-Api-Key") != "key-fixture" {
						t.Error("missing Anthropic key")
					}
				} else if r.Header.Get("Authorization") != "Bearer key-fixture" {
					t.Error("missing bearer key")
				}
				json.NewDecoder(r.Body).Decode(&body)
				w.Header().Set("Content-Type", "application/json")
				switch api {
				case llm.APIResponses:
					fmt.Fprint(w, `{"id":"r","status":"completed","model":"gpt-5","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]}]}`)
				case llm.APIAnthropic:
					fmt.Fprint(w, `{"id":"m","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"pong"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`)
				default:
					fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"model":"gpt-5"}`)
				}
			}))
			defer server.Close()
			temp := 0.7
			p := store.Provider{Name: "fixture", BaseURL: server.URL + "/v1", API: api, APIKey: "key-fixture", Model: "gpt-5", Temperature: &temp, Effort: llm.EffortHigh, NoStream: true, ContextWindow: 32000, Enabled: true, Vision: false, Priority: 10}
			if api == llm.APIAnthropic {
				p.Model = "claude-sonnet-4-5"
			}
			id, e := st.AddProvider(context.Background(), p)
			if e != nil {
				t.Fatal(e)
			}
			p.ID = id
			manager, e := providers.New(context.Background(), st, server.Client(), nil)
			if e != nil {
				t.Fatal(e)
			}
			got, e := manager.Models(context.Background(), p)
			if e != nil || strings.Join(got, ",") != "a,z" {
				t.Fatal(got, e)
			}
			var delta string
			res, e := manager.Complete(context.Background(), llm.Request{Messages: []llm.Message{{Role: "user", Parts: []llm.Part{{Type: "text", Text: "hello"}, {Type: "image_url", ImageURL: "data:image/png;base64,fixture"}}}}, OnDelta: func(s string) { delta += s }, OwnTemperature: true})
			if e != nil || res.Message.Content != "pong" || delta != "pong" {
				t.Fatal(res, e, delta)
			}
			if body["stream"] != false {
				t.Fatal("no-stream setting ignored", body)
			}
			if strings.Contains(fmt.Sprint(body), "data:image/png") {
				t.Fatal("vision disabled but image sent")
			}
			if api != llm.APIAnthropic && body["temperature"] != temp {
				t.Fatal("temperature not sent", body)
			}
			if api == llm.APIResponses {
				if _, ok := body["reasoning"]; !ok {
					t.Fatal("missing reasoning", body)
				}
			} else if api == llm.APIOpenAI && body["reasoning_effort"] != "high" {
				t.Fatal("missing reasoning effort", body)
			}
			persisted, e := st.Provider(context.Background(), id)
			if e != nil || persisted.ContextWindow != 32000 || persisted.Priority != 10 || !persisted.NoStream {
				t.Fatal("settings not persisted", persisted, e)
			}
		})
	}
}
func TestProviderValidationAndMissingUpdate(t *testing.T) {
	base := store.Provider{Name: "fixture", BaseURL: "https://host.invalid/v1/chat/completions", Model: "test", Enabled: true}
	normalized, e := providers.Normalize(base)
	if e != nil || normalized.BaseURL != "https://host.invalid/v1" || normalized.API != llm.APIOpenAI {
		t.Fatal(normalized, e)
	}
	for _, change := range []func(*store.Provider){func(p *store.Provider) { p.Model = "" }, func(p *store.Provider) { v := math.NaN(); p.Temperature = &v }, func(p *store.Provider) { p.BaseURL = "https://user:secret@host.invalid/v1" }, func(p *store.Provider) { p.ContextWindow = 1 }, func(p *store.Provider) { p.API = "unknown" }, func(p *store.Provider) { p.APIKey = "bad\nkey" }} {
		p := base
		change(&p)
		if _, e := providers.Normalize(p); e == nil {
			t.Fatal("invalid setting accepted")
		}
	}
	st := providerStore(t)
	base.ID = 999
	if e := st.UpdateProvider(context.Background(), base); !errors.Is(e, store.ErrNotFound) {
		t.Fatal(e)
	}
}
func TestProviderCrossProcessSwitchAndCredentialRotation(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "shared.db")
	st, e := store.Open(ctx, path, store.Options{SecretKey: "shared"})
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	other, e := store.Open(ctx, path, store.Options{SecretKey: "shared"})
	if e != nil {
		t.Fatal(e)
	}
	defer other.Close()
	var mu sync.Mutex
	var calls []string
	managerHTTP := &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		key := r.Header.Get("Authorization")
		mu.Lock()
		calls = append(calls, fmt.Sprint(body["model"])+":"+key)
		mu.Unlock()
		if key == "Bearer broken" {
			return response(401, `{"error":{"message":"invalid key"}}`), nil
		}
		return response(200, `{"choices":[{"message":{"role":"assistant","content":"pong"}}]}`), nil
	})}
	a, e := providers.Configure(ctx, st, "a", "https://a.invalid/v1", "broken", "model-a")
	if e != nil {
		t.Fatal(e)
	}
	b, e := providers.Configure(ctx, st, "b", "https://b.invalid/v1", "fresh-b", "model-b")
	if e != nil {
		t.Fatal(e)
	}
	manager, e := providers.New(ctx, st, managerHTTP, nil)
	if e != nil {
		t.Fatal(e)
	}
	req := llm.Request{Messages: []llm.Message{{Role: "user", Content: "probe"}}}
	other.SetSetting(ctx, store.SettingActiveProvider, fmt.Sprint(a))
	other.SetSetting(ctx, store.SettingFailover, "false")
	for i := 0; i < 3; i++ {
		if _, e := manager.Complete(ctx, req); e == nil {
			t.Fatal("broken credential succeeded")
		}
	}
	for _, s := range manager.Snapshot() {
		if s.ID == a && !s.Cooling {
			t.Fatal("provider did not cool")
		}
	}
	p, _ := other.Provider(ctx, a)
	p.APIKey = "fresh-a"
	p.Model = "rotated-a"
	other.UpdateProvider(ctx, *p)
	if e := manager.Reload(ctx); e != nil {
		t.Fatal(e)
	}
	for _, s := range manager.Snapshot() {
		if s.ID == a && s.Cooling {
			t.Fatal("corrected credentials still cooled")
		}
	}
	if _, e := manager.Complete(ctx, req); e != nil {
		t.Fatal(e)
	}
	other.SetSetting(ctx, store.SettingActiveProvider, fmt.Sprint(b))
	if _, e := manager.Complete(ctx, req); e != nil {
		t.Fatal(e)
	}
	if calls[len(calls)-1] != "model-b:Bearer fresh-b" {
		t.Fatal("external active selection ignored", calls)
	}
	if _, e := manager.Complete(providers.Only(ctx, a), req); e != nil {
		t.Fatal(e)
	}
	if calls[len(calls)-1] != "rotated-a:Bearer fresh-a" {
		t.Fatal(calls)
	}
	raw, _ := os.ReadFile(path)
	if strings.Contains(string(raw), "fresh-a") {
		t.Fatal("credential persisted in plain text")
	}
}

func TestProviderErrorsHideKeysAndModelListingNeedsNoModel(t *testing.T) {
	ctx := context.Background()
	st := providerStore(t)
	client := &http.Client{Transport: roundtrip(func(r *http.Request) (*http.Response, error) {
		if r.Method == "GET" {
			return response(200, `{"data":[{"id":"one"}]}`), nil
		}
		return response(401, `{"error":{"message":"invalid key secret-fixture-value"}}`), nil
	})}
	manager, e := providers.New(ctx, st, client, nil)
	if e != nil {
		t.Fatal(e)
	}
	models, e := manager.Models(ctx, store.Provider{BaseURL: "https://host.invalid/v1", APIKey: "secret-fixture-value"})
	if e != nil || len(models) != 1 {
		t.Fatal(models, e)
	}
	_, e = manager.Test(ctx, store.Provider{Name: "test", Model: "test", BaseURL: "https://host.invalid/v1", APIKey: "secret-fixture-value"})
	if e == nil || strings.Contains(e.Error(), "secret-fixture-value") {
		t.Fatal("key exposed", e)
	}
	var api *llm.APIError
	if !errors.As(e, &api) || api.Status != 401 {
		t.Fatal("error classification lost", e)
	}
}

func TestLiveProviderManagerQwenSettings(t *testing.T) {
	key := liveModelKey(t)
	ctx := context.Background()
	st := providerStore(t)
	temp := 0.4
	p := store.Provider{Name: "live-qwen", BaseURL: liveModelURL(), APIKey: strings.TrimSpace(string(key)), Model: liveModelName(), Enabled: true, NoStream: true, Temperature: &temp, ContextWindow: 32000}
	id, e := st.AddProvider(ctx, p)
	if e != nil {
		t.Fatal(e)
	}
	_ = st.SetSetting(ctx, store.SettingActiveProvider, fmt.Sprint(id))
	manager, e := providers.New(ctx, st, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	var delta string
	ctx, cancel := context.WithTimeout(ctx, 80*time.Second)
	defer cancel()
	res, e := manager.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "user", Content: "فقط عبارت PROVIDER_PROBE_939 را بنویس."}}, MaxTokens: 2048, OwnTemperature: true, OnDelta: func(s string) { delta += s }})
	if e != nil || res == nil || !strings.Contains(res.Message.Content, "PROVIDER_PROBE_939") || delta != res.Message.Content {
		t.Fatal(res, e, delta)
	}
	t.Log("live Qwen saved-provider selection, explicit temperature and no-stream callback passed")
}
