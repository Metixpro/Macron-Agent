package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"net/http"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func connectionRegistry(t *testing.T, client *http.Client) *tools.Registry {
	t.Helper()
	t.Setenv("MACRON_HOME", t.TempDir())
	t.Setenv("MACRON_ENV_FILE", "")
	m, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	return tools.NewRegistry(m, nil, nil, tools.Options{HTTP: client, ConnectionAdmin: func(session string) bool { return session == "owner" }})
}
func connectionCall(t *testing.T, r *tools.Registry, session, action, service string, values map[string]string) string {
	t.Helper()
	raw, _ := json.Marshal(map[string]any{"action": action, "service": service, "values": values})
	out, e := r.Execute(context.Background(), session, tools.ToolConnections, string(raw))
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestConnectionsOwnerConfigureHotReloadAndClear(t *testing.T) {
	var seen []string
	r := connectionRegistry(t, &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.Path)
		if !strings.HasSuffix(req.URL.Path, "getMe") && !strings.HasSuffix(req.URL.Path, "sendMessage") {
			t.Fatal("unexpected method", req.URL.Path)
		}
		return response(200, `{"ok":true,"result":{"id":2}}`), nil
	})})
	t.Setenv("BALE_BOT_TOKEN", "stale")
	if r.Usable(tools.ToolConnections, "visitor") {
		t.Fatal("non-owner sees connections")
	}
	out := connectionCall(t, r, "visitor", "configure", "bale", map[string]string{"BALE_BOT_TOKEN": "injected"})
	if !strings.Contains(out, "only the owner") || config.ToolEnvValue("BALE_BOT_TOKEN") != "stale" {
		t.Fatal(out)
	}
	_ = os.WriteFile(config.EnvFilePath(), []byte("UNRELATED=keep\nBALE_BOT_TOKEN=old\nBALE_BOT_TOKEN=duplicate\n"), 0600)
	out = connectionCall(t, r, "owner", "configure", "بله", map[string]string{"BALE_BOT_TOKEN": "new-secret", "MACRON_BALE_OWNERS": "42", "MACRON_BALE_API": "https://relay.invalid"})
	if !strings.Contains(out, "Saved") || strings.Contains(out, "new-secret") {
		t.Fatal(out)
	}
	info, _ := os.Stat(config.EnvFilePath())
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatal(info.Mode())
	}
	data, _ := os.ReadFile(config.EnvFilePath())
	if strings.Count(string(data), "BALE_BOT_TOKEN=") != 1 || !strings.Contains(string(data), "UNRELATED=keep") {
		t.Fatal("unrelated config not preserved")
	}
	out = connectionCall(t, r, "owner", "describe", "bale", nil)
	if strings.Contains(out, "new-secret") || !strings.Contains(out, "set") {
		t.Fatal(out)
	}
	out = connectionCall(t, r, "owner", "test", "bale", nil)
	if !strings.Contains(out, "getMe succeeded") {
		t.Fatal(out)
	}
	_, e := r.SendIranMessenger(context.Background(), "bale", "", "chat", "message")
	if e != nil {
		t.Fatal(e)
	}
	if len(seen) != 2 || seen[0] != "/botnew-secret/getMe" || seen[1] != "/botnew-secret/sendMessage" {
		t.Fatal(seen)
	}
	connectionCall(t, r, "owner", "configure", "bale", map[string]string{"BALE_BOT_TOKEN": ""})
	if config.ToolEnvValue("BALE_BOT_TOKEN") != "" {
		t.Fatal("old environment token resurrected")
	}
	if _, e := r.SendIranMessenger(context.Background(), "bale", "", "chat", "message"); e == nil {
		t.Fatal("cleared token used")
	}
	out = connectionCall(t, r, "owner", "configure", "bale", map[string]string{"MACRON_BALE_API": "secret-token-not-an-address"})
	if !strings.Contains(out, "http(s) URL") || strings.Contains(out, "secret-token-not-an-address") {
		t.Fatal("invalid URL accepted or leaked", out)
	}
	out = connectionCall(t, r, "owner", "configure", "bale", map[string]string{"MACRON_API_KEY": "illegal"})
	if !strings.Contains(out, "unsupported setting") {
		t.Fatal(out)
	}
	out = connectionCall(t, r, "owner", "configure", "bale", map[string]string{"MACRON_MESSENGER": "eitaa"})
	if !strings.Contains(out, "error:") {
		t.Fatal(out)
	}
	args := tools.VisibleToolArgs(tools.ToolConnections, `{"action":"configure","values":{"BALE_BOT_TOKEN":"new-secret"}}`)
	if strings.Contains(args, "new-secret") || !strings.Contains(args, "hidden") {
		t.Fatal(args)
	}
}
func TestConnectionsGmailProbeRefreshNoMail(t *testing.T) {
	calls, refresh := 0, 0
	r := connectionRegistry(t, &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.URL.Host == "oauth2.googleapis.com" {
			refresh++
			return response(200, `{"access_token":"fresh"}`), nil
		}
		if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/profile") {
			t.Fatal("probe sent mail", req.URL)
		}
		if req.Header.Get("Authorization") == "Bearer expired" {
			return response(401, `{}`), nil
		}
		return response(200, `{"emailAddress":"owner@example.invalid"}`), nil
	})})
	t.Setenv("MACRON_GMAIL_ACCESS_TOKEN", "expired")
	t.Setenv("MACRON_GMAIL_CLIENT_ID", "client")
	t.Setenv("MACRON_GMAIL_CLIENT_SECRET", "secret")
	t.Setenv("MACRON_GMAIL_REFRESH_TOKEN", "refresh")
	out := connectionCall(t, r, "owner", "test", "gmail", nil)
	if !strings.Contains(out, "succeeded") || refresh != 1 || calls != 3 {
		t.Fatal(out, refresh, calls)
	}
}
func TestConnectionsSMTPProbeNoData(t *testing.T) {
	r := connectionRegistry(t, nil)
	port, received := smtpServer(t, "implicit")
	connectionCall(t, r, "owner", "configure", "smtp", map[string]string{"MACRON_SMTP_HOST": "127.0.0.1", "MACRON_SMTP_PORT": fmt.Sprint(port), "MACRON_SMTP_USER": "owner@example.invalid", "MACRON_SMTP_PASS": "test", "MACRON_SMTP_TLS": "implicit"})
	out := connectionCall(t, r, "owner", "test", "smtp", nil)
	if !strings.Contains(out, "login succeeded") {
		t.Fatal(out)
	}
	select {
	case <-received:
		t.Fatal("probe sent mail")
	default:
	}
}
func TestConnectionsGitHubClearingFallback(t *testing.T) {
	r := connectionRegistry(t, nil)
	r.GitHubToken = "startup-token"
	connectionCall(t, r, "owner", "configure", "github", map[string]string{"GITHUB_TOKEN": ""})
	_, e := r.ExecuteGitHubWorkflow(context.Background(), "owner", "list_issues", "owner/repo", "", "", "", "", 0)
	if e == nil || !strings.Contains(e.Error(), "not set") {
		t.Fatal(e)
	}
}
func TestToolEnvQuotedRoundTrip(t *testing.T) {
	t.Setenv("MACRON_HOME", t.TempDir())
	t.Setenv("MACRON_ENV_FILE", "")
	value := "a' \" # $ complex secret"
	if e := config.SaveToolEnv(map[string]string{"TEST_SECRET": value}); e != nil {
		t.Fatal(e)
	}
	if config.ToolEnvValue("TEST_SECRET") != value {
		t.Fatal("secret changed")
	}
	if e := config.SaveToolEnv(map[string]string{"TEST_SECRET": "bad\nline"}); e == nil {
		t.Fatal("newline accepted")
	}
}
func TestLiveQwenConversationalConnection(t *testing.T) {
	key := liveModelKey(t)
	t.Setenv("MACRON_HOME", t.TempDir())
	t.Setenv("MACRON_ENV_FILE", "")
	client := llm.NewClient(liveModelURL(), strings.TrimSpace(string(key)), liveModelName())
	client.MaxRetries = 0
	traced := &tracedClient{c: client}
	m, e := machine.New(machine.Config{BaseDir: t.TempDir()})
	if e != nil {
		t.Fatal(e)
	}
	probes := 0
	reg := tools.NewRegistry(m, nil, traced, tools.Options{ConnectionAdmin: func(string) bool { return true }, HTTP: &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(req.URL.Path, "/getMe") {
			t.Error("unexpected operation", req.URL.Path)
		}
		probes++
		return response(200, `{"ok":true,"result":{"id":99,"is_bot":true}}`), nil
	})}})
	a := agent.NewAgent(traced, reg, agent.Options{MaxSteps: 10, Channel: agent.ChannelCLI, Soul: func() string {
		return "You are Macron. Use tools to perform requested configuration and report only verified facts. Never echo secret tokens."
	}})
	ctx, cancel := context.WithTimeout(context.Background(), 110*time.Second)
	defer cancel()
	res, e := a.Do(ctx, agent.Request{SessionID: "owner", Owner: true, FocusTools: true, EfficientTools: true, Input: llm.Message{Content: "بله رو با توکن آزمایشی mock-secret-connection-628 و آیدی مالک 42 برای چت و اجرای ابزار تنظیم کن و اتصالش رو بدون ارسال پیام تست کن. سرویس رو اجرا نکن. توکن رو توی پاسخ تکرار نکن."}})
	if e != nil {
		t.Fatal(e)
	}
	if res == nil || config.ToolEnvValue("BALE_BOT_TOKEN") != "mock-secret-connection-628" || config.ToolEnvValue("MACRON_BALE_OWNERS") != "42" || probes == 0 {
		if res != nil {
			t.Fatal("configuration not executed", probes, res.Answer, traced.names)
		}
		t.Fatal("no response")
	}
	if strings.Contains(res.Answer, "mock-secret-connection-628") {
		t.Fatal("secret echoed")
	}
	found := false
	for _, n := range traced.names {
		found = found || n == agent.ToolSearch
	}
	if !found {
		t.Fatal("discovery skipped", traced.names)
	}
	t.Log("live conversational discovery/configure/read-only probe passed; calls:", traced.names, "schema counts:", traced.counts)
}

func TestConnectionReadOnlyProbes(t *testing.T) {
	cases := []struct {
		service, method, body string
		values                map[string]string
	}{
		{"rubika", "POST", `{"status":"OK","data":{"bot":{"bot_id":"1"}}}`, map[string]string{"RUBIKA_BOT_TOKEN": "fixture-token"}},
		{"telegram", "POST", `{"ok":true,"result":{"id":1}}`, map[string]string{"TELEGRAM_BOT_TOKEN": "fixture-token"}},
		{"slack", "POST", `{"ok":true,"user_id":"U1"}`, map[string]string{"SLACK_BOT_TOKEN": "fixture-token"}},
		{"discord", "GET", `{"id":"1"}`, map[string]string{"DISCORD_WEBHOOK_URL": "https://discord.invalid/api/webhooks/1/token-fixture"}},
		{"whatsapp", "GET", `{"id":"1"}`, map[string]string{"WHATSAPP_TOKEN": "fixture-token", "WHATSAPP_PHONE_ID": "1"}},
		{"notion", "GET", `{"id":"1"}`, map[string]string{"NOTION_TOKEN": "fixture-token"}},
		{"trello", "GET", `{"id":"1"}`, map[string]string{"TRELLO_API_KEY": "fixture-key", "TRELLO_TOKEN": "fixture-token"}},
		{"todoist", "GET", `{"id":"1"}`, map[string]string{"TODOIST_TOKEN": "fixture-token"}},
		{"github", "GET", `{"id":1}`, map[string]string{"GITHUB_TOKEN": "fixture-token"}},
		{"s3", "GET", `<ListBucketResult><Name>fixture-bucket</Name></ListBucketResult>`, map[string]string{"S3_ENDPOINT": "https://s3.invalid", "S3_ACCESS_KEY_ID": "fixture-key", "S3_SECRET_ACCESS_KEY": "fixture-secret"}},
		{"webdav", "PROPFIND", `<d:multistatus xmlns:d="DAV:"><d:response><d:href>/</d:href><d:propstat><d:prop><d:resourcetype><d:collection/></d:resourcetype></d:prop></d:propstat></d:response></d:multistatus>`, map[string]string{"WEBDAV_URL": "https://webdav.invalid", "WEBDAV_USER": "user", "WEBDAV_PASS": "fixture-password"}},
	}
	for _, c := range cases {
		t.Run(c.service, func(t *testing.T) {
			calls := 0
			r := connectionRegistry(t, &http.Client{Transport: roundtrip(func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != c.method {
					t.Fatalf("unexpected write: %s %s", req.Method, req.URL.Path)
				}
				if strings.Contains(req.URL.Path, "sendMessage") || strings.Contains(req.URL.Path, "/send") {
					t.Fatal("sent a message")
				}
				status := 200
				if c.service == "webdav" {
					status = 207
				}
				return response(status, c.body), nil
			})})
			if e := config.SaveToolEnv(c.values); e != nil {
				t.Fatal(e)
			}
			raw, _ := json.Marshal(map[string]string{"action": "test", "service": c.service, "resource": "fixture-bucket"})
			out, e := r.Execute(context.Background(), "owner", tools.ToolConnections, string(raw))
			if e != nil || strings.HasPrefix(out, "error:") || calls != 1 {
				t.Fatal(out, e, calls)
			}
		})
	}
}
