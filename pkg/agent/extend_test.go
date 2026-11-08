package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// With no limit, a job that makes progress every round is never cut by the
// step budget: 120 rounds on a budget of 5.
func TestExtendForeverHasNoRoundCap(t *testing.T) {
	const rounds = 120
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n < rounds {
			return 200, answer(callsTo("list_files", fmt.Sprintf(`{"path":"p%d"}`, n)), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "all done"}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "huge job"}, MaxSteps: 5, Extensions: ExtendForever})
	if err != nil || res.Partial || res.Answer != "all done" {
		t.Fatalf("%+v %v", res, err)
	}
	if res.Steps != rounds+1 || res.Extended < (rounds-5)/25 {
		t.Errorf("steps %d, extended %d times", res.Steps, res.Extended)
	}
	// No wrap-up notice while the budget can still grow.
	for i, r := range f.all() {
		if strings.Contains(llmBody(r), "tool rounds are left") {
			t.Fatalf("request %d was told to wrap up while the budget grows", i)
		}
	}
}

// A fixed budget (Extensions 0) still ends where it did: a short job takes
// one round as before, a long one stops at its budget.
func TestFixedBudgetIsUnchanged(t *testing.T) {
	a, _ := newBudgetAgent(t, func(n int) (int, any) {
		return 200, answer(map[string]any{"role": "assistant", "content": "hi"}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "hello"}, MaxSteps: 5, Extensions: ExtendForever})
	if err != nil || res.Steps != 1 || res.Partial || res.Extended != 0 {
		t.Fatalf("a short job is one round: %+v %v", res, err)
	}
	a, _ = newBudgetAgent(t, func(n int) (int, any) {
		if n < 50 {
			return 200, answer(callsTo("list_files", fmt.Sprintf(`{"path":"p%d"}`, n)), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "report"}, "stop")
	})
	res, err = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "big job"}, MaxSteps: 5})
	if err != nil || !res.Partial || res.StopReason != "steps" || res.Steps != 5 || res.Extended != 0 {
		t.Fatalf("a fixed budget stops: %+v %v", res, err)
	}
}

// The same call with the same arguments is not progress: the model is told
// once, and when it goes on repeating, the job stops with a clear reason long
// before the budget would.
func TestRepeatingTheSameCallStops(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if len(f0(n)) > 0 {
			return 200, answer(map[string]any{"role": "assistant", "content": "report"}, "stop")
		}
		return 200, answer(callsTo("list_files", `{"path":"same"}`), "tool_calls")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "loop"}, MaxSteps: 5, Extensions: ExtendForever, Lang: "en"})
	if err != nil || !res.Partial || res.StopReason != "repeat" {
		t.Fatalf("%+v %v", res, err)
	}
	// 2 allowed, the 3rd is told it repeats, the 4th stops it.
	if res.Steps != repeatLimit+2 || res.Extended != 0 {
		t.Errorf("stopped after %d steps (%d extensions)", res.Steps, res.Extended)
	}
	_ = f
}

// f0 is never true: the repeating model never stops by itself.
func f0(int) string { return "" }

// A run where nothing happens (the model does not answer) stops at the idle
// timeout with that reason, keeping the work done before.
func TestIdleTimeoutStopsAStalledJob(t *testing.T) {
	a, _ := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(callsTo("list_files", `{"path":"a"}`), "tool_calls")
		}
		time.Sleep(2 * time.Second) // the model stalls
		return 200, answer(map[string]any{"role": "assistant", "content": "late"}, "stop")
	})
	start := time.Now()
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "job"}, MaxSteps: 5, Extensions: ExtendForever,
		IdleTimeout: 300 * time.Millisecond, Lang: "en"})
	if err != nil || !res.Partial || res.StopReason != "idle" || !strings.Contains(res.Answer, "nothing happened") {
		t.Fatalf("%+v %v", res, err)
	}
	if took := time.Since(start); took > 1500*time.Millisecond {
		t.Errorf("stopped after %s", took)
	}
}

// No progress in the last round (every call failed): the budget does not
// grow, and the model was warned before the end.
func TestNoExtensionWithoutProgress(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n < 50 {
			return 200, answer(callsTo("read_file", fmt.Sprintf(`{"path":"missing-%d.txt"}`, n)), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "report"}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "job"}, MaxSteps: 5, Extensions: ExtendForever})
	if err != nil || !res.Partial || res.StopReason != "steps" || res.Extended != 0 {
		t.Fatalf("failing calls are not progress: %+v %v", res, err)
	}
	_ = f
}

// A sub-agent's budget is its own: 50 rounds by default.
func TestSubAgentsHaveTheirOwnBudget(t *testing.T) {
	if delegateSteps != 50 {
		t.Fatalf("delegateSteps %d", delegateSteps)
	}
}
