package main

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
)

const (
	envAPIAddr      = "MACRON_API_ADDR"
	settingAPIToken = "api.token"
	apiModel        = "macron"
	apiMaxBody      = 8 << 20
	apiTimeout      = 5 * time.Minute
)

type apiChatRequest struct {
	Model     string       `json:"model"`
	Messages  []apiMessage `json:"messages"`
	Stream    bool         `json:"stream"`
	MaxTokens int          `json:"max_tokens"`
}

type apiMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func (m apiMessage) text() string {
	var s string
	if json.Unmarshal(m.Content, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(m.Content, &parts) != nil {
		return ""
	}
	var sb strings.Builder
	for _, p := range parts {
		if p.Type == "text" || p.Type == "" {
			sb.WriteString(p.Text)
		}
	}
	return sb.String()
}

func apiError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error"}})
}

func (b *Bot) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", b.apiChat)
	mux.HandleFunc("/chat/completions", b.apiChat)
	mux.HandleFunc("/v1/models", b.apiModels)
	mux.HandleFunc("/models", b.apiModels)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		want := b.secretSetting(r.Context(), settingAPIToken)
		if want == "" || subtle.ConstantTimeCompare([]byte(key), []byte(want)) != 1 {
			apiError(w, http.StatusUnauthorized, "a valid API key is needed: Authorization: Bearer KEY (the key is shown by /dashboard in the bot)")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (b *Bot) apiModels(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{{"id": apiModel, "object": "model", "created": b.started.Unix(), "owned_by": "macron"}}
	for _, s := range b.prov.Snapshot() {
		if s.Provider.Enabled {
			data = append(data, map[string]any{"id": apiModel + "/" + s.Provider.Name, "object": "model", "created": b.started.Unix(), "owned_by": "macron"})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": data})
}

func (b *Bot) apiRoute(ctx context.Context, model string) (context.Context, bool) {
	name, ok := strings.CutPrefix(model, apiModel+"/")
	if !ok {
		return ctx, true
	}
	for _, s := range b.prov.Snapshot() {
		if strings.EqualFold(s.Provider.Name, name) && s.Provider.Enabled {
			return providers.Only(ctx, s.Provider.ID), true
		}
	}
	return ctx, false
}

func (b *Bot) apiChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		apiError(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req apiChatRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, apiMaxBody)).Decode(&req); err != nil {
		apiError(w, http.StatusBadRequest, "the body is not a chat completion request: "+err.Error())
		return
	}
	var msgs []llm.Message
	for _, m := range req.Messages {
		switch m.Role {
		case "system", "user", "assistant":
			msgs = append(msgs, llm.Message{Role: m.Role, Content: m.text()})
		case "developer":
			msgs = append(msgs, llm.Message{Role: "system", Content: m.text()})
		}
	}
	if len(msgs) == 0 {
		apiError(w, http.StatusBadRequest, "messages is empty")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), apiTimeout)
	defer cancel()
	ctx, ok := b.apiRoute(ctx, req.Model)
	if !ok {
		apiError(w, http.StatusNotFound, "no enabled provider is called "+strings.TrimPrefix(req.Model, apiModel+"/"))
		return
	}
	model := req.Model
	if model == "" {
		model = apiModel
	}
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	created := time.Now().Unix()
	lreq := llm.Request{Messages: msgs, MaxTokens: max(req.MaxTokens, 0)}
	if !req.Stream {
		resp, err := b.prov.Complete(ctx, lreq)
		if err != nil {
			apiError(w, http.StatusBadGateway, "the model did not answer: "+err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id": id, "object": "chat.completion", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": resp.Message.Content},
				"finish_reason": orStop(resp.FinishReason)}},
			"usage": map[string]any{"prompt_tokens": resp.Usage.PromptTokens, "completion_tokens": resp.Usage.CompletionTokens,
				"total_tokens": resp.Usage.PromptTokens + resp.Usage.CompletionTokens},
		})
		return
	}
	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	chunk := func(delta map[string]any, finish any) {
		data, _ := json.Marshal(map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []map[string]any{{"index": 0, "delta": delta, "finish_reason": finish}}})
		_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
		if flusher != nil {
			flusher.Flush()
		}
	}
	chunk(map[string]any{"role": "assistant"}, nil)
	sent := 0
	lreq.OnDelta = func(text string) {
		if len(text) > sent {
			chunk(map[string]any{"content": text[sent:]}, nil)
			sent = len(text)
		}
	}
	resp, err := b.prov.Complete(ctx, lreq)
	if err != nil {
		chunk(map[string]any{"content": "\n[error: " + err.Error() + "]"}, "stop")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		return
	}
	if rest := resp.Message.Content; len(rest) > sent {
		chunk(map[string]any{"content": rest[sent:]}, nil)
	}
	chunk(map[string]any{}, orStop(resp.FinishReason))
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func orStop(reason string) string {
	if reason == "" {
		return "stop"
	}
	return reason
}
