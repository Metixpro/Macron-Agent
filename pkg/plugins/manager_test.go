package plugins

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPToggleReconnectsAndPreservesActiveSession(t *testing.T) {
	var starts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			ID     any    `json:"id"`
			Method string `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
			http.Error(w, "bad request", 400)
			return
		}
		if in.ID == nil {
			w.WriteHeader(204)
			return
		}
		var result any
		switch in.Method {
		case "initialize":
			starts.Add(1)
			result = map[string]any{"protocolVersion": mcpProtocol, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{map[string]any{"name": "echo", "description": "Echo test value", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
		case "tools/call":
			result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "MCP_RECONNECTED_141"}}}
		default:
			t.Errorf("unexpected MCP method %s", in.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": in.ID, "result": result})
	}))
	defer srv.Close()
	m := &Manager{States: &MemoryStates{}, HTTP: srv.Client(), IsAdmin: func(string) bool { return true }}
	defer m.Close()
	keys, err := m.AddServers(context.Background(), []ServerConfig{{ID: "fixture", URL: srv.URL, Transport: "http"}})
	if err != nil {
		t.Fatal(err)
	}
	key := keys[0]
	for cycle := 0; cycle < 3; cycle++ {
		info := m.Wait(key, 5*time.Second)
		if !info.Ready() || len(info.Tools) != 1 {
			t.Fatalf("cycle %d: %+v", cycle, info)
		}
		out, err := m.Call(context.Background(), "owner", info.Tools[0].Name, map[string]any{})
		if err != nil || out != "MCP_RECONNECTED_141" {
			t.Fatal(out, err)
		}
		before := starts.Load()
		m.Reload(context.Background())
		if !m.Wait(key, time.Second).Ready() || starts.Load() != before {
			t.Fatal("reload replaced an active session")
		}
		if err := m.SetEnabled(context.Background(), key, false); err != nil {
			t.Fatal(err)
		}
		if info := m.Wait(key, time.Second); info.Enabled || info.Status != StatusOff || len(m.Tools()) != 0 {
			t.Fatalf("disabled: %+v", info)
		}
		if err := m.SetEnabled(context.Background(), key, true); err != nil {
			t.Fatal(err)
		}
	}
	if !m.Wait(key, 5*time.Second).Ready() || starts.Load() != 4 {
		t.Fatalf("expected four connections, got %d", starts.Load())
	}
	if err := m.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if len(m.Tools()) != 0 {
		t.Fatal("deleted server tools remain")
	}
}
