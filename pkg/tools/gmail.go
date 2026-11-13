package tools

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

func gmailConfigured() bool {
	return firstEnv("MACRON_GMAIL_ACCESS_TOKEN", "MACRON_GMAIL_REFRESH_TOKEN") != ""
}

func (r *Registry) gmailToken(ctx context.Context, refresh bool) (string, error) {
	if !refresh {
		if token := firstEnv("MACRON_GMAIL_ACCESS_TOKEN"); token != "" {
			return token, nil
		}
	}
	token := firstEnv("MACRON_GMAIL_REFRESH_TOKEN")
	id, secret := firstEnv("MACRON_GMAIL_CLIENT_ID"), firstEnv("MACRON_GMAIL_CLIENT_SECRET")
	if token == "" || id == "" || secret == "" {
		return "", fmt.Errorf("Gmail OAuth requires MACRON_GMAIL_ACCESS_TOKEN or MACRON_GMAIL_CLIENT_ID, MACRON_GMAIL_CLIENT_SECRET and MACRON_GMAIL_REFRESH_TOKEN")
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}, "client_id": {id}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://oauth2.googleapis.com/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := r.cloudClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("Gmail OAuth token refresh failed")
	}
	defer resp.Body.Close()
	var reply struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&reply); err != nil {
		return "", fmt.Errorf("Gmail OAuth returned an invalid response")
	}
	if resp.StatusCode/100 != 2 || reply.AccessToken == "" {
		return "", fmt.Errorf("Gmail OAuth refresh refused (HTTP %d, %s)", resp.StatusCode, reply.Error)
	}
	return reply.AccessToken, nil
}

func (r *Registry) Gmail(ctx context.Context, action, to, subject, body, query, messageID string, limit int) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	token, err := r.gmailToken(ctx, false)
	if err != nil {
		return "", err
	}
	refreshed := false
	call := func(method, path string, payload any, out any) error {
		var data []byte
		if payload != nil {
			var err error
			data, err = json.Marshal(payload)
			if err != nil {
				return err
			}
		}
		for attempt := 0; attempt < 2; attempt++ {
			req, err := http.NewRequestWithContext(ctx, method, "https://gmail.googleapis.com/gmail/v1/users/me/"+path, bytes.NewReader(data))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			if payload != nil {
				req.Header.Set("Content-Type", "application/json")
			}
			resp, err := r.cloudClient().Do(req)
			if err != nil {
				return fmt.Errorf("Gmail request failed: %s", strings.ReplaceAll(err.Error(), token, "•••"))
			}
			raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			if readErr != nil {
				return fmt.Errorf("reading Gmail response: %w", readErr)
			}
			if resp.StatusCode == 401 && !refreshed && os.Getenv("MACRON_GMAIL_REFRESH_TOKEN") != "" {
				refreshed = true
				token, err = r.gmailToken(ctx, true)
				if err != nil {
					return err
				}
				continue
			}
			if resp.StatusCode/100 != 2 {
				return fmt.Errorf("Gmail refused the request (HTTP %d); check OAuth scopes and account access", resp.StatusCode)
			}
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("Gmail returned an invalid response")
			}
			return nil
		}
		return fmt.Errorf("Gmail authentication failed")
	}
	switch action {
	case "send", "":
		from := firstEnv("MACRON_GMAIL_FROM", "MACRON_SMTP_USER")
		if from == "" {
			return "", fmt.Errorf("MACRON_GMAIL_FROM is required for Gmail sending")
		}
		raw, _, _, err := emailMessage(from, to, subject, body)
		if err != nil {
			return "", err
		}
		var sent struct {
			ID string `json:"id"`
		}
		if err := call(http.MethodPost, "messages/send", map[string]string{"raw": base64.RawURLEncoding.EncodeToString(raw)}, &sent); err != nil {
			return "", err
		}
		if sent.ID == "" {
			return "", fmt.Errorf("Gmail did not confirm delivery")
		}
		return "Gmail sent message " + sent.ID, nil
	case "search", "list":
		if limit < 1 {
			limit = 10
		}
		limit = min(limit, 20)
		if query == "" {
			query = "in:inbox"
		}
		var list struct {
			Messages []struct {
				ID string `json:"id"`
			} `json:"messages"`
		}
		if err := call(http.MethodGet, "messages?"+url.Values{"q": {query}, "maxResults": {fmt.Sprint(limit)}}.Encode(), nil, &list); err != nil {
			return "", err
		}
		var lines []string
		for _, item := range list.Messages {
			var msg gmailMessage
			if err := call(http.MethodGet, "messages/"+url.PathEscape(item.ID)+"?format=metadata", nil, &msg); err != nil {
				return "", err
			}
			lines = append(lines, msg.summary())
		}
		return fmt.Sprintf("%d Gmail messages:\n%s", len(lines), strings.Join(lines, "\n")), nil
	case "read":
		if messageID == "" {
			return "", fmt.Errorf("message_id is required; search Gmail first")
		}
		var msg gmailMessage
		if err := call(http.MethodGet, "messages/"+url.PathEscape(messageID)+"?format=full", nil, &msg); err != nil {
			return "", err
		}
		return msg.summary() + "\n" + textutil.TruncateRunes(gmailBody(msg.Payload), 12000, "\n… (message shortened)"), nil
	}
	return "", fmt.Errorf("unknown email action %q: send, search, list or read", action)
}

type gmailPart struct {
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []gmailPart `json:"parts"`
}
type gmailMessage struct {
	ID      string    `json:"id"`
	Snippet string    `json:"snippet"`
	Payload gmailPart `json:"payload"`
}

func (m gmailMessage) summary() string {
	var headers []string
	for _, h := range m.Payload.Headers {
		switch strings.ToLower(h.Name) {
		case "from", "subject", "date":
			headers = append(headers, h.Name+": "+h.Value)
		}
	}
	return textutil.TruncateRunes("ID: "+m.ID+" | "+strings.Join(headers, " | ")+"\n"+m.Snippet, 1800, "…")
}
func gmailBody(p gmailPart) string {
	var plain, html []string
	var walk func(gmailPart, int)
	walk = func(p gmailPart, depth int) {
		if depth > 20 {
			return
		}
		if p.Body.Data != "" {
			raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(p.Body.Data, "="))
			if err == nil {
				switch p.MimeType {
				case "text/plain":
					plain = append(plain, string(raw))
				case "text/html":
					html = append(html, textutil.StripHTML(string(raw)))
				}
			}
		}
		for _, part := range p.Parts {
			walk(part, depth+1)
		}
	}
	walk(p, 0)
	if len(plain) > 0 {
		return strings.Join(plain, "\n")
	}
	return strings.Join(html, "\n")
}
