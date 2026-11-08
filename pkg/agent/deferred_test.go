package agent

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type fakePlugins struct{ called []string }

func (f *fakePlugins) Tools() []llm.Tool {
	var out []llm.Tool
	for _, n := range []string{"sqlite_query", "sqlite_tables", "sqlite_describe", "ripe_whois", "ripe_asn_lookup", "ripe_prefix_overview"} {
		out = append(out, llm.Tool{Type: "function", Function: llm.ToolDef{Name: n, Description: "SQLite and network tools: " + strings.ReplaceAll(n, "_", " ") + ".",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"q": map[string]any{"type": "string"}}}}})
	}
	for i := 0; i < 40; i++ {
		out = append(out, llm.Tool{Type: "function", Function: llm.ToolDef{Name: fmt.Sprintf("seclens_check_%02d", i), Description: "A security check.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}})
	}
	return out
}
func (f *fakePlugins) Has(name string) bool {
	return slices.ContainsFunc(f.Tools(), func(t llm.Tool) bool { return t.Function.Name == name })
}
func (f *fakePlugins) Allowed(string, string) bool { return true }
func (f *fakePlugins) Call(_ context.Context, _, name string, _ map[string]any) (string, error) {
	f.called = append(f.called, name)
	return "3 rows", nil
}

func TestPluginToolsWaitUntilSearched(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", ToolSearch, `{"query":"sqlite query"}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c2", "sqlite_query", `{"q":"select 1"}`)}},
		llm.Message{Role: "assistant", Content: "۳ ردیف بود"},
	)
	plugins := &fakePlugins{}
	a.Registry.SetPlugins(plugins)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "تعداد کاربرها رو از دیتابیس دربیار"}})
	if err != nil {
		t.Fatal(err)
	}
	first, second := toolNames(script.requests[0]), toolNames(script.requests[1])
	if slices.Contains(first, "sqlite_query") || slices.Contains(first, "seclens_check_00") || !slices.Contains(first, ToolSearch) {
		t.Fatalf("the first call carries no plugin tool, and tool_search: %v", first)
	}
	if !strings.Contains(llmText(script.requests[0]), "46 more tools from plugins") || !strings.Contains(llmText(script.requests[0]), "sqlite") {
		t.Fatal("the model is told what waits")
	}
	if !slices.Contains(second, "sqlite_query") || slices.Contains(second, "seclens_check_00") {
		t.Fatalf("what the search found is offered next: %v", second)
	}
	if res.Answer != "۳ ردیف بود" || !slices.Equal(plugins.called, []string{"sqlite_query"}) {
		t.Fatalf("answer %q, plugin calls %v", res.Answer, plugins.called)
	}
}

func TestANamedPluginToolIsLoaded(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "باشه"})
	a.Registry.SetPlugins(&fakePlugins{})
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "با ripe_whois اینو چک کن"}}); err != nil {
		t.Fatal(err)
	}
	if got := toolNames(script.requests[0]); !slices.Contains(got, "ripe_whois") || slices.Contains(got, "sqlite_query") || slices.Contains(got, "seclens_check_00") {
		t.Fatalf("named: %v", got)
	}
}

func llmText(req map[string]any) string { return fmt.Sprint(req["messages"]) }

func TestAFamilyTheMessageNamesIsLoaded(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "sqlite_query", `{"q":"select count(*) from users"}`)}},
		llm.Message{Role: "assistant", Content: "۱۳۴۲"},
	)
	plugins := &fakePlugins{}
	a.Registry.SetPlugins(plugins)
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "از دیتابیس sqlite تعداد کاربرها رو بگو"}}); err != nil {
		t.Fatal(err)
	}
	first := toolNames(script.requests[0])
	if !slices.Contains(first, "sqlite_query") || !slices.Contains(first, "sqlite_tables") || slices.Contains(first, "ripe_whois") ||
		slices.Contains(first, "seclens_check_00") || !strings.Contains(llmText(script.requests[0]), "Loaded for this request: sqlite_") {
		t.Fatalf("sqlite loaded at the start: %v", first)
	}
	if len(script.requests) != 2 || !slices.Equal(plugins.called, []string{"sqlite_query"}) {
		t.Fatalf("two model calls, one plugin call: %d %v", len(script.requests), plugins.called)
	}
}
