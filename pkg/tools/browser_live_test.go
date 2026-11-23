package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

func TestBrowserToolLiveEndToEnd(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	reg := NewRegistry(sb, nil, nil, Options{})
	if !reg.Has(ToolBrowser) {
		t.Skipf("browser tool unavailable (chrome=%q); skipping live browser test", findChrome())
	}

	const sess = "browser-live-test"
	htmlDoc := `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Macron Browser Live Check</title></head>
<body>
  <h1 id="heading">Macron StarBuddy</h1>
  <input id="note" placeholder="Enter note" />
  <button id="go" onclick="document.getElementById('status').textContent = 'CLICKED:' + document.getElementById('note').value">Run Check</button>
  <p id="status">IDLE</p>
</body>
</html>`
	if err := sb.WriteFile(sess, "demo.html", []byte(htmlDoc)); err != nil {
		t.Fatalf("WriteFile demo.html: %v", err)
	}

	argsJSON, err := json.Marshal(map[string]any{
		"url": "demo.html",
		"actions": []map[string]any{
			{"action": "fill", "selector": "#note", "text": "SpectorDev-OK"},
			{"action": "click", "selector": "#go"},
		},
		"screenshot":      true,
		"timeout_seconds": 30,
	})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	out, err := reg.Execute(ctx, sess, ToolBrowser, string(argsJSON))
	if err != nil {
		t.Fatalf("browser Execute error: %v", err)
	}
	if strings.HasPrefix(out, "error:") {
		t.Fatalf("browser tool returned error:\n%s", out)
	}
	if !strings.Contains(out, "Macron Browser Live Check") {
		t.Fatalf("expected page title in browser output, got:\n%s", out)
	}
	if !strings.Contains(out, "CLICKED:SpectorDev-OK") {
		t.Fatalf("expected interactive click result in browser output, got:\n%s", out)
	}
	if !strings.Contains(out, "screenshot.png") {
		t.Fatalf("expected screenshot.png in browser output, got:\n%s", out)
	}
	png, _, err := sb.ReadFile(sess, "screenshot.png", 5<<20)
	if err != nil {
		t.Fatalf("ReadFile screenshot.png: %v", err)
	}
	if len(png) < 100 || !bytes.HasPrefix(png, []byte{0x89, 'P', 'N', 'G'}) {
		t.Fatalf("expected valid PNG screenshot, got %d bytes", len(png))
	}
	t.Logf("Live browser test passed! Screenshot=%d bytes, output preview:\n%s", len(png), out)
}
