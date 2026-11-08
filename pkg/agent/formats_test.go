package agent

import (
	"regexp"
	"strings"
	"testing"
)

var formatKnown = map[string]bool{"web_search": true, "bash_execute": true, "generate_chart": true, "write_file": true, "run_code": true}

var markupSeen = regexp.MustCompile(`(?i)<\s*/?\s*(?:[a-z0-9_-]+:)?(?:tool_call|tool_calls|function|function_calls?|invoke|parameter|arg_key|arg_value|tool_use|tools)\b|` +
	`<[|｜]|[|｜]>|\[TOOL_CALLS\]|\[ARGS\]|to=functions\.|^\s*Action:|"name"\s*:\s*"(?:web_search|bash_execute|generate_chart|write_file|run_code)"|` +
	`(?m:^\s*(?:web_search|bash_execute|generate_chart|write_file|run_code)\()|DSML|python_tag|functions\.[a-z_]+:\d`)

func TestEveryCallFormatIsReadAndNeverShown(t *testing.T) {
	cases := []struct {
		name, text string
		calls      []string
		keep       string
	}{
		{"hermes (Qwen, MiMo, Hermes)", "باشه، الان می‌کشمش.\n<tool_call>\n{\"name\": \"generate_chart\", \"arguments\": {\"title\": \"فروش\", \"values\": [12, 18]}}\n</tool_call>",
			[]string{"generate_chart"}, "الان می‌کشمش"},
		{"GLM", "<tool_call>web_search\n<arg_key>query</arg_key>\n<arg_value>قیمت دلار</arg_value>\n</tool_call>", []string{"web_search"}, ""},
		{"Qwen3-Coder", "اجراش می‌کنم:\n<function=bash_execute>\n<parameter=command>\nls -la\n</parameter>\n</function>", []string{"bash_execute"}, "اجراش می‌کنم"},
		{"Anthropic XML", "<function_calls>\n<invoke name=\"web_search\">\n<parameter name=\"query\">tgs</parameter>\n</invoke>\n</function_calls>", []string{"web_search"}, ""},
		{"MiniMax (namespaced)", "<minimax:tool_call>\n<invoke name=\"bash_execute\">\n<parameter name=\"command\">pwd</parameter>\n</invoke>\n</minimax:tool_call>", []string{"bash_execute"}, ""},
		{"namespaced parameter", "<rfl:invoke name=\"web_search\">\n<rfl:parameter name=\"query\">x</rfl:parameter>\n</rfl:invoke>", []string{"web_search"}, ""},
		{"DeepSeek DSML", "<｜DSML｜function_calls>\n<｜DSML｜invoke name=\"web_search\">\n<｜DSML｜parameter name=\"query\" string=\"true\">x</｜DSML｜parameter>\n</｜DSML｜invoke>\n</｜DSML｜function_calls>", []string{"web_search"}, ""},
		{"DeepSeek tokens", "<｜tool▁calls▁begin｜><｜tool▁call▁begin｜>function<｜tool▁sep｜>web_search\n```json\n{\"query\": \"x\"}\n```<｜tool▁call▁end｜><｜tool▁calls▁end｜>", []string{"web_search"}, ""},
		{"Kimi K2", "<|tool_calls_section_begin|><|tool_call_begin|>functions.web_search:0<|tool_call_argument_begin|>{\"query\": \"x\"}<|tool_call_end|><|tool_calls_section_end|>", []string{"web_search"}, ""},
		{"Llama python_tag", "<|python_tag|>{\"name\": \"web_search\", \"parameters\": {\"query\": \"x\"}}", []string{"web_search"}, ""},
		{"Mistral", "[TOOL_CALLS][{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]", []string{"web_search"}, ""},
		{"gpt-oss harmony", "<|channel|>commentary to=functions.web_search <|constrain|>json<|message|>{\"query\": \"x\"}<|call|>", []string{"web_search"}, ""},
		{"fenced JSON", "می‌گردم:\n```json\n{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}\n```", []string{"web_search"}, "می‌گردم"},
		{"bare JSON", "{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}", []string{"web_search"}, ""},
		{"ReAct", "Action: web_search\nAction Input: {\"query\": \"x\"}", []string{"web_search"}, ""},
		{"pythonic", "یه لحظه\nweb_search(query=\"قیمت دلار\")", []string{"web_search"}, "یه لحظه"},
		{"pythonic list", "[web_search(query=\"a\"), run_code(language=\"python\", code=\"print(1)\")]", []string{"web_search", "run_code"}, ""},
		{"Granite", "<|tool_call|>[{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]", []string{"web_search"}, ""},
		{"InternLM", "<|action_start|><|plugin|>\n{\"name\": \"web_search\", \"parameters\": {\"query\": \"x\"}}<|action_end|>", []string{"web_search"}, ""},
		{"Phi-4", "functools[{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]", []string{"web_search"}, ""},
		{"LongCat (prefixed)", "<longcat_tool_call>{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}</longcat_tool_call>", []string{"web_search"}, ""},
		{"Seed-OSS", "<seed:tool_call>\n<function=web_search>\n<parameter=query>x</parameter>\n</function>\n</seed:tool_call>", []string{"web_search"}, ""},
		{"Step-3", "<|tool_calls_begin|><|tool_call_begin|>function<|tool_sep|><steptml:invoke name=\"web_search\"><steptml:parameter name=\"query\">x</steptml:parameter></steptml:invoke><|tool_call_end|><|tool_calls_end|>", []string{"web_search"}, ""},
		{"Jamba / Hunyuan", "<tool_calls>[{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]</tool_calls>", []string{"web_search"}, ""},
		{"xLAM", "[{\"name\": \"web_search\", \"arguments\": {\"query\": \"x\"}}]", []string{"web_search"}, ""},
		{"work-notes imitation (not a call)", "فایل آماده‌ست ✅\nbash_execute(python3 - << 'EOF' import json) → {\n</rfl:parameter>", nil, "فایل آماده‌ست"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			calls, clean := parseLooseToolCalls(c.text, formatKnown)
			var names []string
			for _, tc := range calls {
				names = append(names, tc.Function.Name)
			}
			if strings.Join(names, ",") != strings.Join(c.calls, ",") {
				t.Errorf("calls %v, want %v", names, c.calls)
			}
			for _, tc := range calls {
				if a := strings.TrimSpace(tc.Function.Arguments); a == "" || a[0] != '{' {
					t.Errorf("%s: arguments %q", tc.Function.Name, a)
				}
			}
			final := dropImitatedCalls(stripToolMarkup(clean, formatKnown), formatKnown)
			if markupSeen.MatchString(final) {
				t.Errorf("the answer shows markup: %q", final)
			}
			if c.keep != "" && !strings.Contains(final, c.keep) {
				t.Errorf("the answer lost its words: %q", final)
			}

			for i := 1; i <= len(c.text); i++ {
				if !utf8Boundary(c.text, i) {
					continue
				}
				if v := visibleStream(c.text[:i]); markupSeen.MatchString(v) {
					t.Fatalf("the draft showed markup at %d: %q", i, v)
				}
			}
		})
	}
}

func utf8Boundary(s string, i int) bool { return i == len(s) || (s[i]&0xC0) != 0x80 }

func TestOrdinaryAnswersStreamWhole(t *testing.T) {
	for _, s := range []string{
		"سلام! این یه جواب معمولیه.\n\n- مورد اول\n- مورد دوم\n\n۱. قدم اول\n۲. قدم دوم",
		"این کد رو ببین:\n```python\ndef print_hello():\n    print_hello_world()\nread_file(\"a.txt\")\n```\nتموم.",
		"یه نمونه JSON:\n```json\n{\"name\": \"Ali\", \"age\": 30}\n```",
		"[سایت رسمی](https://example.com) رو ببین.\nبعد هم جدول:\n| a | b |\n|---|---|\n| 1 | 2 |",
		"The function `web_search` finds pages; you call it like web_search(query) in your head.",
		"قیمت‌ها: {دلار: ۶۰ هزار} و [یورو: ۶۵ هزار]",
	} {
		if got := visibleStream(s); got != s {
			t.Errorf("cut:\n%q\nfrom:\n%q", got, s)
		}
	}
}
