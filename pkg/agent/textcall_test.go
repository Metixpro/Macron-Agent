package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

var knownTools = map[string]bool{"web_search": true, "read_file": true, "list_files": true}

func argsOf(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("arguments %q are not JSON: %v", s, err)
	}
	return m
}

func TestParseTextToolCalls(t *testing.T) {
	cases := []struct {
		name, content, clean string
		calls                []string // name:query
	}{
		{"hermes", "باشه، می‌گردم.\n<tool_call>\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"قیمت طلا\"}}\n</tool_call>",
			"باشه، می‌گردم.", []string{"web_search:قیمت طلا"}},
		{"hermes, arguments as a string", `<tool_call>{"name": "web_search", "arguments": "{\"query\": \"go\"}"}</tool_call>`, "", []string{"web_search:go"}},
		{"two calls", "<tool_call>{\"name\":\"web_search\",\"arguments\":{\"query\":\"a\"}}</tool_call>\n<tool_call>{\"name\":\"web_search\",\"arguments\":{\"query\":\"b\"}}</tool_call>",
			"", []string{"web_search:a", "web_search:b"}},
		{"left open at the end", "حتماً\n<tool_call>\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}", "حتماً", []string{"web_search:x"}},
		{"function tag with JSON", "<function=web_search>{\"query\": \"go 1.26\"}</function>", "", []string{"web_search:go 1.26"}},
		{"function tag with parameters", "<function=web_search>\n<parameter=query>\nnews\n</parameter>\n<parameter=limit>3</parameter>\n</function>", "", []string{"web_search:news"}},
		{"invoke", "<function_calls><invoke name=\"web_search\"><parameter name=\"query\">برف</parameter></invoke></function_calls>", "", []string{"web_search:برف"}},
		{"fenced tool_call", "```tool_call\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"q\"}}\n```", "", []string{"web_search:q"}},
		{"fenced json, known tool", "اول جستجو:\n```json\n{\"tool\": \"web_search\", \"parameters\": {\"query\": \"q2\"}}\n```", "اول جستجو:", []string{"web_search:q2"}},
		{"openai shape", `<tool_call>{"type":"function","function":{"name":"web_search","arguments":"{\"query\":\"o\"}"}}</tool_call>`, "", []string{"web_search:o"}},
		{"deepseek v3", "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>function<｜tool▁sep｜>web_search\n```json\n{\"query\": \"ds\"}\n```<｜tool▁call▁end｜><｜tool▁calls▁end｜>",
			"", []string{"web_search:ds"}},
		{"deepseek v3.1", "یه لحظه<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>web_search<｜tool▁sep｜>{\"query\": \"v31\"}<｜tool▁call▁end｜><｜tool▁calls▁end｜>",
			"یه لحظه", []string{"web_search:v31"}},
		{"deepseek with plain bars and spaces", "<|tool calls begin|><|tool call begin|>web_search<|tool sep|>{\"query\": \"sp\"}<|tool call end|><|tool calls end|>",
			"", []string{"web_search:sp"}},
		{"react", "Thought: I should search.\nAction: web_search\nAction Input: {\"query\": \"react\"}", "Thought: I should search.", []string{"web_search:react"}},
		{"bare json reply", `{"name": "web_search", "arguments": {"query": "bare"}}`, "", []string{"web_search:bare"}},
		{"tool_calls array", `<tool_calls>[{"name":"web_search","arguments":{"query":"1"}},{"name":"list_files","arguments":{}}]</tool_calls>`, "", []string{"web_search:1", "list_files:"}},
		{"repaired JSON", "<tool_call>{'name': 'web_search', 'arguments': {'query': 'fix',}}</tool_call>", "", []string{"web_search:fix"}},
		// Not calls:
		{"ordinary json is an answer", "این یه نمونه‌ست:\n```json\n{\"name\": \"Ali\", \"age\": 30}\n```", "این یه نمونه‌ست:\n```json\n{\"name\": \"Ali\", \"age\": 30}\n```", nil},
		{"prose", "سلام! خوبم، تو چطوری؟", "سلام! خوبم، تو چطوری؟", nil},
		{"code mentioning a tool", "```python\nweb_search('x')\n```", "```python\nweb_search('x')\n```", nil},
		{"react with an unknown action", "Action: fly\nAction Input: {}", "Action: fly\nAction Input: {}", nil},
		{"prose quoting a call", `برای جستجو مدل می‌نویسه {"name": "web_search", "arguments": {"query": "x"}} و منتظر می‌مونه.`,
			`برای جستجو مدل می‌نویسه {"name": "web_search", "arguments": {"query": "x"}} و منتظر می‌مونه.`, nil},
	}
	for _, c := range cases {
		calls, clean := parseTextToolCalls(c.content, knownTools)
		if clean != c.clean {
			t.Errorf("%s: clean text\n got  %q\n want %q", c.name, clean, c.clean)
		}
		var got []string
		for _, tc := range calls {
			q, _ := argsOf(t, tc.Function.Arguments)["query"].(string)
			got = append(got, tc.Function.Name+":"+q)
		}
		if strings.Join(got, ",") != strings.Join(c.calls, ",") {
			t.Errorf("%s: calls %v, want %v", c.name, got, c.calls)
		}
	}
}

func TestExplicitMarkupWithAnUnknownToolIsStillACall(t *testing.T) {
	calls, clean := parseTextToolCalls(`<tool_call>{"name": "serach_web", "arguments": {"query": "x"}}</tool_call>`, knownTools)
	if len(calls) != 1 || calls[0].Function.Name != "serach_web" || clean != "" {
		t.Errorf("the model gets an 'unknown tool' error and can fix the name: %v %q", calls, clean)
	}
}

func TestVisibleStream(t *testing.T) {
	cases := map[string]string{
		"سلام دنیا": "سلام دنیا",
		"باشه، می‌گردم.\n<tool_call>\n{\"name\": \"we": "باشه، می‌گردم.\n",
		"دارم نگاه می‌کنم <tool_ca":                    "دارم نگاه می‌کنم",
		"یه لحظه<｜tool▁calls▁begin｜>":                  "یه لحظه",
		"x < 5 and y > 3":          "x < 5 and y > 3",
		"```python\nprint(1)\n```": "```python\nprint(1)\n```",
	}
	for in, want := range cases {
		if got := visibleStream(in); got != want {
			t.Errorf("visibleStream(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepairArgsAndPrepareCall(t *testing.T) {
	for raw, want := range map[string]string{
		``:                                 `{}`,
		`null`:                             `{}`,
		"```json\n{\"query\": \"a\"}\n```": `{"query":"a"}`,
		`{"query": "a",}`:                  `{"query":"a"}`,
		`{"flag": True, "x": None}`:        `{"flag":true,"x":null}`,
		`{'query': 'a'}`:                   `{"query":"a"}`,
		`{"query": "a"`:                    `{"query":"a"}`,
		`"{\"query\": \"a\"}"`:             `{"query":"a"}`,
		`{"query": "True story"}`:          `{"query":"True story"}`,
	} {
		got, err := repairArgs(raw)
		if err != nil || got != want {
			t.Errorf("repairArgs(%q) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{`{query: a}`, `[1, 2]`, `"just text"`} {
		if _, err := repairArgs(bad); err == nil {
			t.Errorf("repairArgs(%q) must fail", bad)
		}
	}

	defs := map[string]llm.ToolDef{"web_search": {Name: "web_search", Parameters: map[string]any{"type": "object",
		"properties": map[string]any{"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}},
		"required":   []string{"query"}}}}
	if args, problem := prepareCall("web_search", `{"query": "x",}`, defs); problem != "" || args != `{"query":"x"}` {
		t.Errorf("small mistakes are repaired silently: %q %q", args, problem)
	}
	if _, problem := prepareCall("web_search", `{query: x}`, defs); !strings.HasPrefix(problem, "Error: invalid arguments JSON (") ||
		!strings.Contains(problem, `{"query": "<string>", "limit": <integer>}`) {
		t.Errorf("broken JSON: %q", problem)
	}
	if _, problem := prepareCall("web_search", `{"limit": 3}`, defs); !strings.HasPrefix(problem, "Error: missing required argument(s): query.") {
		t.Errorf("missing argument: %q", problem)
	}
	if _, problem := prepareCall("websearch", `{}`, defs); !strings.Contains(problem, `unknown tool "websearch". Available tools: web_search.`) {
		t.Errorf("unknown tool: %q", problem)
	}
}

func TestToolCatalog(t *testing.T) {
	cat := toolCatalog([]llm.Tool{{Type: "function", Function: llm.ToolDef{Name: "web_search",
		Description: "Search the web for news. Returns titles and URLs.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"query": map[string]any{"type": "string"},
			"limit": map[string]any{"type": "integer"}}, "required": []string{"query"}}}}}, true)
	for _, want := range []string{"<tool_call>", `{"name": "tool_name", "arguments": {"parameter": "value"}}`, "<tool_response",
		"- web_search(query*: string, limit: integer) — Search the web for news."} {
		if !strings.Contains(cat, want) {
			t.Errorf("catalog misses %q:\n%s", want, cat)
		}
	}
}

func TestHeadTail(t *testing.T) {
	var lines []string
	for i := 0; i < 2000; i++ {
		lines = append(lines, "line "+strings.Repeat("x", 20))
	}
	lines[0], lines[len(lines)-1] = "FIRST", "LAST ERROR"
	out := textutil.HeadTail(strings.Join(lines, "\n"), 15000)
	if n := len([]rune(out)); n > 15000 || n < 14000 {
		t.Errorf("length %d", n)
	}
	if !strings.HasPrefix(out, "FIRST\n") || !strings.HasSuffix(out, "LAST ERROR") || !strings.Contains(out, "... [output truncated, ") {
		t.Errorf("head and tail are kept:\n%s…%s", out[:40], out[len(out)-60:])
	}
	if textutil.HeadTail("short", 15000) != "short" {
		t.Error("short text is untouched")
	}
}

// ---------------------------------------------------------------- the loop with a model that writes its calls

func textModelAgent(t *testing.T, handler http.HandlerFunc) *Agent {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	client := llm.NewClient(srv.URL, "k", "m")
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	_ = sb.WriteFile("s", "notes.txt", []byte("the secret word is pistachio"))
	return NewAgent(client, tools.NewRegistry(sb, search.NewEngine(nil), client, tools.Options{HTTP: netclient.NewClient(netclient.Options{})}), Options{})
}

func reply(w http.ResponseWriter, content string) {
	_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": content}}}})
}

func TestTextModeLoopRunsCallsAndFeedsResultsBack(t *testing.T) {
	var requests []map[string]any
	a := textModelAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		requests = append(requests, req)
		switch len(requests) {
		case 1: // a broken call first
			reply(w, "<tool_call>{\"name\": \"read_file\", \"arguments\": {filename: notes.txt}}</tool_call>")
		case 2:
			reply(w, "ببخشید.\n<tool_call>{\"name\": \"read_file\", \"arguments\": {\"filename\": \"notes.txt\"}}</tool_call>")
		default:
			reply(w, "کلمه رمز **pistachio** است.")
		}
	})
	var streamed []string
	var started []string
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "کلمه رمز چیه؟"},
		Listener: &EventListener{OnText: func(s string) { streamed = append(streamed, s) }, OnToolStart: func(n, _ string) { started = append(started, n) }}})
	if err != nil || res.Answer != "کلمه رمز **pistachio** است." {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Join(started, ",") != "read_file,read_file" {
		t.Errorf("tool events: %v", started)
	}
	// The broken call came back as an error the model could act on…
	second, _ := json.Marshal(requests[1]["messages"])
	if !strings.Contains(string(second), `\u003ctool_response name=\"read_file\"\u003e\nError: invalid arguments JSON`) &&
		!strings.Contains(string(second), `<tool_response name=\"read_file\">\nError: invalid arguments JSON`) {
		t.Errorf("the parse error must reach the model:\n%s", second)
	}
	// …and the real result after the fix, as a user turn (works without native tool support).
	third := requests[2]["messages"].([]any)
	last := third[len(third)-1].(map[string]any)
	if last["role"] != "user" || !strings.Contains(last["content"].(string), "pistachio") {
		t.Errorf("tool results return as <tool_response>: %v", last)
	}
	for _, s := range streamed {
		if strings.Contains(s, "<tool_call") {
			t.Errorf("markup was streamed to the user: %q", s)
		}
	}
	// The system prompt says how to call by text; the tools' definitions go with the call, not as a list in it.
	sys := requests[0]["messages"].([]any)[0].(map[string]any)["content"].(string)
	if !strings.Contains(sys, "<tool_call>") || strings.Contains(sys, "- read_file(filename*: string") {
		t.Errorf("the way to call by text, without the list:\n%s", sys[len(sys)-600:])
	}
	if _, ok := requests[0]["tools"]; !ok {
		t.Error("the call carries the tools' definitions")
	}
}

func TestToolsRejectedByGatewayFallsBackToText(t *testing.T) {
	var withTools, withoutTools int
	a := textModelAgent(t, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if _, ok := req["tools"]; ok {
			withTools++
			http.Error(w, `{"error":{"message":"this model does not support tools"}}`, 400)
			return
		}
		withoutTools++
		if withoutTools == 1 {
			reply(w, `<tool_call>{"name": "list_files", "arguments": {}}</tool_call>`)
			return
		}
		reply(w, "یه فایل notes.txt داری.")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "فایل‌هام؟"}})
	if err != nil || res.Answer != "یه فایل notes.txt داری." {
		t.Fatalf("%+v %v", res, err)
	}
	if withTools != 1 || withoutTools != 2 {
		t.Errorf("after one refusal the run continues without native tools: %d with, %d without", withTools, withoutTools)
	}
}

func TestNativeCallsWinAndDuplicateMarkupIsHidden(t *testing.T) {
	calls := 0
	a := textModelAgent(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant",
				"content":    "یه نگاه بندازم <tool_call>{\"name\": \"list_files\", \"arguments\": {}}</tool_call>",
				"tool_calls": []any{map[string]any{"id": "n1", "type": "function", "function": map[string]any{"name": "list_files", "arguments": "{}"}}}}}}})
			return
		}
		reply(w, "تمام")
	})
	var started int
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"},
		Listener: &EventListener{OnToolStart: func(string, string) { started++ }}})
	if err != nil || res.Answer != "تمام" || started != 1 {
		t.Fatalf("native calls run once, the text copy is ignored: %+v %v, %d runs", res, err, started)
	}
}

// TestOtherCallFormats: DSML (DeepSeek V3.2), Kimi, GLM, Mistral and
// gpt-oss calls written as text are run, and none of their markup is shown.
func TestOtherCallFormats(t *testing.T) {
	known := map[string]bool{"web_search": true}
	cases := map[string]struct{ text, args, rest string }{
		"dsml": {"باشه، می‌گردم.\n<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"web_search\">\n" +
			"<｜DSML｜parameter name=\"query\" string=\"true\">قیمت طلا 18</｜DSML｜parameter>\n" +
			"<｜DSML｜parameter name=\"limit\" string=\"false\">5</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜function_calls>",
			`{"limit":5,"query":"قیمت طلا 18"}`, "باشه، می‌گردم."},
		"dsml-spaced": {"<｜｜DSML｜｜ invoke name=\"web_search\"><｜｜DSML｜｜ parameter name=\"query\" string=\"true\">x</｜｜DSML｜｜ parameter></｜｜DSML｜｜ invoke>",
			`{"query":"x"}`, ""},
		"kimi": {"<|tool_calls_section_begin|><|tool_call_begin|>functions.web_search:0<|tool_call_argument_begin|>{\"query\":\"x\"}<|tool_call_end|><|tool_calls_section_end|>",
			`{"query":"x"}`, ""},
		"glm":          {"<tool_call>web_search\n<arg_key>query</arg_key>\n<arg_value>x</arg_value>\n</tool_call>", `{"query":"x"}`, ""},
		"mistral":      {"[TOOL_CALLS][{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]", `{"query":"x"}`, ""},
		"mistral-args": {"[TOOL_CALLS]web_search[ARGS]{\"query\": \"x\"}", `{"query":"x"}`, ""},
		"gpt-oss": {"<|start|>assistant<|channel|>commentary to=functions.web_search <|constrain|>json<|message|>{\"query\":\"x\"}<|call|>",
			`{"query":"x"}`, ""},
	}
	for name, c := range cases {
		calls, rest := parseTextToolCalls(c.text, known)
		if len(calls) != 1 || calls[0].Function.Name != "web_search" || normJSON(t, calls[0].Function.Arguments) != normJSON(t, c.args) {
			t.Errorf("%s: calls %+v", name, calls)
			continue
		}
		if rest != c.rest {
			t.Errorf("%s: left %q, want %q", name, rest, c.rest)
		}
	}
}

func normJSON(t *testing.T, s string) string {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		return "bad json: " + s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// TestNoInternalMarkupReachesTheUser: tags a model leaves in its answer,
// template tokens and inline reasoning are removed; code keeps them.
func TestNoInternalMarkupReachesTheUser(t *testing.T) {
	for in, want := range map[string]string{
		"سلام! </｜｜DSML｜｜ parameter>اینم جواب":                              "سلام! اینم جواب",
		"جواب نهایی<|im_end|>":                                              "جواب نهایی",
		"قیمت امروز ۴۵ دلاره.<｜end▁of▁sentence｜>":                           "قیمت امروز ۴۵ دلاره.",
		"done</parameter>\n</invoke>":                                       "done",
		"[TOOL_CALLS] nothing to call":                                      "nothing to call",
		"<think>the user wants a poem, keep it short</think>\nیه شعر کوتاه": "یه شعر کوتاه",
		"I should answer in Persian.</think>سلام 👋":                         "سلام 👋",
		"use `<|im_end|>` to stop":                                          "use `<|im_end|>` to stop",
		"```xml\n<|im_start|>user\n```":                                     "```xml\n<|im_start|>user\n```",
		"in Elm, a <| b |> c works":                                         "in Elm, a <| b |> c works",
	} {
		if got := finalText(in, false, "fa"); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"<think>still thinking":        "",
		"<thi":                         "",
		"<think>done</think>جواب":      "جواب",
		"جواب <｜DSML｜function_calls>…": "جواب",
		"جواب <|im_":                   "جواب",
		"جواب [TOOL_CALLS][{\"name\":": "جواب",
	} {
		if got := visibleStream(in); got != want {
			t.Errorf("stream %q → %q, want %q", in, got, want)
		}
	}
}
