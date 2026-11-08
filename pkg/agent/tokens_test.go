package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func TestSmallTalkIsOnlyConversation(t *testing.T) {
	for _, s := range []string{"سلام", "سلاااام!", "سلام خوبی؟", "ممنون 🙏", "مرسی داداش", "دمت گرم", "شب بخیر", "چه خبر؟",
		"خوبی مکرون؟", "hi", "Hey!", "thanks a lot", "❤️", "😂😂", "خخخخ", "ایول عالی بود", "خداحافظ رفیق", "good morning"} {
		if !IsSmallTalk(s) {
			t.Errorf("%q is conversation", s)
		}
	}
	for _, s := range []string{"", "سلام، قیمت دلار چنده؟", "ساعت ۵ بیدارم کن", "آره", "باشه", "اوکی", "بله", "ok", "یه عکس بساز",
		"https://youtu.be/xyz", "مرسی، حالا PDFش رو بفرست", "ادامه بده", "hi, can you search this", "سلام " + strings.Repeat("خوبی ", 20),
		"سلام بیا ۲ تا آهنگ بفرست", "خوبی؟ یه سوال داشتم"} {
		if IsSmallTalk(s) {
			t.Errorf("%q needs the tools", s)
		}
	}
}

func toolNames(req map[string]any) []string {
	var names []string
	list, _ := req["tools"].([]any)
	for _, x := range list {
		names = append(names, x.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	return names
}

func TestAGreetingGoesWithoutTheTools(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "سلام رفیق 👋"})
	var history []llm.Message
	for i := range 30 {
		history = append(history, llm.Message{Role: "user", Content: fmt.Sprintf("پیام %d %s", i, strings.Repeat("متن ", 150))},
			llm.Message{Role: "assistant", Content: strings.Repeat("جواب ", 150)})
	}
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "سلام"}, Chat: true, History: history})
	if err != nil || res.Answer != "سلام رفیق 👋" {
		t.Fatalf("%+v %v", res, err)
	}
	req := script.requests[0]
	if names := toolNames(req); len(names) != 1 || names[0] != toolUseTools {
		t.Fatalf("tools: %v", names)
	}
	m := msgs(req)
	sys := m[0]["content"].(string)
	if strings.Contains(sys, "Choosing tools") || strings.Contains(sys, "Available tools") || !strings.Contains(sys, "Ground rules") ||
		!strings.Contains(sys, toolUseTools) {
		t.Fatalf("the light prompt: %s", sys)
	}
	raw, _ := json.Marshal(req)
	if len(raw) > 45000 {
		t.Errorf("a greeting sends %d bytes", len(raw))
	}
}

func TestAConversationCanAskForTheTools(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("u1", toolUseTools, `{}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "list_files", `{}`)}},
		llm.Message{Role: "assistant", Content: "اینا رو داری"})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "مرسی"}, Chat: true})
	if err != nil || res.Answer != "اینا رو داری" || len(res.ToolsUsed) != 1 || res.ToolsUsed[0] != "list_files" {
		t.Fatalf("%+v %v", res, err)
	}
	second := script.requests[1]
	if names := toolNames(second); len(names) < 5 || strings.Contains(strings.Join(names, ","), toolUseTools) {
		t.Fatalf("the second round has every tool: %v", names)
	}
	m := msgs(second)
	if !strings.Contains(m[0]["content"].(string), "Choosing tools") || m[len(m)-1]["content"] != "مرسی" {
		t.Fatalf("it starts over with the full prompt and the user's message: %v", m[len(m)-1])
	}
}

func TestEarlierOutputsStayWholeUntilTheJobGrows(t *testing.T) {
	page := strings.Repeat("خط طولانی از یه صفحه وب\n", 400)
	round := func(id, name string) []llm.Message {
		return []llm.Message{{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall(id, name, "{}")}},
			{Role: "tool", ToolCallID: id, Name: name, Content: page}}
	}
	messages := []llm.Message{{Role: "system", Content: "s"}, {Role: "user", Content: "go"}}
	for i := 1; i <= 5; i++ {
		messages = append(messages, round(fmt.Sprint(i), "web_fetch")...)
	}
	trimStaleToolOutputs(messages)
	for i, m := range messages {
		if m.Role == "tool" && m.Content != page {
			t.Fatalf("output %d was shortened while the job is small", i)
		}
	}
	messages = append(messages[:2:2], append(round("s", tools.ToolReadSkill), messages[2:]...)...)
	for i := 6; i <= 9; i++ {
		messages = append(messages, round(fmt.Sprint(i), "web_fetch")...)
	}
	trimStaleToolOutputs(messages)
	var cut []int
	for i, m := range messages {
		if m.Role == "tool" && m.Content != page {
			cut = append(cut, i)
			if !strings.HasSuffix(m.Content, staleNote) || len([]rune(m.Content)) > staleToolRunes+200 {
				t.Errorf("output %d: %d runes", i, len([]rune(m.Content)))
			}
		}
	}
	if len(cut) == 0 || cut[0] != 5 {
		t.Fatalf("the oldest page is shortened first (the skill stays): %v", cut)
	}
	if messages[3].Content != page {
		t.Error("a skill stays whole")
	}
	for _, i := range []int{len(messages) - 1, len(messages) - 3, len(messages) - 5} {
		if messages[i].Content != page {
			t.Errorf("the last rounds stay whole (%d)", i)
		}
	}
	once := slices.Clone(messages)
	trimStaleToolOutputs(messages)
	for i := range messages {
		if messages[i].Content != once[i].Content {
			t.Fatal("a shortened output never changes again (the cache keeps it)")
		}
	}
}

func TestASubAgentGetsWhatItsPartNeeds(t *testing.T) {
	got := subTools([]any{"images"}, "یه پوستر برای کنسرت بساز")
	for _, want := range []string{"generate_image", "read_file", "write_file", tools.ToolReadSkill, "send_file"} {
		if !got[want] {
			t.Errorf("missing %s", want)
		}
	}
	for _, not := range []string{ToolDelegate, tools.ToolRunCode, tools.ToolBrowser, "ask_user", "schedule"} {
		if got[not] {
			t.Errorf("%s is not for this part", not)
		}
	}
	if web := subTools(nil, "a few words"); !web["web_search"] || web["generate_image"] {
		t.Error("with nothing to go on, a part gets the web")
	}
	if code := subTools(nil, "fix the bug in the python script"); !code[tools.ToolRunCode] {
		t.Error("the goal shows what it needs")
	}
}

func TestASubAgentWorksWithAFewToolsAndAShortPrompt(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("d1", ToolDelegate, `{"goal":"research the history of Tehran","toolsets":["web"]}`)}},
		llm.Message{Role: "assistant", Content: "Report: Tehran became the capital in 1786."},
		llm.Message{Role: "assistant", Content: "تهران از ۱۷۸۶ پایتخته."})
	a.MaxSteps = 4
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "تاریخ تهران رو تحقیق کن"}})
	if err != nil || !strings.Contains(res.Answer, "۱۷۸۶") {
		t.Fatalf("%+v %v", res, err)
	}
	sub := script.requests[1]
	names := strings.Join(toolNames(sub), ",")
	if strings.Contains(names, ToolDelegate) || !strings.Contains(names, "web_search") || strings.Contains(names, "generate_image") {
		t.Fatalf("the sub-agent's tools: %s", names)
	}
	sys := msgs(sub)[0]["content"].(string)
	if !strings.Contains(sys, "You are a sub-agent") || strings.Contains(sys, "Ground rules") || len(sys) > len(msgs(script.requests[0])[0]["content"].(string))/2 {
		t.Fatalf("the sub-agent's prompt (%d bytes): %s", len(sys), sys)
	}
}

func TestTheInstructionsNeverListTheTools(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "<tool_call>{\"name\": \"list_files\", \"arguments\": {}}</tool_call>"},
		llm.Message{Role: "assistant", Content: "done"},
		llm.Message{Role: "assistant", Content: "again"})
	_, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "files?"}})
	_, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "and now?"}})
	first, later := msgs(script.requests[0])[0]["content"].(string), msgs(script.requests[2])[0]["content"].(string)
	if strings.Contains(first, "Available tools") || strings.Contains(later, "Available tools") || !strings.Contains(later, "<tool_call>") {
		t.Fatal("no list, in the first request or after a call written as text; the way to call by text stays")
	}
	if first != later {
		t.Error("the instructions stay the same between requests (a call written as text changes nothing)")
	}
}

func TestAskingForTheToolsCostsNoStep(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("u1", toolUseTools, `{}`)}},
		llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{toolCall("c1", "list_files", `{}`)}},
		llm.Message{Role: "assistant", Content: "done"})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "مرسی"}, Chat: true, MaxSteps: 2, AnswerOnLastStep: true})
	if err != nil || res.Answer != "done" || len(res.ToolsUsed) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if names := toolNames(script.requests[1]); len(names) < 5 {
		t.Fatalf("the first real step has the tools: %v", names)
	}
}
