package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
)

func (r *Registry) gateAction(ctx context.Context, sessionID string, a cmdgate.Action) string {
	if r.Gate == nil {
		return ""
	}
	if v := r.Gate.CheckAction(ctx, sessionID, a); !v.Allow {
		return v.Message
	}
	return ""
}

var (
	submitRe      = regexp.MustCompile(`(?i)submit`)
	actionVerbRe  = regexp.MustCompile(`(?i)\b(?:send|post|publish|tweet|reply|pay|buy|order|checkout|check out|purchase|subscribe|donate|transfer|withdraw|confirm|delete|remove|unsubscribe|sign ?up|register|apply|book|reserve)\b|ارسال|پرداخت|خرید|ثبت|تأیید|تایید|حذف|انتشار|سفارش|رزرو|انتقال|واریز|برداشت|اشتراک`)
	searchFieldRe = regexp.MustCompile(`(?i)search|query|name=["']?q["'\]]|#q\b|جستجو|جست.?وجو`)
	secretFieldRe = regexp.MustCompile(`(?i)pass|pwd|\bpin\b|otp|cvv|cvc|card|token|secret|رمز`)
	pageActRe     = regexp.MustCompile(`submit\s*\(|requestSubmit|fetch\s*\(|XMLHttpRequest|sendBeacon|WebSocket|\.click\s*\(|dispatchEvent|document\.cookie|localStorage|sessionStorage|indexedDB|navigator\.credentials|\.value\s*=|location\s*=|location\.(?:href|assign|replace)|window\.open\s*\(`)
)

func (r *Registry) gateBrowser(ctx context.Context, sessionID, start string, acts []any) string {
	if r.Gate == nil || len(acts) == 0 {
		return ""
	}
	site := r.browserSite(sessionID, start)
	var typed []string
	searched := false
	asked := map[string]bool{}
	for i, a := range acts {
		act, _ := a.(map[string]any)
		if act == nil {
			continue
		}
		kind, _ := act["action"].(string)
		kind = strings.ToLower(kind)
		sel, _ := act["selector"].(string)
		var verb, what, rule, reason string
		switch kind {
		case "goto":
			if u, _ := act["url"].(string); u != "" {
				site = siteOf(u)
			}
			typed, searched = nil, false
		case "fill", "type", "select", "check", "uncheck":
			if searchFieldRe.MatchString(sel) {
				searched = true
			} else {
				typed = append(typed, typedValue(kind, sel, act))
			}
		case "click", "press":

			if key, _ := act["key"].(string); kind == "press" && key != "" && !strings.EqualFold(key, "Enter") && !strings.EqualFold(key, "NumpadEnter") {
				break
			}

			if len(typed) == 0 && !actionVerbRe.MatchString(sel) && (searched || !submitRe.MatchString(sel)) {
				break
			}
			verb, rule, reason = "send a form", "browser-submit", "sending a form or pressing an action button on a website"
			what = strings.TrimSpace(kind + " " + sel)
			if len(typed) > 0 {
				what += "\nafter typing: " + strings.Join(typed, "; ")
			}
			typed, searched = nil, false
		case "upload":
			verb, rule, reason = "upload files", "browser-upload", "uploading files to a website"
			what = "upload " + strings.Join(anyList(act["files"]), ", ") + " into " + sel
		case "eval":
			if script, _ := act["script"].(string); pageActRe.MatchString(script) {
				verb, rule, reason = "run a page script", "browser-script", "a page script that can send data or act on the site"
				what = "run on the page:\n" + firstRunes(script, 600)
			}
		}
		if rule == "" {
			continue
		}
		key := "[browser] " + verb + " on " + site
		if asked[key] {
			continue
		}
		asked[key] = true
		if refused := r.gateAction(ctx, sessionID, cmdgate.Action{Key: key, Detail: key + "\n" + strings.TrimSpace(what),
			Level: cmdgate.Dangerous, Rule: rule, Reason: reason}); refused != "" {
			return fmt.Sprintf("%s\n(Step %d was refused, so nothing in this call ran; reading the page needs no approval.)", refused, i+1)
		}
	}
	return ""
}

func (r *Registry) gatePlugin(ctx context.Context, sessionID string, p PluginTools, name string, args map[string]any) string {
	if r.Gate == nil {
		return ""
	}
	if t, ok := p.(interface{ Trusted(string) bool }); ok && t.Trusted(name) {
		return ""
	}
	data, _ := json.Marshal(args)
	key := "[plugin] " + name
	return r.gateAction(ctx, sessionID, cmdgate.Action{Key: key, Detail: key + " " + firstRunes(string(data), 800),
		Level: cmdgate.Dangerous, Rule: "plugin-call", Reason: "calling a plugin tool, which acts outside the agent"})
}

func (r *Registry) gateBrowserAgent(ctx context.Context, sessionID, task, start string, domains, files []string) string {
	scope := strings.Join(domains, ", ")
	if scope == "" {
		scope = "any site"
		if start != "" {
			scope += ", from " + siteOf(start)
		}
	}
	key := "[browser_agent] run on " + scope
	detail := key + "\ntask: " + firstRunes(task, 1500)
	if len(files) > 0 {
		detail += "\nfiles it may upload: " + strings.Join(files, ", ")
	}
	return r.gateAction(ctx, sessionID, cmdgate.Action{Key: key, Detail: detail, Level: cmdgate.Dangerous, Rule: "browser-agent",
		Reason: "an autonomous browser run, which can fill and send forms on its own"})
}

func (r *Registry) browserSite(sessionID, start string) string {
	if strings.TrimSpace(start) == "" && r.Machine != nil {
		if data, _, err := r.Machine.ReadFile(sessionID, ".browser/url", 4096); err == nil {
			start = string(data)
		}
	}
	return siteOf(start)
}

func siteOf(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "the open page"
	}
	if schemeRe.FindStringSubmatch(raw) == nil {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "a workspace page"
	}
	return strings.ToLower(u.Hostname())
}

func typedValue(kind, sel string, act map[string]any) string {
	switch kind {
	case "check":
		return sel + " ✓"
	case "uncheck":
		return sel + " ✗"
	}
	v := ""
	for _, k := range []string{"text", "value"} {
		if x, ok := act[k]; ok && x != nil {
			v = fmt.Sprint(x)
			break
		}
	}
	if v != "" && secretFieldRe.MatchString(sel) {
		v = "••••"
	}
	return sel + " ← " + firstRunes(v, 60)
}

func (r *Registry) gateSend(ctx context.Context, sessionID, where, detail string) string {
	key := "[send] " + where
	return r.gateAction(ctx, sessionID, cmdgate.Action{Key: key, Detail: key + "\n" + firstRunes(detail, 800),
		Level: cmdgate.Dangerous, Rule: "send-out", Reason: "sending something to " + where + " on the owner's behalf"})
}
