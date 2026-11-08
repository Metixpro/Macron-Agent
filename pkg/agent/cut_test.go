package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// When the output limit cuts a reply, its last call may have lost the end of
// its arguments: it is not run, the model is told why, and the calls before
// it are run as usual.
func TestACutCallIsNotRun(t *testing.T) {
	a, script := setup(t,
		finished{map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{
			toolCall("c1", "write_file", `{"filename":"first.txt","content":"whole"}`),
			toolCall("c2", "write_file", `{"filename":"half.txt","content":"the end is mis`),
		}}, "length"},
		llm.Message{Role: "assistant", Content: "done"},
	)
	ans, err := a.Run(context.Background(), "cut", "write two files", nil, nil)
	if err != nil || ans != "done" {
		t.Fatalf("ans=%q err=%v", ans, err)
	}
	var tool1, tool2, notice string
	for _, m := range msgs(script.requests[1]) {
		if m["role"] == "tool" && m["tool_call_id"] == "c1" {
			tool1, _ = m["content"].(string)
		}
		if m["role"] == "tool" && m["tool_call_id"] == "c2" {
			tool2, _ = m["content"].(string)
		}
		if c, _ := m["content"].(string); m["role"] == "user" && strings.Contains(c, "output length limit") {
			notice = c
		}
	}
	if !strings.Contains(tool1, "saved") {
		t.Errorf("the whole call runs: %q", tool1)
	}
	if !strings.Contains(tool2, "NOT run") || notice == "" {
		t.Errorf("the model is told the call was cut: %q / %q", tool2, notice)
	}
}

// A read-only call repeated past the limit is answered from its last result,
// unless something that writes ran in between (then it reads again).
func TestRepeatedReadsAreAnsweredFromTheLastResult(t *testing.T) {
	read := func(id string) map[string]any {
		return map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall(id, "read_file", `{"filename":"a.txt"}`)}}
	}
	a, script := setup(t, read("r1"), read("r2"), read("r3"),
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("w1", "write_file", `{"filename":"a.txt","content":"new"}`)}},
		read("r4"), read("r5"),
		llm.Message{Role: "assistant", Content: "done"},
	)
	a.MaxSteps = 8
	_ = a.Registry.Machine.WriteFile("rr", "a.txt", []byte("old"))
	before := 0
	_, err := a.Run(context.Background(), "rr", "read it", nil, &EventListener{OnToolStart: func(n, _ string) {
		if n == "read_file" {
			before++
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	if before != 3 {
		t.Errorf("the third identical read is answered from the second, the read after the write runs: %d reads ran", before)
	}
	got := map[string]string{}
	for _, m := range msgs(script.requests[len(script.requests)-1]) {
		if m["role"] == "tool" {
			got[m["tool_call_id"].(string)], _ = m["content"].(string)
		}
	}
	if !strings.Contains(got["r3"], "old") || !strings.Contains(got["r3"], "already made this exact call") {
		t.Errorf("the repeat gets the last result and the notice: %q", got["r3"])
	}
	if !strings.Contains(got["r4"], "new") {
		t.Errorf("after a write the file is read again: %q", got["r4"])
	}
}
