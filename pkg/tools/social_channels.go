package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const ToolExternalMessenger = "external_messenger"

func (r *Registry) SendExternalMessenger(ctx context.Context, platform, webhookURL, token, channel, to, text string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text is required")
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	switch platform {
	case "discord":
		u := webhookURL
		if u == "" {
			u = os.Getenv("DISCORD_WEBHOOK_URL")
		}
		if u == "" {
			return "", fmt.Errorf("DISCORD_WEBHOOK_URL is not set: macron config set DISCORD_WEBHOOK_URL <the webhook URL>")
		}
		payload, _ := json.Marshal(map[string]any{
			"content": text,
		})
		return sendHTTPPost(ctx, client, u, payload, nil)

	case "slack":
		u := webhookURL
		if u == "" {
			u = os.Getenv("SLACK_WEBHOOK_URL")
		}
		if u != "" {
			payload, _ := json.Marshal(map[string]any{
				"text": text,
			})
			return sendHTTPPost(ctx, client, u, payload, nil)
		}
		if token == "" {
			token = os.Getenv("SLACK_BOT_TOKEN")
		}
		if token == "" {
			return "", fmt.Errorf("Slack is not set up: macron config set SLACK_WEBHOOK_URL <webhook>, or SLACK_BOT_TOKEN for chat.postMessage with a channel")
		}
		payload, _ := json.Marshal(map[string]any{
			"channel": channel,
			"text":    text,
		})
		headers := map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		}
		return sendHTTPPost(ctx, client, "https://slack.com/api/chat.postMessage", payload, headers)

	case "whatsapp":
		if token == "" {
			token = os.Getenv("WHATSAPP_TOKEN")
		}
		phoneID := os.Getenv("WHATSAPP_PHONE_ID")
		if token == "" || phoneID == "" {
			return "", fmt.Errorf("whatsapp requires WHATSAPP_TOKEN and WHATSAPP_PHONE_ID")
		}
		endpoint := fmt.Sprintf("https://graph.facebook.com/v19.0/%s/messages", phoneID)
		payload, _ := json.Marshal(map[string]any{
			"messaging_product": "whatsapp",
			"to":                to,
			"type":              "text",
			"text": map[string]string{
				"body": text,
			},
		})
		headers := map[string]string{
			"Authorization": "Bearer " + token,
			"Content-Type":  "application/json",
		}
		return sendHTTPPost(ctx, client, endpoint, payload, headers)
	}

	return "", fmt.Errorf("unknown platform: %s (supported: discord, slack, whatsapp)", platform)
}

func sendHTTPPost(ctx context.Context, client *http.Client, endpoint string, payload []byte, headers map[string]string) (string, error) {
	secret := endpoint
	if a := headers["Authorization"]; a != "" {
		secret = strings.TrimPrefix(a, "Bearer ")
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", errors.New("the request could not be built")
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s", strings.ReplaceAll(strings.ReplaceAll(err.Error(), endpoint, "the endpoint"), secret, "•••"))
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	text := strings.ReplaceAll(strings.TrimSpace(string(body)), secret, "•••")
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("%s: %s", resp.Status, text)
	}
	var reply struct {
		OK    *bool  `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(body, &reply) == nil && reply.OK != nil && !*reply.OK {
		if reply.Error != "" {
			text = reply.Error
		}
		return "", fmt.Errorf("refused: %s", text)
	}
	return "sent (" + resp.Status + ")", nil
}
