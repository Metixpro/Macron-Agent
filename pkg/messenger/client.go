package messenger

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type Message struct {
	ID, ChatID, UserID, Text string
	Private                  bool
}
type Transport interface {
	Poll(context.Context, string) ([]Message, string, error)
	Send(context.Context, string, string) error
}
type Client struct {
	Platform, Token, BaseURL string
	HTTP                     *http.Client
}

func New(platform, token string) (*Client, error) {
	var base string
	switch platform {
	case "bale":
		base = "https://tapi.bale.ai/bot"
	case "rubika":
		base = "https://botapi.rubika.ir/v3/"
	default:
		return nil, fmt.Errorf("%s has no supported inbound Bot API; Eitaa's Eitaayar integration supports outbound messages only", platform)
	}
	if strings.TrimSpace(token) == "" {
		return nil, fmt.Errorf("%s bot token is required", platform)
	}
	return &Client{Platform: platform, Token: token, BaseURL: base + token, HTTP: &http.Client{Timeout: 40 * time.Second}}, nil
}
func (c *Client) call(ctx context.Context, method string, payload, out any) error {
	ctx, cancel := context.WithTimeout(ctx, 35*time.Second)
	defer cancel()
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.BaseURL, "/")+"/"+method, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("invalid messenger endpoint")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %s", c.Platform, strings.ReplaceAll(err.Error(), c.Token, "<token>"))
	}
	defer resp.Body.Close()
	var reply struct {
		OK          *bool           `json:"ok"`
		Status      string          `json:"status"`
		Result      json.RawMessage `json:"result"`
		Data        json.RawMessage `json:"data"`
		Description string          `json:"description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&reply); err != nil {
		return fmt.Errorf("%s returned invalid JSON (HTTP %d)", c.Platform, resp.StatusCode)
	}
	valid := reply.OK != nil && *reply.OK
	if c.Platform == "rubika" {
		valid = reply.Status == "OK"
	}
	if resp.StatusCode/100 != 2 || !valid {
		reason := reply.Status
		if reply.Description != "" {
			reason = reply.Description
		}
		reason = strings.ReplaceAll(reason, c.Token, "<token>")
		return fmt.Errorf("%s refused %s (HTTP %d): %s", c.Platform, method, resp.StatusCode, textutil.TruncateRunes(reason, 200, "…"))
	}
	raw := reply.Result
	if c.Platform == "rubika" {
		raw = reply.Data
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("%s returned invalid %s data", c.Platform, method)
		}
	}
	return nil
}
func (c *Client) Check(ctx context.Context) error { return c.call(ctx, "getMe", map[string]any{}, nil) }
func (c *Client) Send(ctx context.Context, chat, text string) error {
	runes := []rune(text)
	for len(runes) > 0 {
		n := min(3500, len(runes))
		if err := c.call(ctx, "sendMessage", map[string]string{"chat_id": chat, "text": string(runes[:n])}, nil); err != nil {
			return err
		}
		runes = runes[n:]
	}
	return nil
}
func (c *Client) Poll(ctx context.Context, cursor string) ([]Message, string, error) {
	if c.Platform == "bale" {
		offset := int64(0)
		if cursor != "" {
			var err error
			offset, err = strconv.ParseInt(cursor, 10, 64)
			if err != nil {
				return nil, cursor, fmt.Errorf("invalid Bale update cursor")
			}
		}
		var updates []struct {
			ID      int64 `json:"update_id"`
			Message *struct {
				ID   int64  `json:"message_id"`
				Text string `json:"text"`
				From *struct {
					ID  int64 `json:"id"`
					Bot bool  `json:"is_bot"`
				} `json:"from"`
				Chat struct {
					ID   int64  `json:"id"`
					Type string `json:"type"`
				} `json:"chat"`
			} `json:"message"`
		}
		if err := c.call(ctx, "getUpdates", map[string]any{"offset": offset, "timeout": 25, "allowed_updates": []string{"message"}}, &updates); err != nil {
			return nil, cursor, err
		}
		var messages []Message
		for _, u := range updates {
			if u.ID < offset {
				continue
			}
			if u.ID >= offset {
				offset = u.ID + 1
			}
			m := u.Message
			if m == nil || m.From == nil || m.From.Bot || m.Text == "" {
				continue
			}
			messages = append(messages, Message{ID: fmt.Sprint(m.ID), ChatID: fmt.Sprint(m.Chat.ID), UserID: fmt.Sprint(m.From.ID), Text: m.Text, Private: m.Chat.Type == "private"})
		}
		return messages, fmt.Sprint(offset), nil
	}
	var updates struct {
		Next    string `json:"next_offset_id"`
		Updates []struct {
			Type       string `json:"type"`
			Chat       string `json:"chat_id"`
			NewMessage *struct {
				ID         string `json:"message_id"`
				Text       string `json:"text"`
				Sender     string `json:"sender_id"`
				SenderType string `json:"sender_type"`
			} `json:"new_message"`
		} `json:"updates"`
	}
	payload := map[string]any{"limit": 100}
	if cursor != "" {
		payload["offset_id"] = cursor
	}
	if err := c.call(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, cursor, err
	}
	var messages []Message
	for _, u := range updates.Updates {
		m := u.NewMessage
		if u.Type != "NewMessage" || m == nil || m.Sender == "" || m.Text == "" || m.SenderType != "User" {
			continue
		}
		messages = append(messages, Message{ID: m.ID, ChatID: u.Chat, UserID: m.Sender, Text: m.Text})
	}
	if updates.Next == "" {
		updates.Next = cursor
	}
	return messages, updates.Next, nil
}
func (c *Client) Private(ctx context.Context, m Message) (bool, error) {
	if c.Platform == "bale" {
		return m.Private, nil
	}
	var reply struct {
		Chat struct {
			Type string `json:"chat_type"`
			User string `json:"user_id"`
		} `json:"chat"`
	}
	if err := c.call(ctx, "getChat", map[string]string{"chat_id": m.ChatID}, &reply); err != nil {
		return false, err
	}
	return reply.Chat.Type == "User" && reply.Chat.User == m.UserID, nil
}

func (c *Client) CursorKey() string {
	sum := sha256.Sum256([]byte(c.Token))
	return fmt.Sprintf("messenger_cursor:%s:%x", c.Platform, sum[:12])
}

func (c *Client) SetAPIBase(base string) error {
	u, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("messenger API base must be an http(s) URL without credentials, query or fragment")
	}
	c.BaseURL = strings.TrimRight(base, "/")
	if c.Platform == "bale" {
		c.BaseURL += "/bot" + c.Token
	} else {
		c.BaseURL += "/v3/" + c.Token
	}
	return nil
}
