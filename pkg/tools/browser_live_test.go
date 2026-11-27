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
	defer reg.CloseAllBrowsers()
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
	t.Logf("Live browser test passed! Screenshot=%d bytes", len(png))
}

func TestBrowserDaemonSmoothScrollTypingTabsAndTakeover(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	reg := NewRegistry(sb, nil, nil, Options{})
	defer reg.CloseAllBrowsers()
	if !reg.Has(ToolBrowser) {
		t.Skipf("browser tool unavailable (chrome=%q); skipping live daemon test", findChrome())
	}

	const sess = "browser-daemon-test"
	htmlDoc := `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Live Interactive Page</title>
<style>
  body { margin: 0; font-family: sans-serif; height: 4200px; }
  .bar { position: fixed; top: 20px; left: 20px; display: flex; gap: 10px; align-items: center; background: #fff; padding: 8px; }
  #live-input { width: 220px; height: 32px; box-sizing: border-box; font-size: 14px; }
  #btn { width: 90px; height: 32px; box-sizing: border-box; }
</style>
</head>
<body>
  <script>window.__loadStamp = Math.random().toString(36).slice(2);</script>
  <div class="bar">
    <input id="live-input" type="text" placeholder="Type here" />
    <button id="btn" onclick="document.getElementById('out').textContent = 'LIVE:' + document.getElementById('live-input').value">Apply</button>
    <span id="out">INIT</span>
  </div>
</body>
</html>`
	if err := sb.WriteFile(sess, "demo_scroll.html", []byte(htmlDoc)); err != nil {
		t.Fatalf("WriteFile demo_scroll.html: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. Open page in persistent daemon
	st, err := reg.BrowserNav(ctx, sess, "goto", map[string]any{"url": "demo_scroll.html"})
	if err != nil {
		t.Fatalf("BrowserNav goto: %v", err)
	}
	if !st.Live || !st.Open || st.Title != "Live Interactive Page" {
		t.Fatalf("unexpected initial state: %+v", st)
	}

	// 2. Verify live JPEG screencast frame delivery
	initial, frameCh, unsub := reg.SubscribeBrowserFrames(sess)
	defer unsub()
	frame := initial
	if len(frame) == 0 {
		select {
		case frame = <-frameCh:
		case <-time.After(10 * time.Second):
			t.Fatal("timed out waiting for live JPEG screencast frame")
		}
	}
	if len(frame) < 200 || frame[0] != 0xFF || frame[1] != 0xD8 {
		t.Fatalf("expected valid JPEG frame, got %d bytes", len(frame))
	}

	// Record initial load stamp before scrolling
	evalStampScroll := func() (float64, string) {
		args, _ := json.Marshal(map[string]any{
			"actions":    []map[string]any{{"action": "eval", "script": "[window.scrollY, window.__loadStamp]"}},
			"screenshot": false,
		})
		res, err := reg.Execute(ctx, sess, ToolBrowser, string(args))
		if err != nil {
			t.Fatalf("eval: %v", err)
		}
		lines := strings.Split(res, "\n")
		for _, ln := range lines {
			if idx := strings.Index(ln, "["); idx >= 0 {
				var pair []any
				if json.Unmarshal([]byte(ln[idx:]), &pair) == nil && len(pair) == 2 {
					sy, _ := pair[0].(float64)
					stamp, _ := pair[1].(string)
					return sy, stamp
				}
			}
		}
		t.Fatalf("could not parse [scrollY, stamp] from:\n%s", res)
		return 0, ""
	}
	sy0, stamp0 := evalStampScroll()

	// 3. Smooth wheel scroll via direct CDP input without page reload
	for i := 0; i < 3; i++ {
		if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "wheel", X: 600, Y: 400, DeltaY: 320}); err != nil {
			t.Fatalf("BrowserInput wheel: %v", err)
		}
		time.Sleep(80 * time.Millisecond)
	}
	sy1, stamp1 := evalStampScroll()
	if sy1 <= sy0+300 || stamp1 != stamp0 {
		t.Fatalf("scroll did not persist without reload: before=(y=%.1f, %s) after=(y=%.1f, %s)", sy0, stamp0, sy1, stamp1)
	}

	// 4. Live CDP click + Persian/English typing + submit click
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "click", X: 120, Y: 44, Button: "left", ClickCount: 1}); err != nil {
		t.Fatalf("BrowserInput click input: %v", err)
	}
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "insert_text", Text: "سلام Macron"}); err != nil {
		t.Fatalf("BrowserInput insert_text: %v", err)
	}
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "click", X: 295, Y: 44, Button: "left", ClickCount: 1}); err != nil {
		t.Fatalf("BrowserInput click button: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	evalOutArgs, _ := json.Marshal(map[string]any{
		"actions":    []map[string]any{{"action": "eval", "script": "[document.getElementById('out').textContent, window.__loadStamp]"}},
		"screenshot": false,
	})
	domOut, err := reg.Execute(ctx, sess, ToolBrowser, string(evalOutArgs))
	if err != nil || !strings.Contains(domOut, "LIVE:سلام Macron") || !strings.Contains(domOut, stamp0) {
		t.Fatalf("live typing DOM/stamp mismatch:\nerr=%v\nout=%s", err, domOut)
	}

	// 5. Real multi-tab open / switch / close
	stTabs, err := reg.BrowserNav(ctx, sess, "new_tab", map[string]any{"url": "demo_scroll.html"})
	if err != nil {
		t.Fatalf("new_tab: %v", err)
	}
	if len(stTabs.Tabs) != 2 {
		t.Fatalf("expected 2 tabs after new_tab, got %d (%+v)", len(stTabs.Tabs), stTabs.Tabs)
	}
	if _, err := reg.BrowserNav(ctx, sess, "switch_tab", map[string]any{"index": 0}); err != nil {
		t.Fatalf("switch_tab(0): %v", err)
	}
	stTabsAfterClose, err := reg.BrowserNav(ctx, sess, "close_tab", map[string]any{"index": 1})
	if err != nil || len(stTabsAfterClose.Tabs) != 1 {
		t.Fatalf("close_tab(1) unexpected result: err=%v tabs=%+v", err, stTabsAfterClose.Tabs)
	}

	// 6. Mid-run Takeover pause / user input / hand back / resume
	toolArgs, _ := json.Marshal(map[string]any{
		"actions": []map[string]any{
			{"action": "wait", "ms": 900},
			{"action": "eval", "script": "document.getElementById('live-input').value"},
		},
		"screenshot":      false,
		"timeout_seconds": 30,
	})
	type agentRes struct {
		out string
		err error
	}
	doneCh := make(chan agentRes, 1)
	go func() {
		o, e := reg.Execute(ctx, sess, ToolBrowser, string(toolArgs))
		doneCh <- agentRes{out: o, err: e}
	}()

	// Wait until daemon reports AgentActive == true
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if reg.BrowserState(sess).AgentActive {
			break
		}
		time.Sleep(40 * time.Millisecond)
	}
	if !reg.BrowserState(sess).AgentActive {
		t.Fatal("daemon never entered AgentActive state")
	}

	stPaused, err := reg.SetBrowserTakeover(ctx, sess, true, "user")
	if err != nil || !stPaused.Takeover {
		t.Fatalf("SetBrowserTakeover(true): err=%v state=%+v", err, stPaused)
	}

	// User edits input directly while agent is paused
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "click", X: 120, Y: 44, Button: "left", ClickCount: 1}); err != nil {
		t.Fatalf("takeover click: %v", err)
	}
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "press", Key: "Control+a"}); err != nil {
		t.Fatalf("takeover select all: %v", err)
	}
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "insert_text", Text: "USER_TAKEOVER_OK"}); err != nil {
		t.Fatalf("takeover typing: %v", err)
	}

	// Verify agent remains paused until hand-back
	select {
	case r := <-doneCh:
		t.Fatalf("agent finished prematurely while Takeover was active: %+v", r)
	case <-time.After(1200 * time.Millisecond):
	}

	stResumed, err := reg.SetBrowserTakeover(ctx, sess, false, "")
	if err != nil || stResumed.Takeover {
		t.Fatalf("SetBrowserTakeover(false): err=%v state=%+v", err, stResumed)
	}

	select {
	case r := <-doneCh:
		if r.err != nil || !strings.Contains(r.out, "USER_TAKEOVER_OK") {
			t.Fatalf("resumed agent did not observe user takeover edit: err=%v out=%s", r.err, r.out)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("agent did not resume and finish after SetBrowserTakeover(false)")
	}

	// 7. Clean close
	reg.CloseBrowser(sess)
	if stEnd := reg.BrowserState(sess); stEnd.Live {
		t.Fatalf("expected daemon stopped after CloseBrowser, got Live=true: %+v", stEnd)
	}
}
