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

	type route struct{ env, url string }
	routes := map[string]route{
		"bale":   {"BALE_BOT_TOKEN", "https://tapi.bale.ai/bot%s/sendMessage"},
		"eitaa":  {"EITAA_BOT_TOKEN", "https://eitaayar.ir/api/%s/sendMessage"},
		"rubika": {"RUBIKA_BOT_TOKEN", "https://botapi.rubika.ir/v3/%s/sendMessage"},
	}
	if messenger == "eita" {
		messenger = "eitaa"
	}
	rt, ok := routes[messenger]
	if !ok {
		return "", fmt.Errorf("unknown messenger %q: bale, eitaa or rubika", messenger)
	}
	if token == "" {
		token = os.Getenv(rt.env)
	}
	if token == "" {
		return "", fmt.Errorf("%s is not set: macron config set %s <the bot token> and restart", rt.env, rt.env)
	}
	if strings.TrimSpace(chatID) == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	out, err := sendHTTPJSON(ctx, client, fmt.Sprintf(rt.url, token), payload, token)
	if err != nil {
		return "", err
	}
	return messenger + ": " + out, nil
}

func sendHTTPJSON(ctx context.Context, client *http.Client, endpoint string, payload []byte, secret string) (string, error) {
	scrub := func(err error) error {
		if secret == "" {
			return err
		}
		return fmt.Errorf("%s", strings.ReplaceAll(err.Error(), secret, "•••"))
	}
	sctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(sctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", scrub(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", scrub(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	text := strings.TrimSpace(string(body))
	if resp.StatusCode/100 != 2 {
		return "", scrub(fmt.Errorf("%s: %s", resp.Status, text))
	}
	var reply struct {
		OK          *bool  `json:"ok"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if json.Unmarshal(body, &reply) == nil && ((reply.OK != nil && !*reply.OK) || strings.EqualFold(reply.Status, "ERROR")) {
		return "", scrub(fmt.Errorf("refused: %s", text))
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	return "sent (" + resp.Status + "): " + text, nil
}
