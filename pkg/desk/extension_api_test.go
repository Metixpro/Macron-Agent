package desk

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/websocket"
)

func TestChromeExtensionBridgeLifecycle(t *testing.T) {
	srv := New(&testFeaturesHost{dataDir: t.TempDir(), projDir: t.TempDir()})
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	srv.addr = strings.TrimPrefix(ts.URL, "http://")

	st0 := srv.extBridge().status(srv.addr)
	if st0.Connected || st0.PairingToken == "" {
		t.Fatalf("unexpected initial extension status: %+v", st0)
	}

	// 1. Reject invalid pairing token
	wsURLBad := "ws://" + srv.addr + "/api/browser/extension/ws?ext_token=wrong"
	if _, err := websocket.Dial(wsURLBad, "", "chrome-extension://test-id"); err == nil {
		t.Fatal("expected invalid token to be rejected")
	}

	// 2. Connect with valid pairing token from chrome-extension origin
	wsURL := "ws://" + srv.addr + "/api/browser/extension/ws?ext_token=" + st0.PairingToken
	cfg, err := websocket.NewConfig(wsURL, "chrome-extension://macron-test")
	if err != nil {
		t.Fatalf("NewConfig: %v", err)
	}
	ws, err := websocket.DialConfig(cfg)
	if err != nil {
		t.Fatalf("websocket.DialConfig: %v", err)
	}
	defer ws.Close()

	// Send hello state
	if err := websocket.JSON.Send(ws, map[string]any{
		"type":             "hello",
		"browserName":      "Chrome",
		"extensionVersion": "2.1.0",
		"approvedOrigins":  []string{"https://github.com"},
		"tabs":             []map[string]any{{"id": 7, "url": "https://github.com", "title": "GitHub", "active": true}},
	}); err != nil {
		t.Fatalf("send hello: %v", err)
	}

	// Wait briefly for bridge to ingest hello
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if srv.extBridge().status(srv.addr).BrowserName == "Chrome" {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	st1 := srv.extBridge().status(srv.addr)
	if !st1.Connected || len(st1.Tabs) != 1 || st1.Tabs[0].ID != 7 {
		t.Fatalf("expected connected status with 1 tab, got %+v", st1)
	}

	// 3. Test bidirectional RPC command relay
	go func() {
		var req map[string]any
		if err := websocket.JSON.Receive(ws, &req); err != nil {
			return
		}
		_ = websocket.JSON.Send(ws, map[string]any{
			"id":     req["id"],
			"type":   "reply",
			"ok":     true,
			"result": map[string]any{"echo": req["command"]},
		})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	raw, err := srv.extBridge().Call(ctx, "list_tabs", nil)
	if err != nil {
		t.Fatalf("extBridge.Call: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil || decoded["echo"] != "list_tabs" {
		t.Fatalf("unexpected RPC reply: %s err=%v", string(raw), err)
	}

	// 4. Token rotation disconnects active extension and changes token
	nextTok := srv.extBridge().rotateToken()
	if nextTok == st0.PairingToken {
		t.Fatal("expected rotated token to differ")
	}

	// Ensure status API responds cleanly when authorized
	req := httptest.NewRequest(http.MethodGet, ts.URL+"/api/browser/extension", nil)
	req.AddCookie(&http.Cookie{Name: cookieName, Value: srv.Token()})
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("extensionStatusAPI status=%d body=%s", rec.Code, rec.Body.String())
	}
}
