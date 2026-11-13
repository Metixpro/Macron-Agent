package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/config"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ToolIranMessenger = "iran_messenger"

func messengerEndpoint(platform, token, method string) (string, error) {
	base := map[string]string{"bale": "https://tapi.bale.ai", "rubika": "https://botapi.rubika.ir", "telegram": "https://api.telegram.org", "eitaa": "https://eitaayar.ir"}[platform]
	if base == "" {
		return "", fmt.Errorf("unsupported messenger")
	}
	if platform != "eitaa" {
		if configured := config.ToolEnvValue("MACRON_" + strings.ToUpper(platform) + "_API"); configured != "" {
			base = strings.TrimRight(configured, "/")
		}
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("messenger API base must be an http(s) URL without credentials, query or fragment")
	}
	prefix := "/bot"
	if platform == "rubika" {
		prefix = "/v3/"
	} else if platform == "eitaa" {
		prefix = "/api/"
	}
	return base + prefix + url.PathEscape(token) + "/" + method, nil
}

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

	routes := map[string]string{
		"bale": "BALE_BOT_TOKEN", "eitaa": "EITAA_BOT_TOKEN", "rubika": "RUBIKA_BOT_TOKEN",
	}
	if messenger == "eita" {
		messenger = "eitaa"
	}
	rt, ok := routes[messenger]
	if !ok {
		return "", fmt.Errorf("unknown messenger %q: bale, eitaa or rubika", messenger)
	}
	if token == "" {
		token = config.ToolEnvValue(rt)
	}
	if token == "" {
		return "", fmt.Errorf("%s is not set: configure it through connections or macron config set %s <the bot token>", rt, rt)
	}
	if strings.TrimSpace(chatID) == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	payload, _ := json.Marshal(map[string]any{"chat_id": chatID, "text": text})
	endpoint, err := messengerEndpoint(messenger, token, "sendMessage")
	if err != nil {
		return "", err
	}
	out, err := sendHTTPJSON(ctx, client, endpoint, payload, token)
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
	body, err := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
	if err != nil {
		return "", scrub(fmt.Errorf("reading messenger response: %w", err))
	}
	text := strings.TrimSpace(string(body))
	if resp.StatusCode/100 != 2 {
		return "", scrub(fmt.Errorf("%s: %s", resp.Status, text))
	}
	var reply struct {
		OK          *bool  `json:"ok"`
		Status      string `json:"status"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(body, &reply); err != nil {
		return "", fmt.Errorf("messenger returned an invalid JSON response (HTTP %d)", resp.StatusCode)
	}
	if reply.OK == nil && reply.Status == "" {
		return "", fmt.Errorf("messenger did not confirm delivery (HTTP %d)", resp.StatusCode)
	}
	if (reply.OK != nil && !*reply.OK) || (reply.Status != "" && !strings.EqualFold(reply.Status, "OK")) {
		return "", scrub(fmt.Errorf("refused: %s", text))
	}
	if len(text) > 300 {
		text = text[:300] + "…"
	}
	if secret != "" {
		text = strings.ReplaceAll(text, secret, "•••")
	}
	return "sent (" + resp.Status + "): " + text, nil
}
