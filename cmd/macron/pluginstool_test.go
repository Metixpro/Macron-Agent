package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/plugins"
)

// An MCP server added from the chat lands in the usual mcp_servers.json form,
// beside the servers already there, readable by the owner alone.
func TestMCPFileIsEditedInPlace(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_servers.json")
	one := plugins.ServerConfig{ID: "files", Command: "npx", Args: []string{"-y", "server-filesystem", "/home"}, Env: map[string]string{"TOKEN": "t-1"}}
	if err := writeMCPServer(path, one); err != nil {
		t.Fatal(err)
	}
	two := plugins.ServerConfig{ID: "remote", URL: "https://mcp.example.com/sse", Headers: map[string]string{"Authorization": "Bearer x"}}
	if err := writeMCPServer(path, two); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the file may hold tokens: %v %v", fi, err)
	}
	data, _ := os.ReadFile(path)
	list, err := plugins.ParseServers(data)
	if err != nil || len(list) != 2 {
		t.Fatalf("the plugin loader reads back what was written: %v %v\n%s", list, err, data)
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(data, &top)
	if _, ok := top["mcpServers"]; !ok {
		t.Errorf("the standard form: %s", data)
	}
	if err := removeMCPServer(path, "files"); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.Contains(string(data), "server-filesystem") || !strings.Contains(string(data), "mcp.example.com") {
		t.Errorf("only the named one goes: %s", data)
	}
	if err := removeMCPServer(path, "nothing"); err == nil {
		t.Error("removing what is not there says so")
	}
}

// A file written by hand with the servers at the top keeps its shape.
func TestMCPFileWithServersAtTheTop(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp_servers.json")
	_ = os.WriteFile(path, []byte(`{"git": {"command": "uvx", "args": ["mcp-server-git"]}}`), 0o600)
	if err := writeMCPServer(path, plugins.ServerConfig{ID: "files", Command: "npx"}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	list, err := plugins.ParseServers(data)
	if err != nil || len(list) != 2 {
		t.Fatalf("both are kept: %v %v\n%s", list, err, data)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	_ = os.WriteFile(bad, []byte("{nope"), 0o600)
	if err := writeMCPServer(bad, plugins.ServerConfig{ID: "x", Command: "y"}); err == nil {
		t.Error("a broken file is never overwritten")
	}
	if got, _ := os.ReadFile(bad); string(got) != "{nope" {
		t.Errorf("left as it was: %s", got)
	}
}
