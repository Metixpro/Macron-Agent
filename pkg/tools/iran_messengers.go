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

const ToolIranMessenger = "iran_messenger"

// SendIranMessenger delivers messages across Bale, Eitaa, and Rubika bots.
func (r *Registry) SendIranMessenger(ctx context.Context, messenger, token, chatID, text string) (string, error) {
	messenger = strings.ToLower(strings.TrimSpace(messenger))
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("text is required")
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	switch messenger {
	case "bale":
		if token == "" {
			token = os.Getenv("BALE_BOT_TOKEN")
		}
		if token == "" {
			return "", fmt.Errorf("bale bot token required (set BALE_BOT_TOKEN or pass token)")
		}
		endpoint := fmt.Sprintf("https://tapi.bale.ai/bot%s/sendMessage", token)
		payload, _ := json.Marshal(map[string]any{
			"chat_id": chatID,
			"text":    text,
		})
		return sendHTTPJSON(ctx, client, endpoint, payload)

	case "eitaa", "eita":
		if token == "" {
			token = os.Getenv("EITAA_BOT_TOKEN")
		}
		if token == "" {
			return "", fmt.Errorf("eitaa token required (set EITAA_BOT_TOKEN or pass token)")
		}
		endpoint := fmt.Sprintf("https://eitaayar.com/api/%s/sendMessage", token)
		payload, _ := json.Marshal(map[string]any{
			"chat_id": chatID,
			"text":    text,
		})
		return sendHTTPJSON(ctx, client, endpoint, payload)

	case "rubika":
		if token == "" {
			token = os.Getenv("RUBIKA_BOT_TOKEN")
		}
		endpoint := "https://messengerg2c4.rubika.ir/"
		payload, _ := json.Marshal(map[string]any{
			"api_version": "5",
			"auth":        token,
			"data": map[string]any{
				"object_guid": chatID,
				"text":        text,
			},
			"method": "sendMessage",
		})
		return sendHTTPJSON(ctx, client, endpoint, payload)
	}

	return "", fmt.Errorf("unknown messenger: %s (supported: bale, eitaa, rubika)", messenger)
}

func sendHTTPJSON(ctx context.Context, client *http.Client, endpoint string, payload []byte) (string, error) {
	sctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(sctx, "POST", endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("messenger returned status %d: %s", resp.StatusCode, string(body))
	}
	return fmt.Sprintf("✅ Message sent successfully (HTTP %d). Response: %s", resp.StatusCode, string(body)), nil
}
