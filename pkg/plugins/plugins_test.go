package plugins

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

func newManager(t *testing.T) *Manager {
	t.Helper()
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is not installed")
	}
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: filepath.Join(t.TempDir(), "plugins"), Machine: sb, Version: "test",
		IsAdmin:  func(s string) bool { return s == "tg-1" },
		Reserved: func(name string) bool { return name == "web_search" }}
	t.Cleanup(m.Close)
	return m
}

func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(body))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const depthManifest = `{
  "id": "crypto-trader",
  "name": "دستیار بازار",
  "version": "1.0.0",
  "author": "Metixpro",
  "enabled": true,
  "timeout_seconds": 2,
  "requires_net": false,
  "env": {"API_KEY": "the exchange key"},
  "tools": [
    {"name": "get_crypto_depth", "description": "Order book of a pair",
     "parameters": {"type": "object", "properties": {"symbol": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["symbol"]},
     "test_args": {"symbol": "BTCUSDT"}},
    {"name": "fail_tool", "description": "Always fails", "parameters": {"type": "object", "properties": {}}},
    {"name": "slow_tool", "description": "Too slow", "parameters": {"type": "object", "properties": {}}},
    {"name": "web_search", "description": "Takes a built-in name", "parameters": {"type": "object", "properties": {}}}
  ]
}`

const depthScript = `import json, os, sys, time
req = json.load(sys.stdin)
if req["tool"] == "fail_tool":
    print("exchange is down", file=sys.stderr)
    sys.exit(3)
if req["tool"] == "slow_tool":
    time.sleep(10)
print(json.dumps({"tool": req["tool"], "session": req["session_id"], "args": req["args"],
                  "key": os.environ.get("API_KEY", ""), "other": os.environ.get("OTHER", ""),
                  "cwd_on_path": "" in sys.path}))
`

func TestManifestChecks(t *testing.T) {
	m, err := ParseManifest([]byte(depthManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.TimeoutSeconds != 2 || len(m.Tools) != 4 || m.Tools[0].Name != "get_crypto_depth" {
		t.Fatalf("manifest = %+v", m)
	}
	bad := map[string]string{
		"id":       `{"id": "Bad Id", "tools": [{"name": "x", "description": "d"}]}`,
		"no tools": `{"id": "ok"}`,
		"tool":     `{"id": "ok", "tools": [{"name": "1x", "description": "d"}]}`,
		"env":      `{"id": "ok", "env": {"PATH": "no"}, "tools": [{"name": "x", "description": "d"}]}`,
		"timeout":  `{"id": "ok", "timeout_seconds": 9999, "tools": [{"name": "x", "description": "d"}]}`,
		"memory":   `{"id": "ok", "memory_mb": 4, "tools": [{"name": "x", "description": "d"}]}`,
		"access":   `{"id": "ok", "access": "root", "tools": [{"name": "x", "description": "d"}]}`,
		"dupe":     `{"id": "ok", "tools": [{"name": "x", "description": "d"}, {"name": "x", "description": "d"}]}`,
		"schema":   `{"id": "ok", "tools": [{"name": "x", "description": "d", "parameters": {"type": "object", "properties": {"a": {"description": "` + strings.Repeat("a", 20000) + `"}}}}]}`,
	}
	for name, raw := range bad {
		if _, err := ParseManifest([]byte(raw)); err == nil {
			t.Errorf("%s: a bad manifest was accepted", name)
		}
	}
	d, err := ParseManifest([]byte(`{"id": "ok", "tools": [{"name": "x", "description": "d"}]}`))
	if err != nil || d.TimeoutSeconds != defaultTimeout {
		t.Fatalf("defaults: %+v %v", d, err)
	}
}

func TestManifestForScript(t *testing.T) {
	code := "#!/usr/bin/env python3\n# plugin: weather\n# tool: get_weather\n# description: The weather of a city\n" +
		"# param: city string required The city\n# param: days integer How many days\n# net: true\n# timeout: 20\n# env: WEATHER_KEY the API key\nprint('hi')\n"
	m, err := ManifestForScript("Weather.py", []byte(code))
	if err != nil {
		t.Fatal(err)
	}
	if m.ID != "weather" || m.Entry != "run.py" || !m.RequiresNet || m.TimeoutSeconds != 20 || m.Env["WEATHER_KEY"] != "the API key" {
		t.Fatalf("manifest = %+v", m)
	}
	tool := m.Tools[0]
	if tool.Name != "get_weather" || tool.Description != "The weather of a city" {
		t.Fatalf("tool = %+v", tool)
	}
	if req := requiredOf(tool.Parameters); len(req) != 1 || req[0] != "city" {
		t.Fatalf("required = %v", req)
	}
	plain, err := ManifestForScript("hello-world.sh", []byte("echo hi\n"))
	if err != nil || plain.ID != "hello-world" || plain.Tools[0].Name != "hello_world" {
		t.Fatalf("plain = %+v %v", plain, err)
	}
	if _, err := ManifestForScript("x.exe", []byte("MZ")); err == nil {
		t.Fatal("an .exe was accepted")
	}
}

func TestScriptPluginRuns(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	res, err := m.InstallZip(ctx, makeZip(t, map[string]string{"crypto-trader/plugin.json": depthManifest, "crypto-trader/run.py": depthScript}))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Keys) != 1 || res.Keys[0] != "crypto-trader" {
		t.Fatalf("installed %+v", res)
	}
	names := map[string]bool{}
	for _, tool := range m.Tools() {
		names[tool.Function.Name] = true
	}
	if !names["get_crypto_depth"] || !names["fail_tool"] || names["web_search"] {
		t.Fatalf("tools = %v (a built-in name must stay the built-in's)", names)
	}
	info, _ := m.Get("crypto-trader")
	conflict := false
	for _, ti := range info.Tools {
		if ti.Name == "web_search" {
			conflict = ti.Conflict
		}
	}
	if !conflict || info.Runtime != runtimePython || info.MemoryMB != defaultMemoryMB {
		t.Fatalf("info = %+v", info)
	}

	if err := m.SetEnv(ctx, "crypto-trader", "API_KEY", "s3cret"); err != nil {
		t.Fatal(err)
	}
	if err := m.SetEnv(ctx, "crypto-trader", "OTHER", "x"); err == nil {
		t.Fatal("a variable the plugin does not declare was accepted")
	}
	out, err := m.Call(ctx, "tg-7", "get_crypto_depth", map[string]any{"symbol": "BTCUSDT", "limit": 10})
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		Tool, Session, Key, Other string
		Args                      map[string]any
		CwdOnPath                 bool `json:"cwd_on_path"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output %q: %v", out, err)
	}
	if got.Tool != "get_crypto_depth" || got.Session != "tg-7" || got.Args["symbol"] != "BTCUSDT" || got.Key != "s3cret" || got.Other != "" || got.CwdOnPath {
		t.Fatalf("the plugin saw %+v", got)
	}

	out, err = m.Call(ctx, "tg-7", "fail_tool", nil)
	if err != nil || !strings.Contains(out, "exit 3") || !strings.Contains(out, "exchange is down") {
		t.Fatalf("a failing run: %q %v", out, err)
	}
	start := time.Now()
	out, _ = m.Call(ctx, "tg-7", "slow_tool", nil)
	if !strings.Contains(out, "longer than 2s") || time.Since(start) > 8*time.Second {
		t.Fatalf("a slow run: %q after %s", out, time.Since(start))
	}

	results, err := m.Test(ctx, "crypto-trader")
	if err != nil {
		t.Fatal(err)
	}
	byTool := map[string]TestResult{}
	for _, r := range results {
		byTool[r.Tool] = r
	}
	if r := byTool["get_crypto_depth"]; !r.OK || !strings.Contains(r.Output, "BTCUSDT") {
		t.Fatalf("self-test = %+v", results)
	}
	if r := byTool["fail_tool"]; r.OK {
		t.Fatalf("a failing tool passed the self-test: %+v", r)
	}
}

func TestToggleAccessAndDelete(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	if _, err := m.InstallScript(ctx, "echo.sh", []byte("# tool: say\n# description: Says it back\ncat\n")); err != nil {
		t.Fatal(err)
	}
	if !m.Has("say") || !m.Allowed("say", "tg-9") {
		t.Fatal("a new plugin is on for everyone")
	}
	out, err := m.Call(ctx, "tg-9", "say", map[string]any{"input": "salam"})
	if err != nil || !strings.Contains(out, `"salam"`) {
		t.Fatalf("say = %q %v", out, err)
	}
	if err := m.SetAccess(ctx, "echo", AccessAdmins); err != nil {
		t.Fatal(err)
	}
	if m.Allowed("say", "tg-9") || !m.Allowed("say", "tg-1") || !m.Allowed("say", "tg-1::sub-2") {
		t.Fatal("admins-only access is not enforced")
	}
	if _, err := m.Call(ctx, "tg-9", "say", nil); err == nil {
		t.Fatal("a user ran an admins-only tool")
	}
	if err := m.SetEnabled(ctx, "echo", false); err != nil {
		t.Fatal(err)
	}
	if m.Has("say") || len(m.Tools()) != 0 {
		t.Fatal("a plugin that is off still offers its tools")
	}
	if err := m.Delete(ctx, "echo"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(m.Dir, "echo")); !os.IsNotExist(err) || len(m.List()) != 0 {
		t.Fatal("the plugin's folder is still there")
	}
}

func TestZipIsChecked(t *testing.T) {
	m := newManager(t)
	ctx := context.Background()
	evil := makeZip(t, map[string]string{"p/plugin.json": `{"id": "p", "tools": [{"name": "x", "description": "d"}]}`, "../../escape.txt": "x"})
	if _, err := m.InstallZip(ctx, evil); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("zip slip: %v", err)
	}
	if _, err := m.InstallZip(ctx, makeZip(t, map[string]string{"readme.md": "hi"})); err == nil {
		t.Fatal("a zip without plugin.json was installed")
	}
	if _, err := m.InstallZip(ctx, []byte("not a zip")); err == nil {
		t.Fatal("garbage was installed")
	}
	if _, err := m.InstallZip(ctx, makeZip(t, map[string]string{"plugin.json": `{"id": "../x", "tools": []}`})); err == nil {
		t.Fatal("a bad id was installed")
	}
	two := makeZip(t, map[string]string{
		"bundle/a/plugin.json": `{"id": "a", "tools": [{"name": "tool_a", "description": "d"}]}`, "bundle/a/run.sh": "echo a",
		"bundle/b/plugin.json": `{"id": "b", "tools": [{"name": "tool_b", "description": "d"}]}`, "bundle/b/run.sh": "echo b",
	})
	res, err := m.InstallZip(ctx, two)
	if err != nil || len(res.Keys) != 2 {
		t.Fatalf("two plugins: %+v %v", res, err)
	}
	if out, _ := m.Call(ctx, "tg-1", "tool_b", nil); out != "b" {
		t.Fatalf("tool_b = %q", out)
	}
	entries, _ := os.ReadDir(m.Dir)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			t.Fatalf("a temporary folder was left: %s", e.Name())
		}
	}
}

const fakeStdioServer = `import json, sys
def send(m):
    sys.stdout.write(json.dumps(m) + "\n"); sys.stdout.flush()
print("starting up (a log line on stdout)", flush=True)
for line in sys.stdin:
    m = json.loads(line)
    method, mid = m.get("method"), m.get("id")
    if method == "initialize":
        send({"jsonrpc": "2.0", "id": mid, "result": {"protocolVersion": "2025-06-18", "capabilities": {"tools": {}}, "serverInfo": {"name": "fake-stdio", "version": "1"}}})
    elif method == "tools/list":
        send({"jsonrpc": "2.0", "id": mid, "result": {"tools": [
            {"name": "echo", "description": "Echoes", "inputSchema": {"type": "object", "properties": {"text": {"type": "string"}}, "required": ["text"]}},
            {"name": "broken", "description": "Fails", "inputSchema": {"type": "object"}}]}})
    elif method == "tools/call":
        send({"jsonrpc": "2.0", "id": "srv-1", "method": "ping"})
        pong = json.loads(sys.stdin.readline())
        if pong.get("id") != "srv-1" or "result" not in pong:
            sys.exit(9)
        name, args = m["params"]["name"], m["params"].get("arguments") or {}
        if name == "broken":
            send({"jsonrpc": "2.0", "id": mid, "result": {"isError": True, "content": [{"type": "text", "text": "it broke"}]}})
        else:
            send({"jsonrpc": "2.0", "id": mid, "result": {"content": [{"type": "text", "text": "echo: " + args.get("text", "") + " " + __import__("os").environ.get("FAKE_TOKEN", "")}]}})
    elif mid is not None:
        send({"jsonrpc": "2.0", "id": mid, "error": {"code": -32601, "message": "unknown"}})
`

func TestMCPStdioServer(t *testing.T) {
	m := newManager(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "server.py")
	if err := os.WriteFile(script, []byte(fakeStdioServer), 0o644); err != nil {
		t.Fatal(err)
	}
	m.MCPFile = filepath.Join(dir, "mcp_servers.json")
	cfg := map[string]any{"mcpServers": map[string]any{"fake": map[string]any{"command": "python3", "args": []string{script},
		"env": map[string]string{"FAKE_TOKEN": "tok"}, "access": "everyone"}}}
	data, _ := json.Marshal(cfg)
	if err := os.WriteFile(m.MCPFile, data, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	m.Reload(ctx)
	info := m.Wait("@fake", 20*time.Second)
	if info.Status != StatusReady || !strings.HasPrefix(info.ServerName, "fake-stdio") || len(info.Tools) != 2 || info.Source != SourceFile {
		t.Fatalf("server = %+v", info)
	}
	if !m.Has("fake__echo") {
		t.Fatalf("tools = %v", m.Tools())
	}
	out, err := m.Call(ctx, "tg-5", "fake__echo", map[string]any{"text": "salam"})
	if err != nil || out != "echo: salam tok" {
		t.Fatalf("echo = %q %v", out, err)
	}
	out, err = m.Call(ctx, "tg-5", "fake__broken", nil)
	if err != nil || !strings.Contains(out, "it broke") {
		t.Fatalf("broken = %q %v", out, err)
	}
	if err := m.Delete(ctx, "@fake"); err == nil {
		t.Fatal("a server of mcp_servers.json was deleted from the panel")
	}
	if err := m.SetEnabled(ctx, "@fake", false); err != nil {
		t.Fatal(err)
	}
	if m.Has("fake__echo") {
		t.Fatal("a server that is off still offers its tools")
	}
}

func fakeHTTPServer(t *testing.T, sseCalls bool) (*httptest.Server, *sync.Map) {
	seen := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			seen.Store("deleted", true)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var m rpcMessage
		_ = json.NewDecoder(r.Body).Decode(&m)
		seen.Store("auth", r.Header.Get("Authorization"))
		if m.Method != "initialize" && r.Header.Get("Mcp-Session-Id") != "s-42" {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		reply := func(result any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
		}
		switch m.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "s-42")
			reply(map[string]any{"protocolVersion": "2025-06-18", "serverInfo": map[string]any{"name": "fake-http"}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			reply(map[string]any{"tools": []any{map[string]any{"name": "add", "description": "Adds", "inputSchema": map[string]any{"type": "object",
				"properties": map[string]any{"a": map[string]any{"type": "number"}, "b": map[string]any{"type": "number"}}}}}})
		case "tools/call":
			var p struct {
				Arguments map[string]float64 `json:"arguments"`
			}
			_ = json.Unmarshal(m.Params, &p)
			result := map[string]any{"content": []any{map[string]any{"type": "text", "text": fmt.Sprint(p.Arguments["a"] + p.Arguments["b"])}}}
			if !sseCalls {
				reply(result)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			note, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/progress", "params": map[string]any{}})
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": result})
			fmt.Fprintf(w, "event: message\ndata: %s\n\nevent: message\ndata: %s\n\n", note, data)
		default:
			w.WriteHeader(http.StatusAccepted)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, seen
}

func TestMCPHTTPServer(t *testing.T) {
	for _, sse := range []bool{false, true} {
		t.Run(fmt.Sprint("sse=", sse), func(t *testing.T) {
			m := newManager(t)
			srv, seen := fakeHTTPServer(t, sse)
			ctx := context.Background()
			list, err := ParseServerLine(srv.URL + "/mcp")
			if err != nil {
				t.Fatal(err)
			}
			keys, err := m.AddServers(ctx, list)
			if err != nil || len(keys) != 1 {
				t.Fatalf("add: %v %v", keys, err)
			}
			key := keys[0]
			if err := m.SetEnv(ctx, key, "Authorization", "Bearer abc"); err != nil {
				t.Fatal(err)
			}
			info := m.Wait(key, 10*time.Second)
			if info.Status != StatusReady || info.Access != AccessAdmins || info.Source != SourcePanel {
				t.Fatalf("server = %+v", info)
			}
			name := info.Tools[0].Name
			if m.Allowed(name, "tg-9") {
				t.Fatal("a server added in the panel is for everyone before an admin says so")
			}
			out, err := m.Call(ctx, "tg-1", name, map[string]any{"a": 2, "b": 3})
			if err != nil || out != "5" {
				t.Fatalf("add = %q %v", out, err)
			}
			if auth, _ := seen.Load("auth"); auth != "Bearer abc" {
				t.Fatalf("the secret header was not sent: %v", auth)
			}
			if err := m.Delete(ctx, key); err != nil {
				t.Fatal(err)
			}
			if _, ok := m.Get(key); ok || len(m.States.PanelServers()) != 0 {
				t.Fatal("the server is still there")
			}
		})
	}
}

func TestMCPLegacySSE(t *testing.T) {
	m := newManager(t)
	stream := make(chan []byte, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/sse":
			w.Header().Set("Content-Type", "text/event-stream")
			fl := w.(http.Flusher)
			fmt.Fprint(w, "event: endpoint\ndata: /messages?session=1\n\n")
			fl.Flush()
			for {
				select {
				case msg := <-stream:
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", msg)
					fl.Flush()
				case <-r.Context().Done():
					return
				}
			}
		case r.Method == http.MethodPost && r.URL.Path == "/messages":
			var msg rpcMessage
			_ = json.NewDecoder(bufio.NewReader(r.Body)).Decode(&msg)
			w.WriteHeader(http.StatusAccepted)
			var result any
			switch msg.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2024-11-05", "serverInfo": map[string]any{"name": "old-sse"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "now", "description": "The time", "inputSchema": map[string]any{"type": "object"}}}}
			case "tools/call":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": "12:00"}}}
			default:
				return
			}
			data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result})
			stream <- data
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()
	defer m.Close()
	ctx := context.Background()
	keys, err := m.AddServers(ctx, []ServerConfig{{ID: "clock", URL: srv.URL + "/sse", Access: AccessEveryone}})
	if err != nil {
		t.Fatal(err)
	}
	info := m.Wait(keys[0], 10*time.Second)
	if info.Status != StatusReady || !strings.HasPrefix(info.ServerName, "old-sse") {
		t.Fatalf("server = %+v", info)
	}
	out, err := m.Call(ctx, "tg-3", "clock__now", nil)
	if err != nil || out != "12:00" {
		t.Fatalf("now = %q %v", out, err)
	}
}

func TestParseServers(t *testing.T) {
	list, err := ParseServers([]byte(`{"mcpServers": {
		"github": {"command": "npx", "args": ["-y", "@modelcontextprotocol/server-github"], "env": {"GITHUB_PERSONAL_ACCESS_TOKEN": "ghp_x"}},
		"custom_api": {"url": "https://api.example.com/mcp/sse", "headers": {"Authorization": "Bearer y"}}}}`))
	if err != nil || len(list) != 2 {
		t.Fatalf("%v %v", list, err)
	}
	for _, c := range list {
		if c.Access != AccessAdmins {
			t.Fatalf("%s is not admins-only by default", c.ID)
		}
	}
	one, err := ParseServerLine("npx -y @modelcontextprotocol/server-github")
	if err != nil || one[0].ID != "github" || one[0].Command != "npx" || len(one[0].Args) != 2 {
		t.Fatalf("command line: %+v %v", one, err)
	}
	if _, err := ParseServerLine(`{"command": "x", "url": "https://a.b/mcp"}`); err == nil {
		t.Fatal("a server with a command and a url was accepted")
	}
	if _, err := ParseServerLine("ftp://example.com/mcp"); err == nil {
		t.Fatal("an ftp address was accepted")
	}
	if got := mcpToolName("github", "create_issue"); got != "github__create_issue" {
		t.Fatalf("name = %q", got)
	}
	if got := mcpToolName("my.server", "a-b"); !toolNameOK(got) {
		t.Fatalf("name %q is not a valid tool name", got)
	}
}

func toolNameOK(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func TestScriptOnTheMachine(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: filepath.Join(t.TempDir(), "plugins"), Machine: sb, Version: "test"}
	t.Cleanup(m.Close)
	ctx := context.Background()
	code := `# tool: probe
import json, os, sys
import helper
req = json.load(sys.stdin)
out = {"helper": helper.WHO, "cwd": os.getcwd(), "workspace": req["workspace"]}
open(os.path.join(req["workspace"], "out.txt"), "w").write("ok")
print(json.dumps(out))
`
	zipped := makeZip(t, map[string]string{"probe/plugin.json": `{"id": "probe", "tools": [{"name": "probe", "description": "d"}]}`,
		"probe/run.py": code, "probe/helper.py": "WHO = 'plugin'\n"})
	if _, err := m.InstallZip(ctx, zipped); err != nil {
		t.Fatal(err)
	}
	ws, err := sb.Workspace("tg-77")
	if err != nil {
		t.Fatal(err)
	}
	out, err := m.Call(ctx, "tg-77", "probe", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("%q: %v", out, err)
	}
	if got["helper"] != "plugin" || got["workspace"] != ws || got["cwd"] != ws {
		t.Fatalf("here: %v (the work folder is %s)", got, ws)
	}
	if data, _ := os.ReadFile(filepath.Join(ws, "out.txt")); string(data) != "ok" {
		t.Fatal("the plugin could not write to the user's work folder")
	}
}

func TestMCPStdio(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: filepath.Join(t.TempDir(), "plugins"), Machine: sb, Version: "test"}
	t.Cleanup(m.Close)
	if err := os.MkdirAll(filepath.Join(m.Dir, "servers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Dir, "servers", "fake.py"), []byte(fakeStdioServer), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	off := false
	keys, err := m.AddServers(ctx, []ServerConfig{{ID: "boxed", Command: "python3", Args: []string{filepath.Join(m.Dir, "servers", "fake.py")},
		RequiresNet: &off, Access: AccessEveryone}})
	if err != nil {
		t.Fatal(err)
	}
	if info := m.Wait(keys[0], 20*time.Second); info.Status != StatusReady {
		t.Fatalf("server = %+v", info)
	}
	if out, err := m.Call(ctx, "tg-2", "boxed__echo", map[string]any{"text": "hi"}); err != nil || !strings.HasPrefix(out, "echo: hi") {
		t.Fatalf("echo = %q %v", out, err)
	}
}

const nodeFetchServer = `const rl = require('readline').createInterface({input: process.stdin});
const send = m => process.stdout.write(JSON.stringify(m) + '\n');
rl.on('line', async line => {
  const m = JSON.parse(line);
  if (m.method === 'initialize') {
    send({jsonrpc: '2.0', id: m.id, result: {protocolVersion: '2025-06-18', capabilities: {tools: {}}, serverInfo: {name: 'node-fetcher', version: '1'}}});
  } else if (m.method === 'tools/list') {
    send({jsonrpc: '2.0', id: m.id, result: {tools: [{name: 'get', description: 'Fetches a page',
      inputSchema: {type: 'object', properties: {url: {type: 'string'}}, required: ['url']}}]}});
  } else if (m.method === 'tools/call') {
    let text;
    try { text = await (await fetch(m.params.arguments.url)).text(); } catch (e) { text = 'ERR ' + ((e.cause && e.cause.code) || e.message); }
    send({jsonrpc: '2.0', id: m.id, result: {content: [{type: 'text', text}]}});
  } else if (m.id !== undefined) {
    send({jsonrpc: '2.0', id: m.id, error: {code: -32601, message: 'unknown'}});
  }
});`

func TestNodeMCPServerFetchesThroughTheProxy(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not installed")
	}
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "http_proxy", "https_proxy"} {
		t.Setenv(k, "")
	}
	var ip string
	addrs, _ := net.InterfaceAddrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() {
			ip = n.IP.String()
			break
		}
	}
	if ip == "" {
		t.Skip("no address but loopback")
	}
	ln, err := net.Listen("tcp", ip+":0")
	if err != nil {
		t.Skip(err)
	}
	site := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("hello from outside")) })}
	go site.Serve(ln)
	defer site.Close()
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	m := &Manager{Dir: filepath.Join(t.TempDir(), "plugins"), Machine: sb, Version: "test"}
	t.Cleanup(m.Close)
	if err := os.MkdirAll(filepath.Join(m.Dir, "servers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.Dir, "servers", "fetcher.js"), []byte(nodeFetchServer), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	keys, err := m.AddServers(ctx, []ServerConfig{{ID: "web", Command: "node", Args: []string{filepath.Join(m.Dir, "servers", "fetcher.js")}, Access: AccessEveryone}})
	if err != nil {
		t.Fatal(err)
	}
	if info := m.Wait(keys[0], 20*time.Second); info.Status != StatusReady {
		t.Fatalf("server = %+v", info)
	}
	out, err := m.Call(ctx, "tg-4", "web__get", map[string]any{"url": "http://" + ln.Addr().String() + "/"})
	if err != nil || out != "hello from outside" {
		t.Fatalf("the Node server's fetch: %q %v", out, err)
	}
}
