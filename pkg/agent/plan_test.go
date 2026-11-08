package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestParsePlan(t *testing.T) {
	items, err := parsePlan(map[string]any{"items": []any{
		map[string]any{"text": "  دانلود ویدیو  ", "status": "completed"},
		map[string]any{"step": "تبدیل به MP3", "status": "in progress"},
		map[string]any{"text": "دومی که هم‌زمان", "status": "doing"},
		"ارسال",
		map[string]any{"text": ""},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []PlanItem{{"دانلود ویدیو", PlanDone}, {"تبدیل به MP3", PlanInProgress}, {"دومی که هم‌زمان", PlanPending}, {"ارسال", PlanPending}}
	if len(items) != len(want) {
		t.Fatalf("items = %+v", items)
	}
	for i := range want {
		if items[i] != want[i] {
			t.Fatalf("item %d = %+v, want %+v", i, items[i], want[i])
		}
	}
	if _, err := parsePlan(map[string]any{}); err == nil {
		t.Fatal("an empty plan was taken")
	}
	var many []any
	for i := 0; i < 30; i++ {
		many = append(many, strings.Repeat("x", 300))
	}
	items, _ = parsePlan(map[string]any{"items": many})
	if len(items) != maxPlanItems || len([]rune(items[0].Text)) > maxPlanRunes+1 {
		t.Fatalf("plan not bounded: %d items, %d runes", len(items), len([]rune(items[0].Text)))
	}
}

// TestThePlanIsShownAndKept: the plan reaches the listener, the result, and
// the log of a job that stops early (so «continue» knows what is left).
func TestThePlanIsShownAndKept(t *testing.T) {
	plan := `{"items":[{"text":"Write the file","status":"in_progress"},{"text":"Check it","status":"pending"}]}`
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{
			toolCall("p1", ToolPlan, plan),
			toolCall("w1", "write_file", `{"filename":"a.txt","content":"hi"}`),
		}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("l1", "list_files", ``)}},
		map[string]any{"role": "assistant", "content": "Part done; the check is left."},
	)
	var shown [][]PlanItem
	res, err := a.Do(context.Background(), Request{SessionID: "plan", Input: llm.Message{Role: "user", Content: "write a file and check it"},
		MaxSteps: 2, Listener: &EventListener{OnPlan: func(items []PlanItem) { shown = append(shown, items) }}})
	if err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[0][0].Status != PlanInProgress {
		t.Fatalf("the listener saw %+v", shown)
	}
	if len(res.Plan) != 2 || res.Plan[1].Text != "Check it" {
		t.Fatalf("result plan = %+v", res.Plan)
	}
	if !res.Partial || !strings.HasPrefix(res.WorkLog, "Plan:\n[>] Write the file\n[ ] Check it") || strings.Contains(res.WorkLog, ToolPlan+"(") {
		t.Fatalf("the work log of a job that stopped early: %q", res.WorkLog)
	}
	first := msgs(script.requests[0])
	if !strings.Contains(fmtTools(script.requests[0]), ToolPlan) || len(first) == 0 {
		t.Fatal("the plan tool is not offered")
	}
}

// TestAStoppedJobReturnsItsWork: cancelled after a step, the job still
// reports what it did.
func TestAStoppedJobReturnsItsWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, _ := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("w1", "write_file", `{"filename":"a.txt","content":"hi"}`)}},
		map[string]any{"role": "assistant", "content": "never"},
	)
	res, err := a.Do(ctx, Request{SessionID: "stop", Input: llm.Message{Role: "user", Content: "write"}, MaxSteps: 5,
		Listener: &EventListener{OnToolEnd: func(string, string, string, time.Duration) { cancel() }}})
	if err == nil || res == nil || res.StopReason != "stopped" || !strings.Contains(res.WorkLog, "write_file") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

// lateReply hands over its second reply as the job is stopped: the HTTP
// client may still return a reply that arrives with the stop.
type lateReply struct {
	inner  llm.ChatClient
	cancel context.CancelFunc
	n      int
}

func (l *lateReply) Complete(ctx context.Context, req llm.Request) (*llm.Response, error) {
	l.n++
	resp, err := l.inner.Complete(context.WithoutCancel(ctx), req)
	if l.n == 2 {
		l.cancel()
	}
	return resp, err
}

// TestAReplyThatArrivesWithTheStopIsNotUsed: a stop is a stop, even when the
// model's reply comes in at the same moment.
func TestAReplyThatArrivesWithTheStopIsNotUsed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a, _ := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("w1", "write_file", `{"filename":"a.txt","content":"hi"}`)}},
		map[string]any{"role": "assistant", "content": "the late answer"},
	)
	a.LLM = &lateReply{inner: a.LLM, cancel: cancel}
	res, err := a.Do(ctx, Request{SessionID: "late", Input: llm.Message{Role: "user", Content: "write"}, MaxSteps: 5})
	if !errors.Is(err, context.Canceled) || res == nil || res.StopReason != "stopped" || strings.Contains(res.Answer, "late answer") {
		t.Fatalf("res = %+v, err = %v", res, err)
	}
}

func fmtTools(req map[string]any) string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		if fn, ok := t.(map[string]any)["function"].(map[string]any); ok {
			names = append(names, fn["name"].(string))
		}
	}
	return strings.Join(names, ",")
}

func TestShortRepliesAboutTheWorkAreNotSmallTalk(t *testing.T) {
	for _, s := range []string{"بد شد", "چطور؟", "چه؟", "خیلی بد شد"} {
		if IsSmallTalk(s) {
			t.Errorf("%q is about the work, not small talk", s)
		}
	}
	for _, s := range []string{"چه خبر؟", "سلام خوبی؟", "مرسی عالی بود"} {
		if !IsSmallTalk(s) {
			t.Errorf("%q is small talk", s)
		}
	}
}

// TestThePlanKeepsMoving: after a few turns of work without an update, the
// model is reminded to tick off what is done; a long job without a plan is
// reminded once to make one.
func TestThePlanKeepsMoving(t *testing.T) {
	plan, pp := &planState{}, &planPace{}
	work := []llm.ToolCall{toolCall("c", "list_files", `{}`)}
	var notes []string
	for i := 0; i < 6; i++ {
		if n := pp.after(work, plan); n != "" {
			notes = append(notes, n)
		}
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "This job is taking several steps") {
		t.Fatalf("a long job without a plan: %q", notes)
	}
	plan.set([]PlanItem{{"نصب SDK", PlanDone}, {"بیلد", PlanInProgress}, {"تست", PlanPending}})
	if n := pp.after([]llm.ToolCall{toolCall("p", ToolPlan, `{}`)}, plan); n != "" {
		t.Fatalf("an update resets the count: %q", n)
	}
	notes = nil
	for i := 0; i < 7; i++ {
		if n := pp.after(work, plan); n != "" {
			notes = append(notes, n)
		}
	}
	if len(notes) != 2 || !strings.Contains(notes[0], "1/3 done, in progress: «بیلد»") {
		t.Fatalf("reminders every %d turns: %q", planNudgeAfter, notes)
	}
	plan.set([]PlanItem{{"a", PlanDone}, {"b", PlanDone}})
	for i := 0; i < 5; i++ {
		if n := pp.after(work, plan); n != "" {
			t.Fatalf("a finished plan needs no reminder: %q", n)
		}
	}
}

// TestThePlanReminderReachesTheModel: the reminder rides along with the
// tool results of the turn.
func TestThePlanReminderReachesTheModel(t *testing.T) {
	steps := []any{map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{
		toolCall("p1", ToolPlan, `{"items":[{"text":"one","status":"in_progress"},{"text":"two","status":"pending"}]}`),
		toolCall("l0", "list_files", `{}`)}}}
	for i := 1; i <= planNudgeAfter; i++ {
		steps = append(steps, map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall(fmt.Sprint("l", i), "list_files", `{}`)}})
	}
	steps = append(steps, map[string]any{"role": "assistant", "content": "done"})
	a, script := setup(t, steps...)
	if _, err := a.Do(context.Background(), Request{SessionID: "pace", Input: llm.Message{Role: "user", Content: "go"}, MaxSteps: 10}); err != nil {
		t.Fatal(err)
	}
	last := script.requests[len(script.requests)-1]
	b, _ := json.Marshal(last["messages"])
	if !strings.Contains(string(b), "Your plan was not updated for 3 turns (0/2 done") {
		t.Fatalf("no reminder in the tool results: %s", b)
	}
}

// TestAWeakModelsPlanIsRead: the states written into the step texts, list
// numbers, other key names and a plan sent as one text are all understood
// (the plan of the screenshot: «1. Locate … [done]» counted 0/4).
func TestAWeakModelsPlanIsRead(t *testing.T) {
	cases := []struct {
		args map[string]any
		want []PlanItem
	}{
		{map[string]any{"items": []any{
			"1. Locate and inspect uploader_bot on sev1 [done]",
			"2. Read and analyze bot.py (in progress)",
			"3. Refactor inline keyboards to ReplyKeyboardMarkup",
			"4. Verify syntax, restart bot - pending",
		}}, []PlanItem{{"Locate and inspect uploader_bot on sev1", PlanDone}, {"Read and analyze bot.py", PlanInProgress},
			{"Refactor inline keyboards to ReplyKeyboardMarkup", PlanPending}, {"Verify syntax, restart bot", PlanPending}}},
		{map[string]any{"steps": []any{
			map[string]any{"title": "✅ دانلود ویدیو"},
			map[string]any{"content": "- [x] تبدیل به MP3"},
			map[string]any{"task": "⏳ ارسال", "done": false},
			map[string]any{"description": "▫️ گزارش"},
		}}, []PlanItem{{"دانلود ویدیو", PlanDone}, {"تبدیل به MP3", PlanDone}, {"ارسال", PlanInProgress}, {"گزارش", PlanPending}}},
		{map[string]any{"plan": "1) Back up the config\n2) Edit nginx.conf [در حال انجام]\n3) Reload nginx"},
			[]PlanItem{{"Back up the config", PlanDone}, {"Edit nginx.conf", PlanInProgress}, {"Reload nginx", PlanPending}}},
		{map[string]any{"items": []any{map[string]any{"text": "Write 2.5 GB file", "completed": true}, map[string]any{"text": "Upload", "state": "running"}}},
			[]PlanItem{{"Write 2.5 GB file", PlanDone}, {"Upload", PlanInProgress}}},
	}
	for i, c := range cases {
		items, err := parsePlan(c.args)
		if err != nil {
			t.Fatalf("case %d: %v", i, err)
		}
		if fmt.Sprint(items) != fmt.Sprint(c.want) {
			t.Errorf("case %d:\n got %v\nwant %v", i, items, c.want)
		}
	}
	// The model hears that it wrote the states in the texts.
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("p1", ToolPlan, `{"items":["1. Read [done]","2. Write"]}`)}},
		map[string]any{"role": "assistant", "content": "ok"},
	)
	if _, err := a.Do(context.Background(), Request{SessionID: "plan-marks", Input: llm.Message{Role: "user", Content: "go"}}); err != nil {
		t.Fatal(err)
	}
	script.mu.Lock()
	last := script.requests[len(script.requests)-1]
	script.mu.Unlock()
	raw, _ := json.Marshal(last["messages"])
	if got := string(raw); !strings.Contains(got, "1/2 done") || !strings.Contains(got, "put them in status") {
		t.Fatalf("the model was not told: %s", got)
	}
}
