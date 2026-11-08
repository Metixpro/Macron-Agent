package agent

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func fixDefs() map[string]llm.ToolDef {
	obj := func(props map[string]any, req ...string) map[string]any {
		return map[string]any{"type": "object", "properties": props, "required": req}
	}
	return map[string]llm.ToolDef{
		"web_search": {Name: "web_search", Parameters: obj(map[string]any{
			"query": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			"news": map[string]any{"type": "boolean"}, "region": map[string]any{"type": "string", "enum": []string{"ir-fa", "us-en"}}}, "query")},
		"write_file": {Name: "write_file", Parameters: obj(map[string]any{
			"filename": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "filename", "content")},
		"send_file": {Name: "send_file", Parameters: obj(map[string]any{"filename": map[string]any{"type": "string"}, "caption": map[string]any{"type": "string"}}, "filename")},
		"view_image": {Name: "view_image", Parameters: obj(map[string]any{
			"filenames": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}, "filenames")},
		"run_code": {Name: "run_code", Parameters: obj(map[string]any{"code": map[string]any{"type": "string"},
			"language": map[string]any{"type": "string", "enum": []any{"python", "javascript", "go"}}, "timeout": map[string]any{"type": "number"}}, "code")},
		"schedule": {Name: "schedule", Parameters: obj(map[string]any{"when": map[string]any{"type": "object", "properties": map[string]any{
			"hour": map[string]any{"type": "integer"}, "daily": map[string]any{"type": "boolean"}}}, "task": map[string]any{"type": "string"}}, "task")},
		"generate_image": {Name: "generate_image", Parameters: obj(map[string]any{"prompt": map[string]any{"type": "string"}}, "prompt")},
		"web_fetch":      {Name: "web_fetch", Parameters: obj(map[string]any{"url": map[string]any{"type": "string"}}, "url")},
	}
}

func TestResolveTool(t *testing.T) {
	defs := fixDefs()
	for in, want := range map[string]string{
		"web_search": "web_search", "functions.web_search": "web_search", "default_api:web_search": "web_search", "WebSearch": "web_search",
		"web-search": "web_search", "`web_search()`": "web_search", "google_search": "web_search", "search": "web_search",
		"web_serach": "web_search", "generat_image": "generate_image", "python": "run_code", "fetch_url": "web_fetch", "createImage": "generate_image",
		"tools.send_file": "send_file", "upload_file": "send_file",
	} {
		if got, ok := resolveTool(in, defs); !ok || got != want {
			t.Errorf("%q → %q %v, want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"", "x", "make_coffee", "delete_everything", "bash_execute"} {
		if got, ok := resolveTool(in, defs); ok {
			t.Errorf("%q is not a tool here, got %q", in, got)
		}
	}
}

func fixed(t *testing.T, name, args string) (string, map[string]any, callFix) {
	t.Helper()
	tc := llm.ToolCall{Function: llm.FunctionCall{Name: name, Arguments: args}}
	fx := fixCall(&tc, fixDefs())
	var m map[string]any
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &m); err != nil {
		t.Fatalf("%s %s: %v", name, tc.Function.Arguments, err)
	}
	return tc.Function.Name, m, fx
}

func TestFixCallArguments(t *testing.T) {
	for _, c := range []struct {
		name, args string
		want       map[string]any
		confused   bool
	}{
		{"web_search", `{"q": "دلار", "limit": "۵", "news": "yes", "region": "IR-FA"}`, map[string]any{"query": "دلار", "limit": 5.0, "news": true, "region": "ir-fa"}, true},
		{"web_search", `{"searchQuery": "x", "limit": null}`, map[string]any{"query": "x"}, true},
		{"web_search", `{"input": "x"}`, map[string]any{"query": "x"}, true}, // the one missing parameter
		{"web_search", `{"query": ["قیمت طلا"]}`, map[string]any{"query": "قیمت طلا"}, false},
		{"web_search", `{"query": "x", "limit": "1,500"}`, map[string]any{"query": "x", "limit": 1500.0}, false},
		{"write_file", `{"file_path": "a.py", "text": "print(1)"}`, map[string]any{"filename": "a.py", "content": "print(1)"}, true},
		{"write_file", `{"filename": "d.json", "content": {"a": 1}}`, map[string]any{"filename": "d.json", "content": `{"a":1}`}, false},
		{"send_file", `{"path": "out.pdf"}`, map[string]any{"filename": "out.pdf"}, true},
		{"view_image", `{"filenames": "a.png"}`, map[string]any{"filenames": []any{"a.png"}}, false},
		{"view_image", `{"filenames": "[\"a.png\", \"b.png\"]"}`, map[string]any{"filenames": []any{"a.png", "b.png"}}, false},
		{"view_image", `{"file": "a.png"}`, map[string]any{"filenames": []any{"a.png"}}, true},
		{"run_code", `{"code": "print(1)", "language": "Python", "timeout": "30"}`, map[string]any{"code": "print(1)", "language": "python", "timeout": 30.0}, false},
		{"run_code", `{"script": "print(1)", "language": "js"}`, map[string]any{"code": "print(1)", "language": "js"}, true}, // an enum it cannot tell stays (the tool says what it takes)
		{"schedule", `{"task": "x", "when": "{\"hour\": \"9\", \"daily\": \"true\"}"}`, map[string]any{"task": "x", "when": map[string]any{"hour": 9.0, "daily": true}}, false},
		{"generate_image", `{"description": "a cat"}`, map[string]any{"prompt": "a cat"}, true},
	} {
		_, got, fx := fixed(t, c.name, c.args)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s %s → %v, want %v (%s)", c.name, c.args, got, c.want, fx)
		}
		if fx.confused != c.confused {
			t.Errorf("%s %s: confused %v (%s)", c.name, c.args, fx.confused, fx)
		}
	}

	// Right as it is: untouched.
	tc := llm.ToolCall{Function: llm.FunctionCall{Name: "web_search", Arguments: `{"query":"x","limit":3}`}}
	if fx := fixCall(&tc, fixDefs()); fx.any() || tc.Function.Arguments != `{"query":"x","limit":3}` {
		t.Errorf("a right call: %s %s", fx, tc.Function.Arguments)
	}
	// What cannot be told is left for the model to fix.
	name, got, _ := fixed(t, "web_search", `{"query": "x", "limit": "a few"}`)
	if name != "web_search" || got["limit"] != "a few" {
		t.Errorf("not a number: %v", got)
	}
	if _, got, _ = fixed(t, "write_file", `{"filename": "a", "content": "b", "mode": "w"}`); got["mode"] != "w" {
		t.Errorf("an extra parameter is kept: %v", got)
	}
	tc = llm.ToolCall{Function: llm.FunctionCall{Name: "functions.WebSearch", Arguments: `{'q': 'x',}`}}
	if fx := fixCall(&tc, fixDefs()); tc.Function.Name != "web_search" || tc.Function.Arguments != `{"query":"x"}` || !fx.confused || fx.from != "functions.WebSearch" {
		t.Errorf("name and broken JSON together: %s %s %s", tc.Function.Name, tc.Function.Arguments, fx)
	}
	if !strings.Contains(fx2(t).String(), "tool ") {
		t.Error("a fix says what it did")
	}
}

func fx2(t *testing.T) callFix {
	tc := llm.ToolCall{Function: llm.FunctionCall{Name: "google", Arguments: `{"q":"x"}`}}
	return fixCall(&tc, fixDefs())
}
