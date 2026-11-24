package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// LocalServer describes a locally running OpenAI-compatible model server
// auto-discovered on loopback (Ollama, LM Studio, llama.cpp, vLLM).
type LocalServer struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	BaseURL string   `json:"baseUrl"`
	Models  []string `json:"models"`
}

type localTarget struct {
	id        string
	name      string
	baseURL   string
	modelsURL string
	ollamaURL string
}

var defaultLocalTargets = []localTarget{
	{id: "ollama", name: "Ollama", baseURL: "http://127.0.0.1:11434/v1", modelsURL: "http://127.0.0.1:11434/v1/models", ollamaURL: "http://127.0.0.1:11434/api/tags"},
	{id: "lmstudio", name: "LM Studio", baseURL: "http://127.0.0.1:1234/v1", modelsURL: "http://127.0.0.1:1234/v1/models"},
	{id: "llamacpp", name: "llama.cpp", baseURL: "http://127.0.0.1:8080/v1", modelsURL: "http://127.0.0.1:8080/v1/models"},
	{id: "vllm", name: "vLLM", baseURL: "http://127.0.0.1:8000/v1", modelsURL: "http://127.0.0.1:8000/v1/models"},
}

// DetectLocalServers concurrently probes well-known local inference servers on
// loopback with a fast timeout and returns every reachable server with its models.
func DetectLocalServers(ctx context.Context) []LocalServer {
	return detectLocalTargets(ctx, defaultLocalTargets)
}

func detectLocalTargets(ctx context.Context, targets []localTarget) []LocalServer {
	probeCtx, cancel := context.WithTimeout(ctx, 900*time.Millisecond)
	defer cancel()

	// Dedicated direct loopback client (never routes localhost probes through an external proxy).
	client := &http.Client{
		Timeout: 850 * time.Millisecond,
		Transport: &http.Transport{
			Proxy:                 nil,
			DisableKeepAlives:     true,
			ResponseHeaderTimeout: 750 * time.Millisecond,
		},
	}

	var (
		mu  sync.Mutex
		out []LocalServer
		wg  sync.WaitGroup
	)
	for _, tgt := range targets {
		wg.Add(1)
		go func(t localTarget) {
			defer wg.Done()
			models, ok := probeOpenAIModels(probeCtx, client, t.modelsURL)
			if !ok && t.ollamaURL != "" {
				models, ok = probeOllamaTags(probeCtx, client, t.ollamaURL)
			}
			if !ok {
				return
			}
			mu.Lock()
			out = append(out, LocalServer{
				ID:      t.id,
				Name:    t.name,
				BaseURL: t.baseURL,
				Models:  models,
			})
			mu.Unlock()
		}(tgt)
	}
	wg.Wait()

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func probeOpenAIModels(ctx context.Context, client *http.Client, endpoint string) ([]string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, false
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, false
	}
	// Require a valid OpenAI /models envelope ("data" array) so arbitrary web servers on :8080 don't false-positive.
	if payload.Data == nil {
		return nil, false
	}
	seen := map[string]bool{}
	var models []string
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.ID)
		if id != "" && !seen[id] {
			seen[id] = true
			models = append(models, id)
		}
	}
	sort.Strings(models)
	return models, true
}

func probeOllamaTags(ctx context.Context, client *http.Client, endpoint string) ([]string, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, false
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		return nil, false
	}
	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Models == nil {
		return nil, false
	}
	seen := map[string]bool{}
	var models []string
	for _, m := range payload.Models {
		name := strings.TrimSpace(m.Name)
		if name != "" && !seen[name] {
			seen[name] = true
			models = append(models, name)
		}
	}
	sort.Strings(models)
	return models, true
}
