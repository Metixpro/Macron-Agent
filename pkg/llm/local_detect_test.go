package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDetectLocalTargets(t *testing.T) {
	openaiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/models" {
			_, _ = w.Write([]byte(`{"object":"list","data":[{"id":"qwen2.5-coder:14b"},{"id":"llama3.3:70b"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer openaiSrv.Close()

	ollamaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/tags" {
			_, _ = w.Write([]byte(`{"models":[{"name":"deepseek-r1:8b"}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer ollamaSrv.Close()

	// Non-LLM HTTP server returning HTML should not false-positive
	noiseSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body>Not an LLM</body></html>`))
	}))
	defer noiseSrv.Close()

	got := detectLocalTargets(context.Background(), []localTarget{
		{id: "lmstudio", name: "LM Studio", baseURL: openaiSrv.URL + "/v1", modelsURL: openaiSrv.URL + "/v1/models"},
		{id: "ollama", name: "Ollama", baseURL: ollamaSrv.URL + "/v1", modelsURL: ollamaSrv.URL + "/v1/models", ollamaURL: ollamaSrv.URL + "/api/tags"},
		{id: "noise", name: "Noise", baseURL: noiseSrv.URL + "/v1", modelsURL: noiseSrv.URL + "/v1/models"},
	})

	if len(got) != 2 {
		t.Fatalf("expected 2 discovered local servers, got %d: %+v", len(got), got)
	}
	if got[0].ID != "lmstudio" || len(got[0].Models) != 2 {
		t.Fatalf("unexpected first server: %+v", got[0])
	}
	if got[1].ID != "ollama" || len(got[1].Models) != 1 || got[1].Models[0] != "deepseek-r1:8b" {
		t.Fatalf("unexpected second server: %+v", got[1])
	}
}
