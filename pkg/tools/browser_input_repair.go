package tools

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	// Matches revisioned snapshot refs: rev:frame:idx (e.g. "3:main:12", "4:f1:0")
	// optionally wrapped in brackets or followed by pasted snapshot descriptions.
	snapshotRefLineRe = regexp.MustCompile(`^\s*(?:[-*•]\s*)?[\[\(]?(\d+:(?:main|f\d+):\d+)[\]\)]?(?:\s+.*)?$`)
	// Matches OpenBot-style `role <role> "<name>"` or `role <role> <name>`
	rolePrefixRe = regexp.MustCompile(`(?i)^role\s+([a-z][a-z0-9_-]*)(?:\s+(?:"([^"]+)"|'([^']+)'|(.+)))?$`)
	// Matches OpenBot-style `css <selector>`
	cssPrefixRe = regexp.MustCompile(`(?i)^css\s+(.+)$`)
)

// repairBrowserToolArgs applies forgiving OpenBot-style repairs to LLM-supplied
// browser tool arguments so minor casing/aliasing mistakes or pasted snapshot
// lines don't waste an agent turn. It never alters raw JS scripts or sensitive
// input values.
func repairBrowserToolArgs(raw map[string]any) (map[string]any, []string) {
	if raw == nil {
		return map[string]any{}, nil
	}
	out := make(map[string]any, len(raw))
	for k, v := range raw {
		out[k] = v
	}
	var notes []string

	topAliases := map[string]string{
		"fullPage":       "full_page",
		"closeAfter":     "close_after",
		"newSession":     "new_session",
		"timeoutSeconds": "timeout_seconds",
		"timeout":        "timeout_seconds",
		"waitUntil":      "until",
		"wait_until":     "until",
		"profileId":      "profile",
		"profile_id":     "profile",
		"runScript":      "script",
		"run_script":     "script",
		"fillFields":     "fill_fields",
	}
	for alias, canonical := range topAliases {
		if v, ok := out[alias]; ok {
			if _, exists := out[canonical]; !exists {
				out[canonical] = v
				notes = append(notes, alias+"→"+canonical)
			}
			delete(out, alias)
		}
	}

	if u, ok := out["until"].(string); ok && strings.TrimSpace(u) != "" {
		norm := normalizeWaitUntil(u)
		if norm != u {
			out["until"] = norm
			notes = append(notes, "until:"+u+"→"+norm)
		}
	}

	// Promote top-level shorthand fill_fields or script into actions if actions is omitted.
	if ff, ok := out["fill_fields"]; ok {
		acts, _ := out["actions"].([]any)
		if items, ok := ff.([]any); ok && len(items) > 0 {
			acts = append(acts, map[string]any{"action": "fill_fields", "fields": items})
			out["actions"] = acts
			notes = append(notes, "promoted fill_fields")
		}
		delete(out, "fill_fields")
	}

	if acts, ok := out["actions"].([]any); ok {
		repaired := make([]any, 0, len(acts))
		for _, item := range acts {
			m, ok := item.(map[string]any)
			if !ok {
				repaired = append(repaired, item)
				continue
			}
			rm, anotes := repairBrowserAction(m)
			repaired = append(repaired, rm)
			notes = append(notes, anotes...)
		}
		out["actions"] = repaired
	}

	return out, notes
}

func repairBrowserAction(in map[string]any) (map[string]any, []string) {
	act := make(map[string]any, len(in))
	for k, v := range in {
		act[k] = v
	}
	var notes []string

	// 1. Action key aliases
	if _, hasAction := act["action"]; !hasAction {
		for _, k := range []string{"op", "cmd", "command", "kind"} {
			if v, ok := act[k]; ok {
				act["action"] = v
				delete(act, k)
				notes = append(notes, k+"→action")
				break
			}
		}
		if _, hasAction := act["action"]; !hasAction {
			if t, ok := act["type"].(string); ok {
				if _, isAction := browserActions[strings.ToLower(strings.TrimSpace(t))]; isAction {
					act["action"] = t
					delete(act, "type")
					notes = append(notes, "type→action")
				}
			}
		}
	}

	// Normalize action verb casing & common synonyms
	if rawVerb, ok := act["action"].(string); ok {
		verb := strings.ToLower(strings.TrimSpace(rawVerb))
		verbAliases := map[string]string{
			"open":             "goto",
			"navigate":         "goto",
			"visit":            "goto",
			"tap":              "click",
			"input":            "fill",
			"set_value":        "fill",
			"fillFields":       "fill_fields",
			"fillfields":       "fill_fields",
			"batch_fill":       "fill_fields",
			"runScript":        "run_script",
			"runscript":        "run_script",
			"execute_script":   "run_script",
			"handleDialog":     "handle_dialog",
			"handledialog":     "handle_dialog",
			"dialog":           "handle_dialog",
			"requestTakeover":  "request_takeover",
			"requesttakeover":  "request_takeover",
			"takeover":         "request_takeover",
			"saveSiteMemory":   "save_site_memory",
			"deleteSiteMemory": "delete_site_memory",
			"switchTab":        "switch_tab",
			"closeTab":         "close_tab",
			"newTab":           "new_tab",
			"clickAt":          "click_at",
			"moveAt":           "move_at",
			"dragAt":           "drag_at",
			"go_back":          "back",
			"go_forward":       "forward",
			"refresh":          "reload",
			"key":              "press",
			"keypress":         "press",
			"evaluate":         "eval",
		}
		if mapped, ok := verbAliases[rawVerb]; ok {
			verb = mapped
		} else if mapped, ok := verbAliases[verb]; ok {
			verb = mapped
		}
		if verb != rawVerb {
			act["action"] = verb
			notes = append(notes, "verb:"+rawVerb+"→"+verb)
		}
	}

	// Skip aggressive field aliasing inside eval/run_script bodies.
	verb, _ := act["action"].(string)
	if verb == "eval" || verb == "run_script" {
		if _, hasScript := act["script"]; !hasScript {
			for _, k := range []string{"code", "js", "expression", "source"} {
				if v, ok := act[k]; ok {
					act["script"] = v
					delete(act, k)
					notes = append(notes, k+"→script")
					break
				}
			}
		}
		return act, notes
	}

	// 2. Parameter aliases (camelCase / LLM habits -> canonical snake_case)
	paramAliases := map[string]string{
		"target":      "selector",
		"ref":         "selector",
		"element":     "selector",
		"el":          "selector",
		"locator":     "selector",
		"pressEnter":  "submit",
		"press_enter": "submit",
		"waitUntil":   "until",
		"wait_until":  "until",
		"fullPage":    "full_page",
		"delayMs":     "delay_ms",
		"toSelector":  "to_selector",
		"toTarget":    "to_selector",
		"to_target":   "to_selector",
		"toX":         "to_x",
		"toY":         "to_y",
		"clickCount":  "count",
		"click_count": "count",
		"promptText":  "prompt_text",
		"timeoutMs":   "timeout_ms",
		"tabIndex":    "index",
		"tab_index":   "index",
	}
	for alias, canonical := range paramAliases {
		if v, ok := act[alias]; ok {
			if _, exists := act[canonical]; !exists {
				act[canonical] = v
				notes = append(notes, alias+"→"+canonical)
			}
			delete(act, alias)
		}
	}

	// "value" -> "text" for fill/type (keep "value" for select!)
	if verb == "fill" || verb == "type" {
		if v, ok := act["value"]; ok {
			if _, exists := act["text"]; !exists {
				act["text"] = fmt.Sprint(v)
				notes = append(notes, "value→text")
			}
			delete(act, "value")
		}
	}

	// 3. Target / selector repair (strip pasted snapshot lines, `css ...`, `role ...`)
	for _, selKey := range []string{"selector", "to_selector"} {
		if rawSel, ok := act[selKey].(string); ok && strings.TrimSpace(rawSel) != "" {
			cleaned := repairTargetSelector(rawSel)
			if cleaned != rawSel {
				act[selKey] = cleaned
				notes = append(notes, selKey+":repaired")
			}
		}
	}

	// 4. Mode normalization: replace/clear booleans -> mode="replace"
	if verb == "fill" || verb == "type" {
		if rep, ok := act["replace"].(bool); ok {
			if rep {
				act["mode"] = "replace"
				act["clear"] = true
			} else if _, hasMode := act["mode"]; !hasMode {
				act["mode"] = "append"
			}
			delete(act, "replace")
			notes = append(notes, "replace→mode")
		}
		if clr, ok := act["clear"].(bool); ok && clr {
			if _, hasMode := act["mode"]; !hasMode {
				act["mode"] = "replace"
			}
		}
	}

	// 5. Wait-state aliases
	if u, ok := act["until"].(string); ok && strings.TrimSpace(u) != "" {
		norm := normalizeWaitUntil(u)
		if norm != u {
			act["until"] = norm
			notes = append(notes, "until:"+u+"→"+norm)
		}
	}

	// 6. Repair nested fields inside fill_fields (never touching sensitive values)
	if verb == "fill_fields" {
		if items, ok := act["fields"].([]any); ok {
			outItems := make([]any, 0, len(items))
			for _, it := range items {
				fm, ok := it.(map[string]any)
				if !ok {
					outItems = append(outItems, it)
					continue
				}
				rf := make(map[string]any, len(fm))
				for k, v := range fm {
					rf[k] = v
				}
				for _, alias := range []string{"target", "ref", "element", "locator"} {
					if v, ok := rf[alias]; ok {
						if _, exists := rf["selector"]; !exists {
							rf["selector"] = v
						}
						delete(rf, alias)
					}
				}
				if rawSel, ok := rf["selector"].(string); ok {
					rf["selector"] = repairTargetSelector(rawSel)
				}
				if v, ok := rf["text"]; ok {
					if _, exists := rf["value"]; !exists {
						rf["value"] = fmt.Sprint(v)
					}
					delete(rf, "text")
				}
				if rep, ok := rf["replace"].(bool); ok && rep {
					rf["mode"] = "replace"
					delete(rf, "replace")
				}
				outItems = append(outItems, rf)
			}
			act["fields"] = outItems
		}
	}

	return act, notes
}

// repairTargetSelector normalizes pasted snapshot lines, `css ...`, and `role ...`
// shorthands into clean refs or Playwright selectors while preserving `>> nth=N`.
func repairTargetSelector(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return s
	}

	// Preserve trailing Playwright `>> nth=N` combinator if present.
	nthSuffix := ""
	if idx := strings.Index(s, ">>"); idx > 0 {
		tail := strings.TrimSpace(s[idx:])
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(strings.TrimPrefix(tail, ">>"))), "nth=") {
			nthSuffix = " " + tail
			s = strings.TrimSpace(s[:idx])
		}
	}

	// 1. Pasted snapshot line containing a revisioned ref (`3:main:12 button "Sign in"`).
	if m := snapshotRefLineRe.FindStringSubmatch(s); m != nil {
		return m[1]
	}

	// 2. OpenBot `css <selector>` shorthand.
	if m := cssPrefixRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1]) + nthSuffix
	}

	// 3. OpenBot `role <role> "<name>"` shorthand -> Playwright `role=<role>[name="<name>"]`.
	if m := rolePrefixRe.FindStringSubmatch(s); m != nil {
		role := strings.ToLower(m[1])
		name := m[2]
		if name == "" {
			name = m[3]
		}
		if name == "" {
			name = strings.TrimSpace(m[4])
		}
		if name != "" {
			escaped := strings.ReplaceAll(name, `"`, `\"`)
			return fmt.Sprintf(`role=%s[name="%s"]%s`, role, escaped, nthSuffix)
		}
		return fmt.Sprintf(`role=%s%s`, role, nthSuffix)
	}

	return s + nthSuffix
}

func normalizeWaitUntil(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "dom-quiet", "dom_quiet", "domquiet", "networkidle", "network-idle", "network_idle", "idle", "quiet", "settle":
		return "dom-quiet"
	case "load", "complete", "domcontentloaded", "dom-content-loaded", "ready":
		return "load"
	case "none", "fast", "immediate":
		return "none"
	default:
		return "dom-quiet"
	}
}
