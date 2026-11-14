package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundtrip func(*http.Request) (*http.Response, error)

func (f roundtrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Status: http.StatusText(status), Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}
func TestGmailRefreshSearchReadSend(t *testing.T) {
	t.Setenv("MACRON_GMAIL_ACCESS_TOKEN", "expired")
	t.Setenv("MACRON_GMAIL_REFRESH_TOKEN", "refresh-test")
	t.Setenv("MACRON_GMAIL_CLIENT_ID", "client-test")
	t.Setenv("MACRON_GMAIL_CLIENT_SECRET", "secret-test")
	t.Setenv("MACRON_GMAIL_FROM", "sender@example.invalid")
	refreshes, sent := 0, 0
	r := &tools.Registry{HTTP: &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "oauth2.googleapis.com" {
			_ = req.ParseForm()
			if req.Form.Get("refresh_token") != "refresh-test" {
				t.Error("bad refresh request")
			}
			refreshes++
			return response(200, `{"access_token":"fresh"}`), nil
		}
		if req.URL.Host != "gmail.googleapis.com" {
			t.Fatal(req.URL.Host)
		}
		if req.Header.Get("Authorization") == "Bearer expired" {
			return response(401, `{"error":{}}`), nil
		}
		if req.Header.Get("Authorization") != "Bearer fresh" {
			t.Error("bad token")
		}
		if strings.HasSuffix(req.URL.Path, "/send") {
			var p map[string]string
			_ = json.NewDecoder(req.Body).Decode(&p)
			raw, e := base64.RawURLEncoding.DecodeString(p["raw"])
			if e != nil || !strings.Contains(string(raw), "To: <receiver@example.invalid>") {
				t.Errorf("bad send MIME %q %v", raw, e)
			}
			sent++
			return response(200, `{"id":"sent-id"}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/messages") {
			if req.URL.Query().Get("q") != "in:inbox" {
				t.Error("bad search")
			}
			return response(200, `{"messages":[{"id":"message-id"}]}`), nil
		}
		if req.URL.Query().Get("format") == "full" {
			return response(200, `{"id":"message-id","payload":{"mimeType":"multipart/mixed","parts":[{"mimeType":"text/plain","body":{"data":"aGVsbG8"}}]}}`), nil
		}
		return response(200, `{"id":"message-id","snippet":"preview","payload":{"headers":[{"name":"Subject","value":"test"}]}}`), nil
	})}}
	for _, action := range []string{"search", "read", "send"} {
		out, e := r.Gmail(context.Background(), action, "receiver@example.invalid", "test", "hello", "", "message-id", 5)
		if e != nil {
			t.Fatal(action, e)
		}
		switch action {
		case "search":
			if !strings.Contains(out, "message-id") {
				t.Fatal(out)
			}
		case "read":
			if !strings.Contains(out, "hello") {
				t.Fatal(out)
			}
		case "send":
			if !strings.Contains(out, "sent-id") {
				t.Fatal(out)
			}
		}
	}
	if refreshes != 3 || sent != 1 {
		t.Fatal(refreshes, sent)
	}
}
func TestMessengerRejectsFalseSuccess(t *testing.T) {
	for _, body := range []string{`{"status":"INVALID_INPUT"}`, `<html>login</html>`, `{}`, `{"ok":false}`} {
		r := &tools.Registry{HTTP: &http.Client{Transport: roundtrip(func(*http.Request) (*http.Response, error) { return response(200, body), nil })}}
		out, e := r.SendIranMessenger(context.Background(), "rubika", "test-token", "chat", "test")
		if e == nil || out != "" {
			t.Fatalf("accepted %s: %s %v", body, out, e)
		}
	}
}
