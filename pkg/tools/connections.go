package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/config"
)

const ToolConnections = "connections"
const ConnectionsGuide = "When the owner asks to connect or configure Gmail/email, Bale, Rubika, Eitaa, Telegram, Slack, Discord, WhatsApp, storage, boards or GitHub, discover connections with tool_search first. Use describe to learn the exact supported capabilities and settings, list to inspect configured fields without exposing secrets, configure to save provided settings, then test with a read-only call. Never call a saved token a verified connection. Service tool settings take effect on their next call; starting a messenger listener still needs its run command/service restart. Gmail supports OAuth search/read/send; SMTP only sends. Bale/Rubika have native text-chat runners; Eitaayar only sends. Do not substitute Telegram credentials/IDs for another messenger, invent OAuth tokens, print keys, send a test message without an explicit recipient instruction, or say a service is connected without a successful test. Follow describe field types and required_credential_alternatives. A missing optional URL override is not a missing credential: never ask for MACRON_BALE_API as an API key; leave it unset for the official service. Ask only for the specific missing credential or account choice after checking available configuration."

type ConnectionSpec struct {
	ID, Name, Capability string
	Fields               []string
	Alternatives         [][]string
	Restart              bool
}

var connectionSpecs = []ConnectionSpec{
	{"gmail", "Gmail", "OAuth: search, read and send mail", []string{"MACRON_GMAIL_ACCESS_TOKEN", "MACRON_GMAIL_CLIENT_ID", "MACRON_GMAIL_CLIENT_SECRET", "MACRON_GMAIL_REFRESH_TOKEN", "MACRON_GMAIL_FROM"}, [][]string{{"MACRON_GMAIL_ACCESS_TOKEN"}, {"MACRON_GMAIL_CLIENT_ID", "MACRON_GMAIL_CLIENT_SECRET", "MACRON_GMAIL_REFRESH_TOKEN"}}, false},
	{"smtp", "Email / SMTP", "Send mail; STARTTLS or implicit TLS, no inbox access", []string{"MACRON_SMTP_HOST", "MACRON_SMTP_PORT", "MACRON_SMTP_USER", "MACRON_SMTP_PASS", "MACRON_SMTP_FROM", "MACRON_SMTP_TLS"}, [][]string{{"MACRON_SMTP_HOST", "MACRON_SMTP_USER", "MACRON_SMTP_PASS"}}, false},
	{"bale", "Bale / بله", "Send messages; native private text chat: macron bale run", []string{"BALE_BOT_TOKEN", "MACRON_BALE_OWNERS", "MACRON_BALE_API"}, [][]string{{"BALE_BOT_TOKEN"}}, true},
	{"rubika", "Rubika / روبیکا", "Send messages; native private text chat: macron rubika run", []string{"RUBIKA_BOT_TOKEN", "MACRON_RUBIKA_OWNERS", "MACRON_RUBIKA_API"}, [][]string{{"RUBIKA_BOT_TOKEN"}}, true},
	{"eitaa", "Eitaa / ایتا", "Eitaayar outbound messages only; no native inbound listener", []string{"EITAA_BOT_TOKEN"}, [][]string{{"EITAA_BOT_TOKEN"}}, false},
	{"telegram", "Telegram", "Private bot chat, panels, files and tools", []string{"TELEGRAM_BOT_TOKEN", "MACRON_OWNERS", "MACRON_TELEGRAM_API"}, [][]string{{"TELEGRAM_BOT_TOKEN", "MACRON_OWNERS"}}, true},
	{"slack", "Slack", "Send through a webhook or bot token", []string{"SLACK_WEBHOOK_URL", "SLACK_BOT_TOKEN"}, [][]string{{"SLACK_WEBHOOK_URL"}, {"SLACK_BOT_TOKEN"}}, false},
	{"discord", "Discord", "Send through a webhook", []string{"DISCORD_WEBHOOK_URL"}, [][]string{{"DISCORD_WEBHOOK_URL"}}, false},
	{"whatsapp", "WhatsApp", "Send using WhatsApp Cloud API", []string{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_ID"}, [][]string{{"WHATSAPP_TOKEN", "WHATSAPP_PHONE_ID"}}, false},
	{"s3", "S3 / MinIO / R2", "List, upload and download objects", []string{"S3_ENDPOINT", "S3_REGION", "S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"}, [][]string{{"S3_ACCESS_KEY_ID", "S3_SECRET_ACCESS_KEY"}}, false},
	{"webdav", "WebDAV / Nextcloud", "List, upload and download files", []string{"WEBDAV_URL", "WEBDAV_USER", "WEBDAV_PASS"}, [][]string{{"WEBDAV_URL", "WEBDAV_USER", "WEBDAV_PASS"}}, false},
	{"notion", "Notion", "Create and list tasks in a database", []string{"NOTION_TOKEN", "NOTION_DATABASE_ID"}, [][]string{{"NOTION_TOKEN"}}, false},
	{"trello", "Trello", "Create and list cards", []string{"TRELLO_API_KEY", "TRELLO_TOKEN"}, [][]string{{"TRELLO_API_KEY", "TRELLO_TOKEN"}}, false},
	{"todoist", "Todoist", "Create and list tasks", []string{"TODOIST_TOKEN"}, [][]string{{"TODOIST_TOKEN"}}, false},
	{"github", "GitHub", "Inspect repositories, issues and pull requests; create issues, PRs and comments", []string{"GITHUB_TOKEN"}, [][]string{{"GITHUB_TOKEN"}}, false},
}

func ConnectionSpecs() []ConnectionSpec {
	out := make([]ConnectionSpec, len(connectionSpecs))
	for i, s := range connectionSpecs {
		out[i] = s
		out[i].Fields = append([]string(nil), s.Fields...)
		out[i].Alternatives = make([][]string, len(s.Alternatives))
		for j, alt := range s.Alternatives {
			out[i].Alternatives[j] = append([]string(nil), alt...)
		}
	}
	return out
}
func connectionSpec(id string) (ConnectionSpec, bool) {
	id = strings.ToLower(strings.TrimSpace(id))
	aliases := map[string]string{"email": "smtp", "بله": "bale", "ایتا": "eitaa", "روبیکا": "rubika", "minio": "s3", "r2": "s3", "nextcloud": "webdav"}
	if v := aliases[id]; v != "" {
		id = v
	}
	for _, s := range connectionSpecs {
		if s.ID == id {
			return s, true
		}
	}
	return ConnectionSpec{}, false
}
func ConnectionReady(s ConnectionSpec) bool {
	for _, alt := range s.Alternatives {
		ok := true
		for _, key := range alt {
			ok = ok && strings.TrimSpace(config.ToolEnvValue(key)) != ""
		}
		if ok {
			return true
		}
	}
	return false
}
func ConnectionsStatus() string {
	var lines []string
	for _, s := range connectionSpecs {
		state := "needs setup"
		if ConnectionReady(s) {
			state = "configured · not verified"
		}
		lines = append(lines, fmt.Sprintf("%-10s %s", s.ID, state))
	}
	return strings.Join(lines, "\n")
}
func (r *Registry) SetConnectionAdmin(allow func(string) bool) { r.connectionAdmin = allow }
func (r *Registry) ConnectionAllowed(session string) bool {
	return r.connectionAdmin != nil && r.connectionAdmin(session)
}
func (r *Registry) registerConnections() {
	r.add(ToolConnections, "Inspect, configure and read-only test service connections. List supported services; describe exact settings/capabilities before configure. Never expose saved secret values.", map[string]any{"action": enumProp("list", "list", "describe", "configure", "test"), "service": prop("string", "gmail, smtp, bale, rubika, eitaa, telegram, slack, discord, whatsapp, s3, webdav, notion, trello, todoist, github"), "values": map[string]any{"type": "object", "additionalProperties": map[string]string{"type": "string"}, "description": "configure: exact setting names from describe and their supplied values; empty value clears a setting"}, "resource": prop("string", "test: S3 bucket or optional WebDAV path")}, []string{"action"}, func(ctx context.Context, session string, args map[string]any) (string, error) {
		if !r.ConnectionAllowed(session) {
			return "error: only the owner can manage connections", nil
		}
		action := strArg(args, "action")
		if action == "list" || action == "" {
			return ConnectionsStatus() + "\nConfigured does not mean authenticated. Use describe, then read-only test.", nil
		}
		s, ok := connectionSpec(strArg(args, "service"))
		if !ok {
			return "error: unknown service; use connections action=list", nil
		}
		switch action {
		case "describe":
			return describeConnection(s), nil
		case "configure":
			raw, _ := args["values"].(map[string]any)
			if len(raw) == 0 {
				return "error: values must contain settings from describe", nil
			}
			values := map[string]string{}
			allowed := map[string]bool{}
			for _, key := range s.Fields {
				allowed[key] = true
			}
			var keys []string
			for key, v := range raw {
				value, ok := v.(string)
				if !ok || !allowed[key] {
					return "error: unsupported setting " + key + " for " + s.ID, nil
				}
				if strings.Contains(key, "_PORT") && value != "" {
					n, err := strconv.Atoi(value)
					if err != nil || n < 1 || n > 65535 {
						return "error: invalid port", nil
					}
				}
				if key == "MACRON_SMTP_TLS" && value != "" && value != "implicit" {
					return "error: MACRON_SMTP_TLS must be empty or implicit", nil
				}
				if key == "MACRON_MESSENGER" && value != "" && value != "bale" && value != "rubika" && value != "telegram" {
					return "error: MACRON_MESSENGER must be bale, rubika or telegram", nil
				}
				values[key] = value
				keys = append(keys, key)
			}
			sort.Strings(keys)
			if r.Gate != nil {
				v := r.Gate.CheckAction(ctx, session, cmdgate.Action{Key: "[connections] " + s.ID, Detail: "Configure " + s.ID + ": " + strings.Join(keys, ", "), Level: cmdgate.Dangerous, Rule: "service-config", Reason: "changing service settings"})
				if !v.Allow {
					return v.Message, nil
				}
			}
			if err := config.SaveToolEnv(values); err != nil {
				return "error: saving connection settings failed: " + err.Error(), nil
			}
			return "Saved " + s.ID + " settings securely. Not verified yet; call connections action=test. Service tools use these settings on the next call. Native messenger listeners need a restart.", nil
		case "test":
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			out, err := r.testConnection(ctx, s.ID, strArg(args, "resource"))
			if err != nil {
				return "error: " + err.Error(), nil
			}
			return out, nil
		}
		return "error: unknown connections action", nil
	})
}

func (r *Registry) testConnection(ctx context.Context, id, resource string) (string, error) {
	token := func(key string) string { return config.ToolEnvValue(key) }
	request := func(endpoint string, headers map[string]string, secrets ...string) (string, error) {
		data, err := apiCall(ctx, r.cloudClient(), http.MethodGet, endpoint, headers, nil, secrets...)
		if err != nil {
			return "", err
		}
		var body map[string]any
		if err := json.Unmarshal(data, &body); err != nil {
			return "", fmt.Errorf("service returned an invalid response")
		}
		if body["id"] == nil && body["emailAddress"] == nil {
			return "", fmt.Errorf("service did not confirm an authenticated account")
		}
		return id + ": authenticated read-only request succeeded; no message sent", nil
	}
	switch id {
	case "gmail":
		return r.Gmail(ctx, "profile", "", "", "", "", "", 0)
	case "bale", "rubika", "telegram":
		key := "BALE_BOT_TOKEN"
		if id == "rubika" {
			key = "RUBIKA_BOT_TOKEN"
		}
		if id == "telegram" {
			key = "TELEGRAM_BOT_TOKEN"
		}
		t := token(key)
		if t == "" {
			return "", fmt.Errorf("%s is missing", key)
		}
		data, _ := json.Marshal(map[string]any{})
		endpoint, err := messengerEndpoint(id, t, "getMe")
		if err != nil {
			return "", err
		}
		_, err = sendHTTPJSON(ctx, r.cloudClient(), endpoint, data, t)
		if err != nil {
			return "", err
		}
		return id + ": getMe succeeded; no message sent", nil
	case "eitaa":
		return "Eitaayar token can be saved, but no supported read-only account probe is available here. No test message was sent; delivery remains unverified.", nil
	case "smtp":
		if err := TestSMTP(ctx); err != nil {
			return "", err
		}
		return "SMTP login succeeded; no email sent", nil
	case "slack":
		t := token("SLACK_BOT_TOKEN")
		if t == "" {
			return "Slack webhook configured status can be inspected, but verifying delivery would send a message. No message sent.", nil
		}
		data, err := apiCall(ctx, r.cloudClient(), http.MethodPost, "https://slack.com/api/auth.test", map[string]string{"Authorization": "Bearer " + t}, map[string]any{}, t)
		if err != nil {
			return "", err
		}
		var v struct {
			OK    bool   `json:"ok"`
			Error string `json:"error"`
		}
		if json.Unmarshal(data, &v) != nil || !v.OK {
			return "", fmt.Errorf("Slack authentication refused: %s", v.Error)
		}
		return "Slack authentication succeeded; no message sent", nil
	case "discord":
		u := token("DISCORD_WEBHOOK_URL")
		if u == "" {
			return "", fmt.Errorf("DISCORD_WEBHOOK_URL is missing")
		}
		return request(u, nil, u)
	case "whatsapp":
		t := token("WHATSAPP_TOKEN")
		return request("https://graph.facebook.com/v19.0/"+url.PathEscape(token("WHATSAPP_PHONE_ID")), map[string]string{"Authorization": "Bearer " + t}, t)
	case "s3":
		if resource == "" {
			return "", fmt.Errorf("test requires resource: the S3 bucket name")
		}
		return r.ExecuteCloudStorage(ctx, "connections-check", "s3", "list", resource, "", "", "", "")
	case "webdav":
		return r.ExecuteCloudStorage(ctx, "connections-check", "webdav", "list", resource, "", "", "", "")
	case "notion":
		t := token("NOTION_TOKEN")
		return request("https://api.notion.com/v1/users/me", map[string]string{"Authorization": "Bearer " + t, "Notion-Version": "2022-06-28"}, t)
	case "trello":
		k, t := token("TRELLO_API_KEY"), token("TRELLO_TOKEN")
		return request("https://api.trello.com/1/members/me?"+url.Values{"key": {k}, "token": {t}}.Encode(), nil, k, t)
	case "todoist":
		t := token("TODOIST_TOKEN")
		return request("https://api.todoist.com/api/v1/user", map[string]string{"Authorization": "Bearer " + t}, t)
	case "github":
		t := r.githubToken()
		return request("https://api.github.com/user", map[string]string{"Authorization": "Bearer " + t, "Accept": "application/vnd.github+json"}, t)
	}
	return "", fmt.Errorf("unsupported service")
}

func VisibleToolArgs(name, args string) string {
	if name != ToolConnections {
		return args
	}
	var a map[string]any
	if json.Unmarshal([]byte(args), &a) != nil {
		return `{"action":"configure","values":"[hidden]"}`
	}
	if values, ok := a["values"].(map[string]any); ok {
		for key := range values {
			values[key] = "[hidden]"
		}
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return `{"values":"[hidden]"}`
	}
	return string(raw)
}
