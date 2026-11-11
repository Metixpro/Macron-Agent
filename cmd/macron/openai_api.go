package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type openAIChatRequest struct {
	Model       string        `json:"model"`
	Messages    []openAIMsg   `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
}

type openAIMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openAIChatResponse struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
	Usage   openAIUsage    `json:"usage"`
}

type openAIChoice struct {
	Index        int       `json:"index"`
	Message      openAIMsg `json:"message"`
	FinishReason string    `json:"finish_reason"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ServeOpenAIChatCompletions handles POST /v1/chat/completions compatible with OpenAI API clients.
func (b *Bot) ServeOpenAIChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req openAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json request body: "+err.Error(), http.StatusBadRequest)
		return
	}

	if len(req.Messages) == 0 {
		http.Error(w, "messages array is required", http.StatusBadRequest)
		return
	}

	var msgs []llm.Message
	for _, m := range req.Messages {
		msgs = append(msgs, llm.Message{
			Role:    m.Role,
			Content: m.Content,
		})
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
	defer cancel()

	resp, err := b.prov.Complete(ctx, llm.Request{
		Messages: msgs,
	})
	if err != nil {
		http.Error(w, "completion error: "+err.Error(), http.StatusInternalServerError)
		return
	}

	out := openAIChatResponse{
		ID:      fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano()),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []openAIChoice{
			{
				Index: 0,
				Message: openAIMsg{
					Role:    "assistant",
					Content: resp.Message.Content,
				},
				FinishReason: "stop",
			},
		},
		Usage: openAIUsage{
			PromptTokens:     resp.Usage.PromptTokens,
			CompletionTokens: resp.Usage.CompletionTokens,
			TotalTokens:      resp.Usage.TotalTokens,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// ServeOpenAIModels handles GET /v1/models.
func (b *Bot) ServeOpenAIModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	data := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"id":       "macron-1.5",
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "macron",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(data)
}
