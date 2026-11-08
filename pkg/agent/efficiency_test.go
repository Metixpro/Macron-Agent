package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestCLIRequestBudgetAndCustomPersonality(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "Ready."})
	a.Channel = ChannelCLI
	a.Soul = func() string { return "CUSTOM_PERSONALITY: speak clearly and keep private notes private." }
	_, err := a.Do(context.Background(), Request{SessionID: "budget", Input: llm.Message{Content: "Inspect the Python project"}, FocusTools: true, EfficientTools: true})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(script.requests[0])
	t.Logf("initial request bytes: %d", len(raw))
	if len(raw) > 40000 {
		t.Fatalf("initial request exceeded 40 KB budget: %d", len(raw))
	}
	sys := msgs(script.requests[0])[0]["content"].(string)
	if !strings.Contains(sys, "CUSTOM_PERSONALITY") || !strings.Contains(sys, "Ground rules") {
		t.Fatal("personality or ground rules lost")
	}

	if strings.Contains(sys, "Inspect the Python project") || strings.Contains(sys, "2026-10-18") {
		t.Fatal("volatile data in system prefix")
	}
}

func TestFocusedDiscoveryRetainsHiddenCapability(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("load", ToolMoreTools, `{"need":"special tool"}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("run", "special_capability", `{}`)}},
		llm.Message{Role: "assistant", Content: "Verified."})
	a.Channel = ChannelCLI
	calls := 0
	a.Registry.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: "special_capability", Description: "Special operation", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, func(context.Context, string, map[string]any) (string, error) {
		calls++
		return "actual capability ran", nil
	})
	_, err := a.Do(context.Background(), Request{SessionID: "discover", Input: llm.Message{Content: "Do the task"}, FocusTools: true, EfficientTools: true})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("hidden capability executed %d times", calls)
	}
	if offeredTools(script.requests[0])["special_capability"] {
		t.Fatal("hidden tool schema loaded prematurely")
	}
	if !offeredTools(script.requests[1])["special_capability"] {
		t.Fatal("discovery did not load schema")
	}
	first := msgs(script.requests[0])[0]["content"].(string)
	second := msgs(script.requests[1])[0]["content"].(string)
	if first != second {
		t.Fatal("discovery mutated system prefix")
	}
}

func TestRestrictedPromptDoesNotAdvertiseAbsentCapabilities(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "Ready."})
	_, err := a.Do(context.Background(), Request{SessionID: "limited", Input: llm.Message{Content: "write a note"}, ToolFilter: func(name string) bool { return name == "write_file" }})
	if err != nil {
		t.Fatal(err)
	}
	sys := msgs(script.requests[0])[0]["content"].(string)
	if strings.Contains(sys, "After you create or edit an image") || strings.Contains(sys, "Developer mode (code runner") {
		t.Fatal("instructions require filtered-out tools")
	}
}

func TestEfficientCLILoadsRealSkillOnDemandWithoutEagerIndex(t *testing.T) {
	a, script := skillAgent(t, nil, true,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("load", ToolMoreTools, `{"need":"read skill"}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("skill", "read_skill", `{"name":"linux-server-ops"}`)}},
		llm.Message{Role: "assistant", Content: "Ready."})
	a.Channel = ChannelCLI
	_, err := a.Do(context.Background(), Request{SessionID: "skill-discovery", Input: llm.Message{Role: "user", Content: "Use my server procedure"}, FocusTools: true, EfficientTools: true})
	if err != nil {
		t.Fatal(err)
	}
	first := msgs(script.requests[0])[0]["content"].(string)
	if strings.Contains(first, "<available_skills>") {
		t.Fatal("hidden skill index inflated initial request")
	}
	raw, _ := json.Marshal(script.requests[len(script.requests)-1])
	if !strings.Contains(string(raw), "uptime first") {
		t.Fatal("real skill body was not loaded after discovery")
	}
	for _, req := range script.requests {
		if msgs(req)[0]["content"] != first {
			t.Fatal("discovery mutated the stable system prefix")
		}
	}
}
