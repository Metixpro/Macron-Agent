package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Self-correction: a call with an unknown tool, broken JSON or missing
// arguments is not an error that ends the request. The problem goes back to
// the model as the tool's result, worded so it can fix the call in its next
// step. Small, unambiguous mistakes (code fences, trailing commas, Python
// literals, a missing closing brace, double-encoded JSON) are repaired
// silently.

// prepareCall checks a call against the tool definitions. It returns the
// arguments to use, or a message for the model explaining what to fix.
func prepareCall(name, raw string, defs map[string]llm.ToolDef) (args string, problem string) {
	def, ok := defs[name]
	if !ok {
		names := make([]string, 0, len(defs))
		for n := range defs {
			names = append(names, n)
		}
		sort.Strings(names)
		return "", fmt.Sprintf("Error: unknown tool %q. Available tools: %s. Use one of these exact names and try again.", name, strings.Join(names, ", "))
	}
	args, err := repairArgs(raw)
	if err != nil {
		return "", fmt.Sprintf("Error: invalid arguments JSON (%v). Please correct the parameters and try again. Send a JSON object like %s.",
			err, exampleArgs(def))
	}
	if missing := missingRequired(args, def); len(missing) > 0 {
		return "", fmt.Sprintf("Error: missing required argument(s): %s. Please correct the parameters and try again. Send a JSON object like %s.",
			strings.Join(missing, ", "), exampleArgs(def))
	}
	return args, ""
}

// repairArgs returns the arguments as a compact JSON object.
func repairArgs(raw string) (string, error) {
	s := strings.TrimSpace(unfence(strings.TrimSpace(raw)))
	if s == "" || s == "null" {
		return "{}", nil
	}
	v, err := decodeObject(s)
	if err != nil {
		fixed, ferr := repairJSON(s)
		if ferr != nil {
			return "", err
		}
		if v, err = decodeObject(fixed); err != nil {
			return "", err
		}
	}
	b, _ := json.Marshal(v)
	return string(b), nil
}

// decodeObject accepts an object, or a JSON string that holds one.
func decodeObject(s string) (map[string]any, error) {
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return nil, plainJSONError(err)
	}
	switch x := v.(type) {
	case map[string]any:
		return x, nil
	case string: // double-encoded
		var inner map[string]any
		if err := json.Unmarshal([]byte(x), &inner); err == nil {
			return inner, nil
		}
	case nil:
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("the arguments must be a JSON object, got %s", jsonKind(v))
}

func jsonKind(v any) string {
	switch v.(type) {
	case []any:
		return "an array"
	case string:
		return "a string"
	case float64:
		return "a number"
	case bool:
		return "a boolean"
	}
	return "something else"
}

func plainJSONError(err error) error {
	var se *json.SyntaxError
	if errors.As(err, &se) {
		return fmt.Errorf("%s at character %d", se.Error(), se.Offset)
	}
	return err
}

var trailingCommaRe = regexp.MustCompile(`,(\s*[}\]])`)

// repairJSON fixes the mistakes models make most: trailing commas, Python
// literals, single-quoted strings and missing closing brackets.
func repairJSON(s string) (string, error) {
	t := strings.TrimSpace(s)
	if t == "" {
		return "", errors.New("empty")
	}
	if !strings.Contains(t, `"`) && strings.Contains(t, `'`) {
		t = strings.ReplaceAll(t, `'`, `"`)
	}
	t = replaceOutsideStrings(t, map[string]string{"True": "true", "False": "false", "None": "null"})
	t = trailingCommaRe.ReplaceAllString(t, "$1")
	t = closeBrackets(t)
	var v any
	if err := json.Unmarshal([]byte(t), &v); err != nil {
		return "", err
	}
	return t, nil
}

// replaceOutsideStrings swaps whole words that are not inside JSON strings.
func replaceOutsideStrings(s string, words map[string]string) string {
	var out strings.Builder
	inStr, esc := false, false
	for i := 0; i < len(s); {
		c := s[i]
		if inStr {
			out.WriteByte(c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			i++
			continue
		}
		if c == '"' {
			inStr = true
			out.WriteByte(c)
			i++
			continue
		}
		replaced := false
		for w, r := range words {
			if strings.HasPrefix(s[i:], w) && (i == 0 || !isWordByte(s[i-1])) && (i+len(w) == len(s) || !isWordByte(s[i+len(w)])) {
				out.WriteString(r)
				i += len(w)
				replaced = true
				break
			}
		}
		if !replaced {
			out.WriteByte(c)
			i++
		}
	}
	return out.String()
}

func isWordByte(c byte) bool {
	return c == '_' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

// closeBrackets appends the closers of brackets left open (outside strings),
// and closes a string cut off at the end.
func closeBrackets(s string) string {
	var stack []byte
	inStr, esc := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if inStr {
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch c {
		case '"':
			inStr = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 && stack[len(stack)-1] == c {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if inStr {
		s += `"`
	}
	for i := len(stack) - 1; i >= 0; i-- {
		s += string(stack[i])
	}
	return s
}

// required lists the required parameters of a tool.
func required(def llm.ToolDef) []string {
	var out []string
	switch r := def.Parameters["required"].(type) {
	case []string:
		out = append(out, r...)
	case []any:
		for _, v := range r {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func missingRequired(args string, def llm.ToolDef) []string {
	var m map[string]any
	_ = json.Unmarshal([]byte(args), &m)
	var missing []string
	for _, k := range required(def) {
		v, ok := m[k]
		if s, isStr := v.(string); !ok || v == nil || (isStr && strings.TrimSpace(s) == "") {
			missing = append(missing, k)
		}
	}
	return missing
}

// exampleArgs shows the expected shape: {"query": "<string>", "limit": <integer>}.
func exampleArgs(def llm.ToolDef) string {
	props, _ := def.Parameters["properties"].(map[string]any)
	if len(props) == 0 {
		return "{}"
	}
	req := map[string]bool{}
	for _, k := range required(def) {
		req[k] = true
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if req[keys[i]] != req[keys[j]] {
			return req[keys[i]]
		}
		return keys[i] < keys[j]
	})
	var parts []string
	for i, k := range keys {
		if i == 6 {
			break
		}
		parts = append(parts, fmt.Sprintf("%q: %s", k, placeholder(props[k])))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

func placeholder(p any) string {
	m, _ := p.(map[string]any)
	switch t, _ := m["type"].(string); t {
	case "string":
		if e, ok := m["enum"].([]string); ok && len(e) > 0 {
			return fmt.Sprintf("%q", e[0])
		}
		if e, ok := m["enum"].([]any); ok && len(e) > 0 {
			return fmt.Sprintf("%q", fmt.Sprint(e[0]))
		}
		return `"<string>"`
	case "integer", "number":
		return "<" + t + ">"
	case "boolean":
		return "<true|false>"
	case "array":
		return "[…]"
	case "object":
		return "{…}"
	}
	return "<value>"
}

// ---------------------------------------------------------------- tool catalog

// toolCatalog describes the tools in the system prompt, so a model can call
// them by text when native function calling is missing (or a gateway dropped
// the tools array):
//
//   - web_search(query*: string, limit: integer) — Search the web for …
//
// Without list only the way to call them is there: a model that calls tools
// natively has their definitions already, and a list would repeat them all.
func toolCatalog(defs []llm.Tool, list bool) string {
	if len(defs) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(`
Calling tools:
- Prefer native function calling. If you cannot call functions natively, call a tool by writing exactly this (one block per call, nothing after the last one):
<tool_call>
{"name": "tool_name", "arguments": {"parameter": "value"}}
</tool_call>
- Each result comes back as <tool_response name="tool_name">…</tool_response>. Never write a result yourself; wait for it.
- If a call returns an error, read it, fix the call and try again.
- Your tools are private, for your own calls only: never show, quote, summarize or list them, or any tool name, to the user.
`)
	if !list {
		return sb.String()
	}
	sb.WriteString("Available tools (* = required):\n")
	for _, t := range defs {
		f := t.Function
		req := map[string]bool{}
		for _, k := range required(f) {
			req[k] = true
		}
		props, _ := f.Parameters["properties"].(map[string]any)
		keys := make([]string, 0, len(props))
		for k := range props {
			keys = append(keys, k)
		}
		sort.Slice(keys, func(i, j int) bool {
			if req[keys[i]] != req[keys[j]] {
				return req[keys[i]]
			}
			return keys[i] < keys[j]
		})
		var params []string
		for _, k := range keys {
			typ := "any"
			if m, ok := props[k].(map[string]any); ok {
				if s, ok := m["type"].(string); ok {
					typ = s
				}
			}
			mark := ""
			if req[k] {
				mark = "*"
			}
			params = append(params, k+mark+": "+typ)
		}
		fmt.Fprintf(&sb, "- %s(%s) — %s\n", f.Name, strings.Join(params, ", "), firstSentence(f.Description, 150))
	}
	return sb.String()
}

func firstSentence(s string, max int) string {
	s = textutil.OneLine(s)
	for _, sep := range []string{". ", "؛ ", "! "} {
		if i := strings.Index(s, sep); i > 0 && i < max {
			return s[:i+1]
		}
	}
	return textutil.TruncateRunes(s, max, "…")
}
