package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
)

type confidentialKey struct{}

// WithConfidential sets the confidential execution mode in context.
func WithConfidential(ctx context.Context, confidential bool) context.Context {
	return context.WithValue(ctx, confidentialKey{}, confidential)
}

// IsConfidential reports whether confidential mode is enabled for the request.
func IsConfidential(ctx context.Context) bool {
	if v, ok := ctx.Value(confidentialKey{}).(bool); ok {
		return v
	}
	env := strings.ToLower(strings.TrimSpace(os.Getenv("MACRON_CONFIDENTIAL")))
	return env == "1" || env == "true" || env == "on" || env == "yes"
}

// BlockedInConfidentialMode lists all external network tools forbidden in confidential mode.
var networkTools = map[string]bool{
	"web_search":            true,
	"web_fetch":             true,
	"deep_research":         true,
	"academic_search":       true,
	"crawl_site":            true,
	"fact_check":            true,
	"download_file":         true,
	"download_media_ytdlp":  true,
	"iran_messenger":        true,
	"external_messenger":    true,
	"cloud_storage":         true,
	"email_client":          true,
	"github_workflow":       true,
	"openapi_call":          true,
}

// CheckConfidentialGate returns an error message if an outbound network tool is called in confidential mode.
func CheckConfidentialGate(ctx context.Context, toolName string) (string, bool) {
	if !IsConfidential(ctx) {
		return "", false
	}
	if networkTools[toolName] {
		return fmt.Sprintf("🔒 [حالت محرمانه / Confidential Mode] ابزار «%s» مسدود است. در حالت محرمانه هیچ‌گونه ارتباط شبکه‌ای یا ارسال داده به خارج از سیستم مجاز نیست.", toolName), true
	}
	return "", false
}
