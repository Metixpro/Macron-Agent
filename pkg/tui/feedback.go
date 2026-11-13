package tui

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

var statusRe = regexp.MustCompile(`status (\d{3})`)

func explainError(err error) (string, string) {
	s := strings.ToLower(err.Error())
	code := 0
	if m := statusRe.FindStringSubmatch(s); m != nil {
		code, _ = strconv.Atoi(m[1])
	}
	with := func(text string) string {
		if code > 0 {
			return fmt.Sprintf("%s (%d)", text, code)
		}
		return text
	}
	switch {
	case strings.Contains(s, "no provider") || strings.Contains(s, "no model provider"):
		return "No model is connected", "Connect one with /connect model"
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(s, "deadline exceeded") || strings.Contains(s, "timed out"):
		return "The request ran out of time", "Try again, or split the job into smaller steps"
	case code == 401 || code == 403 || strings.Contains(s, "invalid api key") || strings.Contains(s, "unauthorized"):
		return with("The provider refused the API key"), "Check the key with /connect model, or switch with /provider"
	case code == 429 || strings.Contains(s, "rate limit"):
		return with("The provider is limiting requests"), "Wait a moment and try again, or switch with /provider"
	case strings.Contains(s, "context") && (strings.Contains(s, "too long") || strings.Contains(s, "maximum context") || strings.Contains(s, "context length")):
		return "The conversation is too long for this model", "Free room with /compact, or start fresh with /new"
	case code == 404 || strings.Contains(s, "model_not_found") || strings.Contains(s, "does not exist") || strings.Contains(s, "not available"):
		return with("The model was not found or is not available"), "Pick another one with /model"
	case code >= 500 || strings.Contains(s, "overloaded") || strings.Contains(s, "unavailable"):
		return with("The model is unavailable right now"), "Try again in a moment, or switch with /model or /provider"
	case strings.Contains(s, "connection refused") || strings.Contains(s, "no such host") || strings.Contains(s, "network is unreachable") ||
		strings.Contains(s, "connection reset"):
		return "Could not reach the model provider", "Check the network, or the provider's address with /connect model"
	}
	return firstLine(err.Error()), ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if r := []rune(s); len(r) > 160 {
		s = string(r[:160]) + "…"
	}
	return s
}

func errorBlock(err error, width int) string {
	title, hint := explainError(err)
	indent := func(text string, st func(...string) string) string {
		return "  " + strings.ReplaceAll(st(wrap(text, max(width-4, 10))), "\n", "\n  ")
	}
	out := stFail.Render("✗ ") + strings.ReplaceAll(stBold.Render(wrap(title, max(width-4, 10))), "\n", "\n  ")
	if hint != "" {
		out += "\n" + indent(hint, stDim.Render)
	}
	if raw := oneLine(errText(err)); raw != title {
		out += "\n" + "  " + stFaint.Render(clip(raw, max(width-4, 10)))
	}
	return out
}

func notify(title string) {
	_, _ = os.Stdout.WriteString("\a\x1b]9;" + strings.NewReplacer("\x07", "", "\x1b", "").Replace(title) + "\x07")
}

func copyToClipboard(text string) {
	_, _ = os.Stdout.WriteString("\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\x07")
}
