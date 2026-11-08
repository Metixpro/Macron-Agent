package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/skills"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

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

	for i, r := range f.all() {
		if strings.Contains(llmBody(r), "tool rounds are left") {
			t.Fatalf("request %d was told to wrap up while the budget grows", i)
		}
	}
}

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

	if res.Steps != repeatLimit+2 || res.Extended != 0 {
		t.Errorf("stopped after %d steps (%d extensions)", res.Steps, res.Extended)
	}
	_ = f
}

func f0(int) string { return "" }

func TestIdleTimeoutStopsAStalledJob(t *testing.T) {
	a, _ := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(callsTo("list_files", `{"path":"a"}`), "tool_calls")
		}
		time.Sleep(2 * time.Second)
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

func TestSubAgentsHaveTheirOwnBudget(t *testing.T) {
	if delegateSteps != 50 {
		t.Fatalf("delegateSteps %d", delegateSteps)
	}
}

func TestSkillNudge(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n < 7 {
			return 200, answer(callsTo("list_files", fmt.Sprintf(`{"path":"p%d"}`, n)), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "done"}, "stop")
	})
	st, err := skills.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	client := a.LLM.(*llm.Client)
	a.Registry = tools.NewRegistry(sb, search.NewEngine(nil), client, tools.Options{HTTP: netclient.NewClient(netclient.Options{}),
		Skills: st, SkillWrite: func(string) bool { return true }})
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "job"}, MaxSteps: 20, SkillNudgeEvery: 3}); err != nil {
		t.Fatal(err)
	}
	reqs := f.all()
	count := func(i int) int { return strings.Count(llmBody(reqs[i]), "save how you did it with save_skill") }
	if len(reqs) < 8 || count(2) != 0 || count(3) != 1 || count(6) != 2 {
		t.Fatalf("reminders at rounds 4 and 7: %d requests, %d %d %d", len(reqs), count(2), count(3), count(6))
	}
}
