package agent

import (
	"context"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/tools"
	"testing"
)

type discoveryClient struct {
	t      *testing.T
	step   int
	legacy bool
	loaded bool
	counts []int
}

func (c *discoveryClient) Complete(_ context.Context, r llm.Request) (*llm.Response, error) {
	c.t.Helper()
	c.counts = append(c.counts, len(r.Tools))
	names := map[string]bool{}
	for _, d := range r.Tools {
		names[d.Function.Name] = true
	}
	if names[ToolMoreTools] {
		c.t.Fatal("bulk-loading tool offered alongside tool search")
	}
	for i := 0; i < 7; i++ {
		if names[fmt.Sprintf("qa_capability_%d", i)] {
			c.t.Fatal("unrelated schema was loaded")
		}
	}
	if c.loaded && !names["qa_capability_7"] {
		c.t.Fatal("searched tool was not offered")
	}
	call := func(n, args string) *llm.Response {
		return &llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: fmt.Sprint(c.step), Type: "function", Function: llm.FunctionCall{Name: n, Arguments: args}}}}}
	}
	c.step++
	if c.legacy && c.step == 1 {
		return call(ToolMoreTools, `{"need":"all tools"}`), nil
	}
	offset := 0
	if c.legacy {
		offset = 1
	}
	switch c.step - offset {
	case 1:
		c.loaded = true
		return call(ToolSearch, `{"query":"qa_capability_7"}`), nil
	case 2:
		return call("qa_capability_7", `{}`), nil
	default:
		return &llm.Response{Message: llm.Message{Role: "assistant", Content: "Result QA_VALUE_141"}}, nil
	}
}
func TestSearchLoadsOnlyRequestedSchemaWithoutBulkEscape(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			m, err := machine.New(machine.Config{BaseDir: t.TempDir(), Sandbox: "off"})
			if err != nil {
				t.Fatal(err)
			}
			c := &discoveryClient{t: t, legacy: legacy}
			reg := tools.NewRegistry(m, nil, c, tools.Options{})
			calls := 0
			for i := 0; i < 8; i++ {
				name := fmt.Sprintf("qa_capability_%d", i)
				reg.Register(llm.Tool{Type: "function", Function: llm.ToolDef{Name: name, Description: "Special local test capability", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}}, func(context.Context, string, map[string]any) (string, error) { calls++; return "QA_VALUE_141", nil })
			}
			a := NewAgent(c, reg, Options{MaxSteps: 8, Channel: ChannelCLI})
			res, err := a.Do(context.Background(), Request{SessionID: "discovery", Input: llm.Message{Content: "Inspect the available capabilities and return the requested result."}, FocusTools: true, EfficientTools: true, Owner: true})
			if err != nil || res == nil || calls != 1 {
				t.Fatalf("calls=%d result=%v err=%v", calls, res, err)
			}
			t.Logf("schemas per model request: %v", c.counts)
		})
	}
}
