package tools

import (
	"encoding/json"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/config"
)

type connectionField struct {
	Key         string   `json:"key"`
	Type        string   `json:"type"`
	Purpose     string   `json:"purpose"`
	Default     string   `json:"default,omitempty"`
	RequiredFor []string `json:"required_for,omitempty"`
	Secret      bool     `json:"secret"`
	State       string   `json:"state"`
}

var connectionFields = map[string]connectionField{
	"MACRON_GMAIL_ACCESS_TOKEN":  {Type: "credential", Secret: true, Purpose: "Google-authorized OAuth access token; alternative to the refresh-token flow"},
	"MACRON_GMAIL_CLIENT_ID":     {Type: "identifier", Purpose: "Google OAuth client ID for the refresh-token flow"},
	"MACRON_GMAIL_CLIENT_SECRET": {Type: "credential", Secret: true, Purpose: "Google OAuth client secret for the refresh-token flow"},
	"MACRON_GMAIL_REFRESH_TOKEN": {Type: "credential", Secret: true, Purpose: "Offline Google OAuth refresh token; use with client ID and client secret"},
	"MACRON_GMAIL_FROM":          {Type: "email", Purpose: "Sender email address; not required for search/read", RequiredFor: []string{"send"}},
	"MACRON_SMTP_HOST":           {Type: "hostname", Purpose: "SMTP server hostname"},
	"MACRON_SMTP_PORT":           {Type: "port", Default: "587", Purpose: "SMTP port; use the port required by your mail provider"},
	"MACRON_SMTP_USER":           {Type: "identifier", Purpose: "SMTP login username"},
	"MACRON_SMTP_PASS":           {Type: "credential", Secret: true, Purpose: "SMTP password or provider app password"},
	"MACRON_SMTP_FROM":           {Type: "email", Purpose: "Sender email; defaults to SMTP username"},
	"MACRON_SMTP_TLS":            {Type: "enum", Purpose: "Optional override: implicit. Leave unset for STARTTLS; port 465 selects implicit TLS automatically"},
	"BALE_BOT_TOKEN":             {Type: "credential", Secret: true, Purpose: "Bale bot token from Bale's own bot-management service; never substitute a Telegram token"},
	"MACRON_BALE_OWNERS":         {Type: "id_list", Purpose: "Comma-separated Bale owner user IDs; needed for incoming private chat, not for outbound-only use", RequiredFor: []string{"native_chat"}},
	"MACRON_BALE_API":            {Type: "url", Default: "https://tapi.bale.ai/bot", Purpose: "Optional API base URL override for a trusted relay. This is an address, NOT an API key or bot ID; leave unset for the official service"},
	"RUBIKA_BOT_TOKEN":           {Type: "credential", Secret: true, Purpose: "Rubika bot token from Rubika"},
	"MACRON_RUBIKA_OWNERS":       {Type: "id_list", Purpose: "Comma-separated Rubika owner user IDs", RequiredFor: []string{"native_chat"}},
	"MACRON_RUBIKA_API":          {Type: "url", Default: "https://botapi.rubika.ir/v3/", Purpose: "Optional trusted API base URL override; not a credential"},
	"EITAA_BOT_TOKEN":            {Type: "credential", Secret: true, Purpose: "Eitaayar outbound bot token; native inbound chat is not supported"},
	"TELEGRAM_BOT_TOKEN":         {Type: "credential", Secret: true, Purpose: "Telegram bot token from Telegram's @BotFather"},
	"MACRON_OWNERS":              {Type: "id_list", Purpose: "Comma-separated Telegram owner user IDs", RequiredFor: []string{"native_chat"}},
	"MACRON_TELEGRAM_API":        {Type: "url", Default: "https://api.telegram.org", Purpose: "Optional trusted Telegram API base URL override; not a credential"},
	"SLACK_WEBHOOK_URL":          {Type: "url", Secret: true, Purpose: "Slack incoming webhook URL; alternative to a bot token"},
	"SLACK_BOT_TOKEN":            {Type: "credential", Secret: true, Purpose: "Slack bot token; alternative to an incoming webhook"},
	"DISCORD_WEBHOOK_URL":        {Type: "url", Secret: true, Purpose: "Discord webhook URL, including its secret token"},
	"WHATSAPP_TOKEN":             {Type: "credential", Secret: true, Purpose: "WhatsApp Cloud API access token"},
	"WHATSAPP_PHONE_ID":          {Type: "identifier", Purpose: "WhatsApp Cloud API phone-number ID, not the phone number"},
	"S3_ENDPOINT":                {Type: "url", Purpose: "Optional S3-compatible endpoint for MinIO/R2 or another provider"},
	"S3_REGION":                  {Type: "identifier", Default: "us-east-1", Purpose: "S3 signing region"},
	"S3_ACCESS_KEY_ID":           {Type: "credential", Secret: true, Purpose: "S3 access key ID"},
	"S3_SECRET_ACCESS_KEY":       {Type: "credential", Secret: true, Purpose: "S3 secret access key"},
	"WEBDAV_URL":                 {Type: "url", Purpose: "WebDAV server base URL"},
	"WEBDAV_USER":                {Type: "identifier", Purpose: "WebDAV username"},
	"WEBDAV_PASS":                {Type: "credential", Secret: true, Purpose: "WebDAV password or app password"},
	"NOTION_TOKEN":               {Type: "credential", Secret: true, Purpose: "Notion integration token"},
	"NOTION_DATABASE_ID":         {Type: "identifier", Purpose: "Notion database to work with", RequiredFor: []string{"database_tasks"}},
	"TRELLO_API_KEY":             {Type: "credential", Secret: true, Purpose: "Trello API key"},
	"TRELLO_TOKEN":               {Type: "credential", Secret: true, Purpose: "Trello authorization token"},
	"TODOIST_TOKEN":              {Type: "credential", Secret: true, Purpose: "Todoist API token"},
	"GITHUB_TOKEN":               {Type: "credential", Secret: true, Purpose: "GitHub token with only the repository permissions needed for the requested action"},
}

func describeConnection(s ConnectionSpec) string {
	fields := make([]connectionField, 0, len(s.Fields))
	for _, key := range s.Fields {
		f := connectionFields[key]
		f.Key = key
		f.State = "missing"
		if strings.TrimSpace(config.ToolEnvValue(key)) != "" {
			f.State = "set"
		}
		fields = append(fields, f)
	}
	notes := "Required credential alternatives describe basic service access; required_for lists extra action-specific fields. Fields outside both are optional. A missing optional field is not a setup failure. Configured does not mean authenticated; use a read-only test."
	if s.Restart {
		notes += " Native chat needs owner IDs from that same platform. After saving, restart the listener with macron telegram restart; configured messengers can run side by side."
	}
	if s.ID == "gmail" {
		notes += " OAuth scopes: gmail.readonly for search/read, gmail.send for sending. Never invent OAuth credentials."
	}
	out := struct {
		Service      string            `json:"service"`
		Name         string            `json:"name"`
		Capability   string            `json:"capability"`
		Settings     []connectionField `json:"settings"`
		Alternatives [][]string        `json:"required_credential_alternatives"`
		Notes        string            `json:"notes"`
	}{s.ID, s.Name, s.Capability, fields, s.Alternatives, notes}
	raw, _ := json.Marshal(out)
	return string(raw)
}
