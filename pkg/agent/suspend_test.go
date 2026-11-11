package agent

import (
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestSuspendedJSONSerialization(t *testing.T) {
	orig := &Suspended{
		messages: []llm.Message{
			{Role: "user", Content: "build an app"},
			{Role: "assistant", Content: "working on it", ToolCalls: []llm.ToolCall{
				{ID: "c1", Function: llm.FunctionCall{Name: "write_file", Arguments: `{"path":"app.py"}`}},
			}},
		},
		lay: layout{histFrom: 0, histTo: 1, input: 0},
		plan: []PlanItem{
			{Text: "create app.py", Status: PlanDone},
			{Text: "test app.py", Status: PlanInProgress},
		},
		at:   time.Now().Add(-5 * time.Minute),
		size: 120,
	}

	data, err := orig.Bytes()
	if err != nil {
		t.Fatalf("orig.Bytes(): %v", err)
	}

	restored, err := SuspendedFromBytes(data)
	if err != nil {
		t.Fatalf("SuspendedFromBytes(): %v", err)
	}

	if restored == nil {
		t.Fatal("restored is nil")
	}
	if len(restored.Messages()) != len(orig.Messages()) {
		t.Fatalf("messages len = %d, want %d", len(restored.Messages()), len(orig.Messages()))
	}
	if len(restored.Plan()) != len(orig.Plan()) {
		t.Fatalf("plan len = %d, want %d", len(restored.Plan()), len(orig.Plan()))
	}
	if restored.Plan()[0].Text != "create app.py" || restored.Plan()[0].Status != PlanDone {
		t.Errorf("plan[0] = %+v", restored.Plan()[0])
	}
	if restored.Plan()[1].Status != PlanInProgress {
		t.Errorf("plan[1] = %+v", restored.Plan()[1])
	}
	if restored.lay.input != orig.lay.input {
		t.Errorf("lay.input = %d, want %d", restored.lay.input, orig.lay.input)
	}
}
