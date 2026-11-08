package agent

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type textCall struct {
	start, end int
	name, args string
}

const (
	bar = `[｜|]`
	sp  = `[▁_ ]`
)

var (
	tagCallRe      = regexp.MustCompile(`(?s)<(tool_call|toolcall|tool_calls|function_call|function_calls|tool_use)>\s*(.*?)\s*</(?:tool_call|toolcall|tool_calls|function_call|function_calls|tool_use)>`)
	tagOpenTailRe  = regexp.MustCompile(`(?s)<(tool_call|toolcall|function_call|tool_use)>\s*(.+)$`)
	functionTagRe  = regexp.MustCompile(`(?s)<function=([A-Za-z0-9_.\-]+)>\s*(.*?)\s*(?:</function>|$)`)
	invokeRe       = regexp.MustCompile(`(?s)<invoke\s+name\s*=\s*"([^"]+)"[^>]*>\s*(.*?)\s*(?:</invoke>|$)`)
	paramEqRe      = regexp.MustCompile(`(?s)<parameter=([A-Za-z0-9_.\-]+)>\s*(.*?)\s*</parameter>`)
	paramNameRe    = regexp.MustCompile(`(?s)<parameter\s+name\s*=\s*"([^"]+)"([^>]*)>(.*?)</parameter>`)
	dsmlRe         = regexp.MustCompile(`<(\s*/)?\s*[｜|]{1,3}\s*DSML\s*[｜|]{1,3}\s*`)
	argPairRe      = regexp.MustCompile(`(?s)<arg_key>\s*(.*?)\s*</arg_key>\s*<arg_value>(.*?)</arg_value>`)
	mistralRe      = regexp.MustCompile(`\[TOOL_CALLS\]\s*(?:([A-Za-z0-9_.\-]+)\s*\[ARGS\])?\s*`)
	harmonyRe      = regexp.MustCompile(`(?s)(?:<\|start\|>\s*assistant\s*)?(?:<\|channel\|>\s*\w+\s*)?to=functions\.([A-Za-z0-9_.\-]+)[^<]*(?:<\|constrain\|>\s*\w*\s*)?<\|message\|>(.*?)(?:<\|call\|>|$)`)
	fenceRe        = regexp.MustCompile("(?s)```[ \\t]*([A-Za-z_]*)[ \\t]*\\n(.*?)```")
	deepseekCallRe = regexp.MustCompile(`(?s)<` + bar + `\s*tool` + sp + `call` + sp + `begin\s*` + bar + `>(.*?)(?:<` + bar + `\s*tool` + sp + `call` + sp + `end\s*` + bar + `>|$)`)
	deepseekSepRe  = regexp.MustCompile(`<` + bar + `\s*tool` + sp + `(?:sep|call` + sp + `argument` + sp + `begin)\s*` + bar + `>`)
	deepseekWrapRe = regexp.MustCompile(`<` + bar + `\s*tool` + sp + `(?:calls|outputs?)(?:` + sp + `section)?` + sp + `(?:begin|end)\s*` + bar + `>`)
	reactRe        = regexp.MustCompile(`(?s)(?:^|\n)[ \t]*Action:[ \t]*([A-Za-z0-9_.\-]+)[ \t]*\n[ \t]*Action Input:[ \t]*(.*?)(?:\n[ \t]*(?:Observation|Thought|Final Answer):|\z)`)
	wrapperTagRe   = regexp.MustCompile(`</?(?:function_calls|tool_calls)>`)
	nsTagRe        = regexp.MustCompile(`<(\s*/?\s*)[A-Za-z0-9_-]{1,20}:((?:tool_call|tool_calls|toolcall|function_calls|function_call|invoke|parameter|tool_use|arg_key|arg_value)\b)`)
	prefixTagRe    = regexp.MustCompile(`<(\s*/?\s*)[a-z0-9]{2,20}_(tool_calls?)>`)

	pythonTagRe   = regexp.MustCompile(`(?:<\|(?:python_tag|tool_call|tool_calls|plugin)\|>|\bfunctools)\s*`)
	pyCallStartRe = regexp.MustCompile(`(?m)^[ \t>*•-]*\[?\s*([A-Za-z_][A-Za-z0-9_]*)\(`)
	blankLinesRe  = regexp.MustCompile(`\n{3,}`)
)

func parseTextToolCalls(content string, known map[string]bool) ([]llm.ToolCall, string) {
	return parseTextCalls(content, known, false)
}

func parseLooseToolCalls(content string, known map[string]bool) ([]llm.ToolCall, string) {
	return parseTextCalls(content, known, true)
}

func parseTextCalls(content string, known map[string]bool, loose bool) ([]llm.ToolCall, string) {
	if strings.TrimSpace(content) == "" {
		return nil, content
	}
	content = dsmlRe.ReplaceAllString(content, "<$1")
	content = nsTagRe.ReplaceAllString(content, "<$1$2")
	content = prefixTagRe.ReplaceAllString(content, "<$1$2>")
	var found []textCall
	taken := func(s, e int) bool {
		for _, f := range found {
			if s < f.end && e > f.start {
				return true
			}
		}
		return false
	}
	add := func(s, e int, calls []nameArgs) {
		if taken(s, e) || len(calls) == 0 {
			return
		}
		for i, c := range calls {
			tc := textCall{start: s, end: e, name: c.name, args: c.args}
			if i > 0 {
				tc.start, tc.end = e, e
			}
			found = append(found, tc)
		}
	}
	knows := func(name string) bool {
		if known[name] || !loose {
			return known[name]
		}
		_, ok := resolveName(name, known)
		return ok
	}
	isKnown := func(calls []nameArgs) bool {
		for _, c := range calls {
			if !knows(c.name) {
				return false
			}
		}
		return len(calls) > 0
	}

	for _, m := range deepseekCallRe.FindAllStringSubmatchIndex(content, -1) {
		if c, ok := parseDeepSeekCall(content[m[2]:m[3]]); ok {
			add(m[0], m[1], []nameArgs{c})
		}
	}

	for _, m := range tagCallRe.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], callsFromBody(content[m[4]:m[5]]))
	}

	for _, m := range functionTagRe.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], []nameArgs{{name: content[m[2]:m[3]], args: argsFromBody(content[m[4]:m[5]], paramEqRe)}})
	}

	for _, m := range invokeRe.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], []nameArgs{{name: content[m[2]:m[3]], args: argsFromBody(content[m[4]:m[5]], paramNameRe)}})
	}

	for _, m := range harmonyRe.FindAllStringSubmatchIndex(content, -1) {
		add(m[0], m[1], []nameArgs{{name: content[m[2]:m[3]], args: normalizeArgs(content[m[4]:m[5]])}})
	}

	for _, m := range mistralRe.FindAllStringSubmatchIndex(content, -1) {
		dec := json.NewDecoder(strings.NewReader(content[m[1]:]))
		var v any
		if dec.Decode(&v) != nil {
			continue
		}
		end := m[1] + int(dec.InputOffset())
		if m[2] >= 0 {
			b, _ := json.Marshal(v)
			add(m[0], end, []nameArgs{{name: content[m[2]:m[3]], args: string(b)}})
		} else {
			add(m[0], end, callsFromValue(v))
		}
	}

	for _, m := range pythonTagRe.FindAllStringIndex(content, -1) {
		dec := json.NewDecoder(strings.NewReader(content[m[1]:]))
		var v any
		if dec.Decode(&v) != nil {
			continue
		}
		add(m[0], m[1]+int(dec.InputOffset()), callsFromValue(v))
	}

	for _, g := range pythonicCalls(content, knows) {
		add(g.start, g.end, g.calls)
	}

	if m := tagOpenTailRe.FindStringSubmatchIndex(content); m != nil && !taken(m[0], m[1]) {
		add(m[0], m[1], callsFromBody(content[m[4]:m[5]]))
	}

	for _, m := range fenceRe.FindAllStringSubmatchIndex(content, -1) {
		lang, body := strings.ToLower(content[m[2]:m[3]]), content[m[4]:m[5]]
		calls := callsFromBody(body)
		switch lang {
		case "tool_call", "tool_calls", "toolcall", "function_call", "tool_use", "tool":
			add(m[0], m[1], calls)
		case "json", "":
			if isKnown(calls) {
				add(m[0], m[1], calls)
			}
		}
	}

	for _, m := range reactRe.FindAllStringSubmatchIndex(content, -1) {
		c := nameArgs{name: content[m[2]:m[3]], args: normalizeArgs(strings.TrimSpace(content[m[4]:m[5]]))}
		if knows(c.name) {
			add(m[0], m[1], []nameArgs{c})
		}
	}

	if t := strings.TrimSpace(unfence(content)); len(found) == 0 && (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) {
		if calls := callsFromBody(content); isKnown(calls) {
			add(0, len(content), calls)
		}
	}
	if len(found) == 0 {
		return nil, content
	}

	sort.SliceStable(found, func(i, j int) bool { return found[i].start < found[j].start })
	var (
		calls []llm.ToolCall
		clean strings.Builder
		pos   int
	)
	for _, f := range found {
		calls = append(calls, llm.ToolCall{Type: "function", Function: llm.FunctionCall{Name: f.name, Arguments: f.args}})
		if f.start >= pos {
			clean.WriteString(content[pos:f.start])
			pos = f.end
		}
	}
	clean.WriteString(content[pos:])
	return calls, cleanText(clean.String())
}

func stripToolMarkup(content string, known map[string]bool) string {
	_, clean := parseTextToolCalls(content, known)
	return scrubMarkup(clean)
}

func cleanText(s string) string {
	s = deepseekWrapRe.ReplaceAllString(s, "")
	s = wrapperTagRe.ReplaceAllString(s, "")
	return scrubMarkup(s)
}

type nameArgs struct{ name, args string }

func parseDeepSeekCall(inner string) (nameArgs, bool) {
	parts := deepseekSepRe.Split(inner, -1)
	var name, rest string
	switch {
	case len(parts) >= 3:
		name, rest = parts[1], strings.Join(parts[2:], "")
	case len(parts) == 2:
		left := strings.TrimSpace(parts[0])
		if left == "" || left == "function" {
			name, rest, _ = strings.Cut(strings.TrimLeft(parts[1], " \t"), "\n")
		} else {
			name, rest = left, parts[1]
		}
	default:
		return nameArgs{}, false
	}
	name = kimiIndexRe.ReplaceAllString(strings.TrimPrefix(strings.TrimSpace(name), "functions."), "")
	if name == "" || !isIdent(name) {
		return nameArgs{}, false
	}
	return nameArgs{name: name, args: normalizeArgs(unfence(rest))}, true
}

var kimiIndexRe = regexp.MustCompile(`:\d+$`)

func callsFromBody(body string) []nameArgs {
	body = unfence(strings.TrimSpace(body))
	if body == "" {
		return nil
	}
	var v any
	if json.Unmarshal([]byte(body), &v) != nil {
		fixed, err := repairJSON(body)
		if err != nil || json.Unmarshal([]byte(fixed), &v) != nil {
			if i := strings.Index(body, "<arg_key>"); i > 0 && isIdent(strings.TrimSpace(body[:i])) {
				obj := map[string]any{}
				for _, p := range argPairRe.FindAllStringSubmatch(body[i:], -1) {
					obj[p[1]] = paramValue(p[2])
				}
				b, _ := json.Marshal(obj)
				return []nameArgs{{name: strings.TrimSpace(body[:i]), args: string(b)}}
			}
			if name, rest, ok := strings.Cut(body, "\n"); ok && isIdent(strings.TrimSpace(name)) {
				return []nameArgs{{name: strings.TrimSpace(name), args: normalizeArgs(rest)}}
			}

			if m := brokenNameRe.FindStringSubmatch(body); m != nil {
				args := "{}"
				if a := brokenArgsRe.FindStringSubmatch(body); a != nil {
					args = strings.TrimSpace(a[1])
					args = strings.TrimSpace(strings.TrimSuffix(args, "}"))
				}
				return []nameArgs{{name: m[1], args: args}}
			}
			return nil
		}
	}
	return callsFromValue(v)
}

var (
	brokenNameRe = regexp.MustCompile(`"(?:name|tool|tool_name|function)"\s*:\s*"([A-Za-z0-9_.\-]+)"`)
	brokenArgsRe = regexp.MustCompile(`(?s)"(?:arguments|parameters|args|input|params)"\s*:\s*(.*)$`)
)

func callsFromValue(v any) []nameArgs {
	switch x := v.(type) {
	case []any:
		var out []nameArgs
		for _, e := range x {
			out = append(out, callsFromValue(e)...)
		}
		return out
	case map[string]any:
		for _, k := range []string{"tool_calls", "calls", "function_calls"} {
			if list, ok := x[k].([]any); ok {
				return callsFromValue(list)
			}
		}
		if c, ok := callFromObject(x); ok {
			return []nameArgs{c}
		}
	}
	return nil
}

func callFromObject(m map[string]any) (nameArgs, bool) {
	if fn, ok := m["function"].(map[string]any); ok {
		return callFromObject(fn)
	}
	var name string
	for _, k := range []string{"name", "tool", "tool_name", "function", "action", "recipient_name"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			name = strings.TrimPrefix(strings.TrimSpace(s), "functions.")
			break
		}
	}
	if !isIdent(name) {
		return nameArgs{}, false
	}
	for _, k := range []string{"arguments", "parameters", "args", "input", "params", "action_input", "tool_input"} {
		if a, ok := m[k]; ok {
			return nameArgs{name: name, args: argsToJSON(a)}, true
		}
	}
	return nameArgs{name: name, args: "{}"}, true
}

func argsFromBody(body string, paramRe *regexp.Regexp) string {
	body = strings.TrimSpace(body)
	if ps := paramRe.FindAllStringSubmatch(body, -1); len(ps) > 0 {
		obj := map[string]any{}
		for _, p := range ps {
			if len(p) == 4 {
				if strings.Contains(strings.ReplaceAll(p[2], " ", ""), `string="true"`) {
					obj[p[1]] = strings.Trim(p[3], "\n")
				} else {
					obj[p[1]] = paramValue(p[3])
				}
				continue
			}
			obj[p[1]] = paramValue(p[2])
		}
		b, _ := json.Marshal(obj)
		return string(b)
	}
	return normalizeArgs(unfence(body))
}

func paramValue(raw string) any {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if c := s[0]; c == '{' || c == '[' || c == '-' || (c >= '0' && c <= '9') || s == "true" || s == "false" || s == "null" {
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			return v
		}
	}
	return s
}

func argsToJSON(a any) string {
	switch x := a.(type) {
	case nil:
		return "{}"
	case string:
		return normalizeArgs(x)
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return "{}"
		}
		return string(b)
	}
}

func normalizeArgs(s string) string {
	s = strings.TrimSpace(unfence(strings.TrimSpace(s)))
	if s == "" {
		return "{}"
	}
	return s
}

func unfence(s string) string {
	t := strings.TrimSpace(s)
	if !strings.HasPrefix(t, "```") {
		return s
	}
	t = strings.TrimPrefix(t, "```")
	if i := strings.IndexByte(t, '\n'); i >= 0 && !strings.ContainsAny(t[:i], "{[") {
		t = t[i+1:]
	}
	return strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(t), "```"))
}

func isIdent(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, r := range s {
		if !(r == '_' || r == '-' || r == '.' || r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

var callMarkers = []string{"<tool_call", "<toolcall", "<tool_calls", "<function_call", "<function=", "<tool_use", "<invoke",
	"<｜tool", "<|tool", "```tool", "```function_call", "\nAction:", "Action Input:", "<｜DSML", "<|DSML", "<｜｜", "</｜", "<parameter",
	"<arg_key", "[TOOL_CALLS]", "<|start|>", "<|channel|>", "<|message|>", "<|im_", "<|eot", "<|endof", "<｜end", "<｜begin", "to=functions."}

var (
	streamCutRes = []*regexp.Regexp{
		regexp.MustCompile(`<\s*/?\s*[A-Za-z0-9_-]{1,20}:(?:tool_call|tool_calls|toolcall|invoke|parameter|function_calls?|tool_use)`),
		regexp.MustCompile("(?m)^[ \\t]*(?:```[A-Za-z_]*[ \\t]*\\n[ \\t]*)?\\[?\\s*\\{\\s*\"(?:name|function|tool|tool_name)\"\\s*:\\s*" +
			"(?:\"[A-Za-z][A-Za-z0-9]*_|\"[^\"\\n]+\"\\s*,\\s*\"(?:arguments|parameters|args|input)\")"),
		regexp.MustCompile(`(?m)^[ \t]*Action:\s`),
		regexp.MustCompile(`<[|｜]`),
		regexp.MustCompile(`\bfunctools\s*\[`),
		regexp.MustCompile(`<[a-z0-9]{2,20}_tool_calls?>`),
	}

	streamPyRe = regexp.MustCompile(`(?m)^[ \t>*•-]*\[?\s*[a-z][a-z0-9]*_[a-z0-9_]*\(`)

	streamHoldRe  = regexp.MustCompile("^[ \\t]*(?:\\{|\\[\\s*(?:\\{|[a-z_]*$|[a-z][a-z0-9]*_)|<|Action|functools|[a-z][a-z0-9]*_[a-z0-9_]*(?:\\(.*)?$)")
	fenceOpenRe   = regexp.MustCompile("^[ \\t]*```[A-Za-z_]*[ \\t]*$")
	trailingTagRe = regexp.MustCompile(`<[A-Za-z0-9_:/|｜-]*$`)
)

func visibleStream(s string) string {
	s = streamThought(s)
	cut := len(s)
	for _, m := range callMarkers {
		if i := strings.Index(s, m); i >= 0 && i < cut {
			cut = i
		}
	}
	for _, re := range streamCutRes {
		if loc := re.FindStringIndex(s); loc != nil && loc[0] < cut {
			cut = loc[0]
		}
	}
	for _, loc := range streamPyRe.FindAllStringIndex(s, -1) {
		if loc[0] < cut && strings.Count(s[:loc[0]], "```")%2 == 0 {
			cut = loc[0]
			break
		}
	}
	if cut < len(s) {
		s = strings.TrimRight(s[:cut], " \t")
	}

	if i := strings.LastIndexByte(s, '\n'); streamHoldRe.MatchString(s[i+1:]) && (s[i+1:] == "" || !strings.Contains(s[:i+1], "```") ||
		strings.Count(s[:i+1], "```")%2 == 0 || strings.HasPrefix(strings.TrimSpace(s[i+1:]), "{")) {
		s = strings.TrimRight(s[:i+1], " \t")

		if j := strings.LastIndexByte(s, '\n'); fenceOpenRe.MatchString(s[j+1:]) && strings.Count(s, "```")%2 == 1 {
			s = strings.TrimRight(s[:j+1], " \t")
		}
	}
	if loc := trailingTagRe.FindStringIndex(s); loc != nil {
		s = strings.TrimRight(s[:loc[0]], " \t")
	}

	for _, m := range callMarkers {
		if m[0] != '<' {
			continue
		}
		for n := min(len(m)-1, len(s)); n >= 2; n-- {
			if strings.HasSuffix(s, m[:n]) {
				return strings.TrimRight(s[:len(s)-n], " \t")
			}
		}
	}
	return s
}

var (
	dsmlTagRe      = regexp.MustCompile(`<\s*/?\s*[｜|]{1,3}\s*DSML\s*[｜|]{1,3}[^<>\n]{0,200}>`)
	specialTokenRe = regexp.MustCompile(`<[｜|][A-Za-z][A-Za-z0-9_▁.:\-]{0,40}[｜|]>`)
	bracketTokenRe = regexp.MustCompile(`\[/?(?:TOOL_CALLS|TOOL_RESULTS|ARGS|CALL_ID|AVAILABLE_TOOLS)\]`)
	orphanTagRe    = regexp.MustCompile(`</?(?:[A-Za-z0-9_-]{1,20}:)?(?:tool_call|tool_calls|toolcall|function_calls|function_call|tool_use|tool_response|tool_result|invoke|parameter|arg_key|arg_value)\b[^<>\n]{0,200}>`)
	thinkBlockRe   = regexp.MustCompile(`(?s)^\s*<(think|thinking|reasoning)>.*?</(?:think|thinking|reasoning)>\s*`)
	thinkCloseRe   = regexp.MustCompile(`</(?:think|thinking|reasoning)>`)
)

func namedCalls(calls []llm.ToolCall) ([]llm.ToolCall, int) {
	out := calls[:0:0]
	dropped := 0
	for _, c := range calls {
		if strings.TrimSpace(c.Function.Name) == "" {
			dropped++
			continue
		}
		out = append(out, c)
	}
	if dropped == 0 {
		return calls, 0
	}
	return out, dropped
}

var imitatedCallRe = regexp.MustCompile(`(?m)^[ \t>*•-]*([A-Za-z][A-Za-z0-9_]{4,})\(`)

func imitatedCalls(text string, known map[string]bool) []string {
	var names []string
	seen := map[string]bool{}
	outsideCode(text, func(t string) string {
		for _, m := range imitatedCallRe.FindAllStringSubmatch(t, -1) {
			if known[m[1]] && m[1] != toolUseTools && !seen[m[1]] {
				seen[m[1]] = true
				names = append(names, m[1])
			}
		}
		return t
	})
	return names
}

func dropImitatedCalls(text string, known map[string]bool) string {
	if len(imitatedCalls(text, known)) == 0 {
		return text
	}
	return strings.TrimSpace(outsideCode(text, func(t string) string {
		lines := strings.Split(t, "\n")
		out := lines[:0]
		for _, l := range lines {
			if m := imitatedCallRe.FindStringSubmatch(l); m != nil && known[m[1]] {
				continue
			}
			out = append(out, l)
		}
		return strings.Join(out, "\n")
	}))
}

func scrubMarkup(s string) string {
	s = dropThought(s)
	s = outsideCode(s, func(t string) string {
		t = dsmlTagRe.ReplaceAllString(t, "")
		t = specialTokenRe.ReplaceAllString(t, "")
		t = bracketTokenRe.ReplaceAllString(t, "")
		return orphanTagRe.ReplaceAllString(t, "")
	})
	s = blankLinesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

func dropThought(s string) string {
	s = thinkBlockRe.ReplaceAllString(s, "")
	if loc := thinkCloseRe.FindStringIndex(s); loc != nil && !strings.Contains(s[:loc[0]], "```") && !strings.Contains(s[:loc[0]], "<think") {
		s = s[loc[1]:]
	}
	return s
}

func streamThought(s string) string {
	t := strings.TrimLeft(s, " \t\n")
	for _, open := range []string{"<think>", "<thinking>", "<reasoning>"} {
		if strings.HasPrefix(t, open) {
			if loc := thinkCloseRe.FindStringIndex(t); loc != nil {
				return strings.TrimLeft(t[loc[1]:], " \t\n")
			}
			return ""
		}
		if strings.HasPrefix(open, t) && t != "" {
			return ""
		}
	}
	return s
}

func outsideCode(s string, f func(string) string) string {
	var sb strings.Builder
	for i, block := range strings.Split(s, "```") {
		if i > 0 {
			sb.WriteString("```")
		}
		if i%2 == 1 {
			sb.WriteString(block)
			continue
		}
		for j, part := range strings.Split(block, "`") {
			if j > 0 {
				sb.WriteString("`")
			}
			if j%2 == 1 {
				sb.WriteString(part)
			} else {
				sb.WriteString(f(part))
			}
		}
	}
	return sb.String()
}

type callGroup struct {
	start, end int
	calls      []nameArgs
}

func pythonicCalls(content string, knows func(string) bool) []callGroup {
	var out []callGroup
	for _, m := range pyCallStartRe.FindAllStringSubmatchIndex(content, -1) {
		start := m[0]
		for start < len(content) && strings.ContainsRune(" \t>*•-", rune(content[start])) {
			start++
		}
		pos := m[2]
		var calls []nameArgs
		for {
			name, end, args, ok := pythonicCall(content, pos)
			if !ok || !knows(name) {
				calls = nil
				break
			}
			calls = append(calls, nameArgs{name: name, args: args})
			pos = end
			for pos < len(content) && (content[pos] == ' ' || content[pos] == '\t') {
				pos++
			}
			if pos < len(content) && content[pos] == ',' {
				pos++
				for pos < len(content) && (content[pos] == ' ' || content[pos] == '\t') {
					pos++
				}
				continue
			}
			break
		}
		if len(calls) == 0 {
			continue
		}
		end := pos
		if end < len(content) && content[end] == ']' {
			end++
		}

		if rest, _, _ := strings.Cut(content[end:], "\n"); strings.TrimSpace(rest) != "" {
			continue
		}
		out = append(out, callGroup{start: start, end: end, calls: calls})
	}
	return out
}

func pythonicCall(s string, pos int) (name string, end int, args string, ok bool) {
	i := pos
	for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9') {
		i++
	}
	name = s[pos:i]
	if name == "" || i >= len(s) || s[i] != '(' {
		return "", 0, "", false
	}
	i++
	obj := map[string]any{}
	for {
		for i < len(s) && strings.ContainsRune(" \t\n", rune(s[i])) {
			i++
		}
		if i < len(s) && s[i] == ')' {
			b, _ := json.Marshal(obj)
			return name, i + 1, string(b), true
		}
		k := i
		for i < len(s) && (s[i] == '_' || s[i] >= 'a' && s[i] <= 'z' || s[i] >= 'A' && s[i] <= 'Z' || s[i] >= '0' && s[i] <= '9') {
			i++
		}
		key := s[k:i]
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if key == "" || i >= len(s) || s[i] != '=' {
			return "", 0, "", false
		}
		i++
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		v, n, ok := pyValue(s[i:])
		if !ok {
			return "", 0, "", false
		}
		obj[key] = v
		i += n
		for i < len(s) && (s[i] == ' ' || s[i] == '\t' || s[i] == '\n') {
			i++
		}
		if i < len(s) && s[i] == ',' {
			i++
			continue
		}
		if i < len(s) && s[i] == ')' {
			continue
		}
		return "", 0, "", false
	}
}

func pyValue(s string) (v any, n int, ok bool) {
	switch {
	case s == "":
		return nil, 0, false
	case strings.HasPrefix(s, `"""`) || strings.HasPrefix(s, "'''"):
		q := s[:3]
		if j := strings.Index(s[3:], q); j >= 0 {
			return s[3 : 3+j], 3 + j + 3, true
		}
		return nil, 0, false
	case s[0] == '"' || s[0] == '\'':
		q := s[0]
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) {
				i++
				switch s[i] {
				case 'n':
					b.WriteByte('\n')
				case 't':
					b.WriteByte('\t')
				default:
					b.WriteByte(s[i])
				}
				continue
			}
			if c == q {
				return b.String(), i + 1, true
			}
			b.WriteByte(c)
		}
		return nil, 0, false
	case s[0] == '[' || s[0] == '{':
		dec := json.NewDecoder(strings.NewReader(s))
		var x any
		if dec.Decode(&x) == nil {
			return x, int(dec.InputOffset()), true
		}
		return nil, 0, false
	}
	j := 0
	for j < len(s) && (s[j] == '_' || s[j] == '-' || s[j] == '+' || s[j] == '.' || s[j] >= '0' && s[j] <= '9' || s[j] >= 'a' && s[j] <= 'z' || s[j] >= 'A' && s[j] <= 'Z') {
		j++
	}
	switch w := s[:j]; w {
	case "True", "true":
		return true, j, true
	case "False", "false":
		return false, j, true
	case "None", "null":
		return nil, j, true
	case "":
		return nil, 0, false
	default:
		var f float64
		if err := json.Unmarshal([]byte(w), &f); err == nil {
			return f, j, true
		}
	}
	return nil, 0, false
}
