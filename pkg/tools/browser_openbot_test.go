package tools

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

func TestRepairBrowserToolArgs(t *testing.T) {
	raw := map[string]any{
		"fullPage":       true,
		"waitUntil":      "networkidle",
		"timeoutSeconds": 45,
		"actions": []any{
			map[string]any{
				"op":         "tap",
				"target":     `- 3:main:12 ← button "Sign in"`,
				"clickCount": 2,
			},
			map[string]any{
				"action":     "input",
				"element":    `role textbox "Email" >> nth=1`,
				"value":      "alice@example.com",
				"replace":    true,
				"pressEnter": true,
			},
			map[string]any{
				"action": "click",
				"ref":    "css #submit-btn",
			},
			map[string]any{
				"action": "fillFields",
				"fields": []any{
					map[string]any{"target": "[4:f1:2]", "text": "hello", "replace": true},
				},
			},
			map[string]any{
				"action": "eval",
				"code":   "document.title",
			},
		},
	}

	repaired, notes := repairBrowserToolArgs(raw)
	if len(notes) == 0 {
		t.Fatal("expected repair notes")
	}
	if repaired["full_page"] != true {
		t.Fatalf("expected full_page=true, got %#v", repaired["full_page"])
	}
	if repaired["until"] != "dom-quiet" {
		t.Fatalf("expected until=dom-quiet, got %#v", repaired["until"])
	}
	if repaired["timeout_seconds"] != 45 {
		t.Fatalf("expected timeout_seconds=45, got %#v", repaired["timeout_seconds"])
	}

	acts := repaired["actions"].([]any)
	a0 := acts[0].(map[string]any)
	if a0["action"] != "click" || a0["selector"] != "3:main:12" || a0["count"] != 2 {
		t.Fatalf("unexpected a0: %#v", a0)
	}
	a1 := acts[1].(map[string]any)
	if a1["action"] != "fill" || a1["selector"] != `role=textbox[name="Email"] >> nth=1` || a1["text"] != "alice@example.com" || a1["mode"] != "replace" || a1["submit"] != true {
		t.Fatalf("unexpected a1: %#v", a1)
	}
	a2 := acts[2].(map[string]any)
	if a2["selector"] != "#submit-btn" {
		t.Fatalf("unexpected a2: %#v", a2)
	}
	a3 := acts[3].(map[string]any)
	if a3["action"] != "fill_fields" {
		t.Fatalf("unexpected a3 action: %#v", a3)
	}
	f0 := a3["fields"].([]any)[0].(map[string]any)
	if f0["selector"] != "4:f1:2" || f0["value"] != "hello" || f0["mode"] != "replace" {
		t.Fatalf("unexpected fill_fields[0]: %#v", f0)
	}
	a4 := acts[4].(map[string]any)
	if a4["script"] != "document.title" {
		t.Fatalf("expected eval code->script, got %#v", a4)
	}
}

func TestSiteMemoryStoreMatchingAndOncePerTurn(t *testing.T) {
	reg := &Registry{BrowserDataDir: t.TempDir()}
	m1, err := reg.AddSiteMemory("https://www.GitHub.com/Metixpro", "Use SSH deploy keys tab for CI secrets")
	if err != nil || m1.Host != "github.com" {
		t.Fatalf("AddSiteMemory parent: entry=%+v err=%v", m1, err)
	}
	m2, err := reg.AddSiteMemory("app.github.com", "Dashboard requires switching org dropdown first")
	if err != nil || m2.Host != "app.github.com" {
		t.Fatalf("AddSiteMemory subdomain: entry=%+v err=%v", m2, err)
	}

	matched := reg.siteMem().match("https://app.github.com/settings", 5)
	if len(matched) != 2 || matched[0].Host != "app.github.com" || matched[1].Host != "github.com" {
		t.Fatalf("expected subdomain then parent domain, got %+v", matched)
	}

	// Claim once per turn window: first call returns notes, second call returns nil.
	first := reg.siteMem().claimForTurn("sess1", "https://app.github.com/settings")
	if len(first) != 2 {
		t.Fatalf("expected 2 site memories on first claim, got %d", len(first))
	}
	second := reg.siteMem().claimForTurn("sess1", "https://app.github.com/issues")
	if len(second) != 0 {
		t.Fatalf("expected 0 site memories on second claim same turn, got %d", len(second))
	}
	reg.ResetBrowserTurnSiteMemories("sess1")
	third := reg.siteMem().claimForTurn("sess1", "https://app.github.com/pulls")
	if len(third) != 2 {
		t.Fatalf("expected 2 site memories after resetTurn, got %d", len(third))
	}

	if err := reg.DeleteSiteMemory(m1.ID); err != nil {
		t.Fatalf("DeleteSiteMemory: %v", err)
	}
	if len(reg.ListSiteMemories()) != 1 {
		t.Fatalf("expected 1 remaining site memory")
	}
}

func TestBrowserProfilesAndSOCKS5Relay(t *testing.T) {
	reg := &Registry{BrowserDataDir: t.TempDir()}

	// Start a mock upstream SOCKS5 server requiring RFC 1929 auth ("macron"/"secret123")
	// and echoing back "RELAY_ECHO_OK" once CONNECT succeeds.
	upLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen upstream: %v", err)
	}
	defer upLn.Close()

	go func() {
		conn, err := upLn.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		var greet [4]byte
		if _, err := io.ReadFull(conn, greet[:2]); err != nil {
			return
		}
		methods := make([]byte, int(greet[1]))
		_, _ = io.ReadFull(conn, methods)
		// Require username/password auth (0x02)
		_, _ = conn.Write([]byte{0x05, 0x02})

		var ah [2]byte
		_, _ = io.ReadFull(conn, ah[:])
		u := make([]byte, int(ah[1]))
		_, _ = io.ReadFull(conn, u)
		var plen [1]byte
		_, _ = io.ReadFull(conn, plen[:])
		p := make([]byte, int(plen[0]))
		_, _ = io.ReadFull(conn, p)
		if string(u) != "macron" || string(p) != "secret123" {
			_, _ = conn.Write([]byte{0x01, 0x01})
			return
		}
		_, _ = conn.Write([]byte{0x01, 0x00})

		// Read CONNECT request
		var req [4]byte
		_, _ = io.ReadFull(conn, req[:])
		if req[3] == 0x01 {
			buf := make([]byte, 6)
			_, _ = io.ReadFull(conn, buf)
		}
		_, _ = conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 127, 0, 0, 1, 0, 80})
		_, _ = conn.Write([]byte("RELAY_ECHO_OK"))
	}()

	upAddr := upLn.Addr().(*net.TCPAddr)
	saved, err := reg.SaveBrowserProfile(BrowserProfile{
		Name:  "Work Proxy",
		Color: "#a78bfa",
		Proxy: &BrowserProxyConfig{
			Scheme:   "socks5",
			Host:     "127.0.0.1",
			Port:     upAddr.Port,
			Username: "macron",
			Password: "secret123",
		},
	}, false)
	if err != nil {
		t.Fatalf("SaveBrowserProfile: %v", err)
	}
	if !saved.HasProxy || !saved.ProxyPassword || saved.Proxy.Password != "" {
		t.Fatalf("expected password masked in view: %+v", saved)
	}

	_, launchCfg, err := reg.profileStore().resolveLaunchConfig(saved.ID)
	if err != nil {
		t.Fatalf("resolveLaunchConfig: %v", err)
	}
	defer reg.profileStore().closeAllRelays()
	if launchCfg["webrtc_disable_non_proxied_udp"] != true {
		t.Fatalf("expected webrtc_disable_non_proxied_udp=true")
	}
	pxMap := launchCfg["proxy"].(map[string]any)
	relayServer := strings.TrimPrefix(pxMap["server"].(string), "socks5://")

	// Connect to local unauthenticated relay as Chromium does.
	c, err := net.DialTimeout("tcp", relayServer, 5*time.Second)
	if err != nil {
		t.Fatalf("dial relay: %v", err)
	}
	defer c.Close()
	_, _ = c.Write([]byte{0x05, 0x01, 0x00})
	var ch [2]byte
	if _, err := io.ReadFull(c, ch[:]); err != nil || ch[1] != 0x00 {
		t.Fatalf("relay greeting failed: %v %x", err, ch)
	}
	_, _ = c.Write([]byte{0x05, 0x01, 0x00, 0x01, 93, 184, 216, 34, 0, 80})
	var rep [10]byte
	if _, err := io.ReadFull(c, rep[:]); err != nil || rep[1] != 0x00 {
		t.Fatalf("relay CONNECT failed: %v %x", err, rep)
	}
	payload := make([]byte, 13)
	if _, err := io.ReadFull(c, payload); err != nil || string(payload) != "RELAY_ECHO_OK" {
		t.Fatalf("unexpected relay payload: %q err=%v", string(payload), err)
	}

	if err := reg.DeleteBrowserProfile("workspace"); err == nil {
		t.Fatal("expected built-in profile deletion to fail")
	}
	if err := reg.DeleteBrowserProfile(saved.ID); err != nil {
		t.Fatalf("DeleteBrowserProfile: %v", err)
	}
}

type mockScriptHost struct {
	takeover atomic.Bool
	actions  []map[string]any
	evalVal  any
}

func (m *mockScriptHost) IsTakeover() bool { return m.takeover.Load() }
func (m *mockScriptHost) ExecAction(_ context.Context, act map[string]any) (map[string]any, error) {
	m.actions = append(m.actions, act)
	return map[string]any{"ok": true}, nil
}
func (m *mockScriptHost) Snapshot(_ context.Context) (map[string]any, error) {
	return map[string]any{"title": "Mock", "url": "https://example.com", "rev": 1}, nil
}
func (m *mockScriptHost) Evaluate(_ context.Context, _ string, _ int) (any, error) {
	if m.evalVal != nil {
		return m.evalVal, nil
	}
	return []any{"alpha", "beta", "gamma"}, nil
}

func TestRunBrowserScriptSandbox(t *testing.T) {
	// 1. Await-style multi-step script + local filtering + logs
	host := &mockScriptHost{}
	res, err := runBrowserScript(context.Background(), host, `
		const items = await evaluate("items");
		const keep = items.filter(x => x.startsWith("b"));
		log("matched", keep.length, "await stays inside string");
		await click("1:main:0");
		return { keep, count: keep.length };
	`, 5*time.Second)
	if err != nil {
		t.Fatalf("runBrowserScript: %v", err)
	}
	if len(host.actions) != 1 || host.actions[0]["action"] != "click" {
		t.Fatalf("expected 1 click action, got %+v", host.actions)
	}
	if len(res.Logs) != 1 || !strings.Contains(res.Logs[0], "await stays inside string") {
		t.Fatalf("unexpected logs: %+v", res.Logs)
	}

	// 2. Takeover mid-script aborts cleanly
	host2 := &mockScriptHost{}
	host2.takeover.Store(true)
	res2, err := runBrowserScript(context.Background(), host2, `click("1:main:0");`, 5*time.Second)
	if err != nil {
		t.Fatalf("expected graceful takeover pause, got err=%v", err)
	}
	if !strings.HasPrefix(res2.Interrupted, "paused:") {
		t.Fatalf("expected interrupted paused reason, got %+v", res2)
	}

	// 3. Infinite loop interrupted by timeout
	host3 := &mockScriptHost{}
	_, err = runBrowserScript(context.Background(), host3, `while (true) {}`, 250*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("expected timeout error on infinite loop, got %v", err)
	}

	// 4. 64KB output truncation guard
	host4 := &mockScriptHost{}
	res4, err := runBrowserScript(context.Background(), host4, `return "x".repeat(90000);`, 5*time.Second)
	if err != nil {
		t.Fatalf("large return failed: %v", err)
	}
	m, ok := res4.Value.(map[string]any)
	if !ok || m["_truncated"] != true {
		t.Fatalf("expected truncated payload over 64KB, got %#v", res4.Value)
	}
}

func TestBrowserOpenBotEndToEnd(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	reg := NewRegistry(sb, nil, nil, Options{})
	reg.BrowserDataDir = t.TempDir()
	defer reg.CloseAllBrowsers()
	if !reg.Has(ToolBrowser) {
		t.Skipf("browser tool unavailable (chrome=%q); skipping OpenBot E2E test", findChrome())
	}

	const sess = "openbot-e2e"
	// Fixture 1: SPA skeleton settle + hidden subtree pruning + Shadow DOM cookie banner + fill_fields + sensitive field + dialog + link
	fixtureHTML := `<!doctype html>
<html>
<head><meta charset="utf-8"><title>OpenBot Parity Fixture</title></head>
<body>
  <div id="cookie-host"></div>
  <div id="skel" class="loading-skeleton" aria-busy="true" style="width:120px;height:24px;background:#ccc">Loading…</div>
  <main id="app" style="display:none">
    <h1>SPA_READY_DATA</h1>
    <div style="display:none"><button id="hidden-btn">Should Not Appear</button></div>
    <div aria-hidden="true"><button id="aria-hidden-btn">Aria Hidden</button></div>
    <input id="first" name="first_name" placeholder="First name" />
    <input id="email" name="email" placeholder="Email" />
    <input id="pw" type="password" autocomplete="current-password" placeholder="Password" />
    <button id="save-btn" onclick="document.getElementById('res').textContent = 'SAVED:' + document.getElementById('first').value + ':' + document.getElementById('email').value">Save Profile</button>
    <button id="dlg-btn" onclick="window.__dlgAns = confirm('Confirm deploy?'); document.getElementById('res').textContent = 'DLG:' + window.__dlgAns">Open Confirm</button>
    <a id="ext-link" href="https://example.com/docs">Example Docs</a>
    <p id="res">INIT</p>
  </main>
  <script>
    // Attach an Open Shadow DOM cookie banner (Accept vs Reject All)
    const host = document.getElementById('cookie-host');
    const sr = host.attachShadow({ mode: 'open' });
    sr.innerHTML = '<div id="cookie-banner" role="dialog" aria-label="Cookie consent" style="padding:12px;background:#222;color:#fff"><p>We use cookies</p><button id="accept-c" onclick="window.__consent=\'accept\';this.parentElement.remove()">Accept all</button><button id="reject-c" onclick="window.__consent=\'reject\';this.parentElement.remove()">Reject all</button></div>';

    // Simulate SPA skeleton clearing after 350ms
    setTimeout(() => {
      document.getElementById('skel').remove();
      document.getElementById('app').style.display = 'block';
    }, 350);
  </script>
</body>
</html>`
	if err := sb.WriteFile(sess, "fixture.html", []byte(fixtureHTML)); err != nil {
		t.Fatalf("WriteFile fixture.html: %v", err)
	}

	// Fixture 2: Interstitial challenge screen
	challengeHTML := `<!doctype html>
<html>
<head><meta charset="utf-8"><title>Just a moment...</title></head>
<body>
  <h1>Checking if the site connection is secure</h1>
  <div class="cf-turnstile" data-sitekey="test"></div>
  <input type="hidden" name="cf-turnstile-response" />
</body>
</html>`
	if err := sb.WriteFile(sess, "challenge.html", []byte(challengeHTML)); err != nil {
		t.Fatalf("WriteFile challenge.html: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// 1. Open SPA fixture -> verify Smart SPA Settle, revisioned refs, hidden pruning, opt-in screenshot=false
	openArgs, _ := json.Marshal(map[string]any{"url": "fixture.html", "until": "dom-quiet"})
	out, err := reg.Execute(ctx, sess, ToolBrowser, string(openArgs))
	if err != nil || strings.HasPrefix(out, "error:") {
		t.Fatalf("open fixture failed: err=%v out=%s", err, out)
	}
	if !strings.Contains(out, "SPA_READY_DATA") {
		t.Fatalf("Smart SPA settle did not wait for skeleton to clear:\n%s", out)
	}
	if strings.Contains(out, "Should Not Appear") || strings.Contains(out, "Aria Hidden") {
		t.Fatalf("Hidden subtrees should have been pruned from snapshot:\n%s", out)
	}
	if strings.Contains(out, "screenshot.png") {
		t.Fatalf("Screenshot should be opt-in (false by default):\n%s", out)
	}
	refRe := regexp.MustCompile(`- (\d+:main:\d+) ← button "Save Profile"`)
	m := refRe.FindStringSubmatch(out)
	if m == nil {
		t.Fatalf("expected revisioned ref for Save Profile button, got:\n%s", out)
	}
	saveRef := m[1]

	// 2. Verify Reject-Only Cookie Banner Refuser clicked Reject (never Accept) inside Open Shadow DOM
	time.Sleep(600 * time.Millisecond)
	consentArgs, _ := json.Marshal(map[string]any{"actions": []map[string]any{{"action": "eval", "script": "window.__consent || ''"}}})
	consentOut, err := reg.Execute(ctx, sess, ToolBrowser, string(consentArgs))
	if err != nil || !strings.Contains(consentOut, "reject") || strings.Contains(consentOut, "accept") {
		t.Fatalf("expected shadow-DOM cookie banner to be rejected (never accepted), got:\n%s", consentOut)
	}

	// 3. Verify Stale Ref rejection (rev 0 != current rev)
	staleArgs, _ := json.Marshal(map[string]any{"actions": []map[string]any{{"action": "click", "selector": "0:main:0"}}})
	staleOut, _ := reg.Execute(ctx, sess, ToolBrowser, string(staleArgs))
	if !strings.Contains(strings.ToLower(staleOut), "stale ref") {
		t.Fatalf("expected stale ref error, got:\n%s", staleOut)
	}
	_ = saveRef

	// 4. Verify fill_fields succeeds on normal fields AND rejects password field preflight
	badFillArgs, _ := json.Marshal(map[string]any{
		"actions": []map[string]any{{
			"action": "fill_fields",
			"fields": []map[string]any{
				{"selector": "#first", "value": "Ali"},
				{"selector": "#pw", "value": "supersecret"},
			},
		}},
	})
	badFillOut, _ := reg.Execute(ctx, sess, ToolBrowser, string(badFillArgs))
	if !strings.Contains(strings.ToLower(badFillOut), "refused sensitive field") {
		t.Fatalf("expected fill_fields to refuse password field preflight, got:\n%s", badFillOut)
	}

	goodFillArgs, _ := json.Marshal(map[string]any{
		"actions": []map[string]any{
			{
				"action": "fill_fields",
				"fields": []map[string]any{
					{"selector": "#first", "value": "Mina"},
					{"selector": "#email", "value": "mina@macron.dev"},
				},
			},
			{"action": "click", "selector": "#save-btn"},
		},
	})
	goodFillOut, err := reg.Execute(ctx, sess, ToolBrowser, string(goodFillArgs))
	if err != nil || !strings.Contains(goodFillOut, "SAVED:Mina:mina@macron.dev") {
		t.Fatalf("fill_fields + click failed: err=%v out=%s", err, goodFillOut)
	}

	// 5. Verify live run_script (Goja sandbox driving real Chromium)
	scriptArgs, _ := json.Marshal(map[string]any{
		"script": `
			const txt = await evaluate("document.getElementById('res').textContent");
			log("saw", txt);
			await fill("#first", "Reza");
			await click("#save-btn");
			return { after: await evaluate("document.getElementById('res').textContent") };
		`,
	})
	scriptOut, err := reg.Execute(ctx, sess, ToolBrowser, string(scriptArgs))
	if err != nil || !strings.Contains(scriptOut, "SAVED:Reza:mina@macron.dev") {
		t.Fatalf("live run_script failed: err=%v out=%s", err, scriptOut)
	}

	// 6. Verify Teach a Task records clicks & redacts password field without recording typed secrets
	if _, _, err := reg.BrowserTeach(ctx, sess, "start"); err != nil {
		t.Fatalf("BrowserTeach start: %v", err)
	}
	if err := reg.BrowserInput(ctx, sess, BrowserInputEvent{Type: "click", X: 250, Y: 140, Button: "left", ClickCount: 1}); err != nil {
		t.Fatalf("teach click: %v", err)
	}
	_, teachSteps, err := reg.BrowserTeach(ctx, sess, "stop")
	if err != nil || len(teachSteps) == 0 {
		t.Fatalf("BrowserTeach stop: err=%v steps=%+v", err, teachSteps)
	}

	// 7. Verify Challenge Detection -> Instant Takeover (NO CAPTCHA solving!)
	chalArgs, _ := json.Marshal(map[string]any{"url": "challenge.html"})
	chalOut, err := reg.Execute(ctx, sess, ToolBrowser, string(chalArgs))
	if err != nil {
		t.Fatalf("challenge nav failed: %v", err)
	}
	if !strings.Contains(chalOut, "Challenge [needs-user]") {
		t.Fatalf("expected Challenge [needs-user] in output, got:\n%s", chalOut)
	}
	time.Sleep(150 * time.Millisecond)
	st := reg.BrowserState(sess)
	if !st.Takeover {
		t.Fatalf("expected automatic Takeover when challenge needs-user, state=%+v", st)
	}
	if _, err := reg.SetBrowserTakeover(ctx, sess, false, ""); err != nil {
		t.Fatalf("clear takeover: %v", err)
	}
}
