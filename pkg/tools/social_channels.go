package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

const ToolExternalMessenger = "external_messenger"

// SendExternalMessenger delivers messages across Discord, Slack, and WhatsApp.
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
			return "", fmt.Errorf("discord webhook URL required (pass webhook_url or set DISCORD_WEBHOOK_URL)")
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
			return "", fmt.Errorf("slack requires webhook_url or SLACK_BOT_TOKEN")
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
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(sctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("platform returned status %d: %s", resp.StatusCode, string(body))
	}
	return fmt.Sprintf("✅ Message sent to %s (HTTP %d).", endpoint, resp.StatusCode), nil
}
