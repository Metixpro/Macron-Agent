package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

type fakeModel struct {
	mu       sync.Mutex
	requests []map[string]any
	reply    func(n int) (int, any)
}

func (f *fakeModel) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	n := len(f.requests)
	f.requests = append(f.requests, req)
	f.mu.Unlock()
	status, body := f.reply(n)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (f *fakeModel) all() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.requests...)
}

func answer(msg any, finish string) map[string]any {
	return map[string]any{"choices": []any{map[string]any{"message": msg, "finish_reason": finish}}}
}

func callsTo(name, args string) map[string]any {
	return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c", "type": "function",
		"function": map[string]any{"name": name, "arguments": args}}}}
}

func newBudgetAgent(t *testing.T, reply func(n int) (int, any)) (*Agent, *fakeModel) {
	t.Helper()
	f := &fakeModel{reply: reply}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	client := llm.NewClient(srv.URL, "k", "m")
	client.MaxRetries = 0
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(sb, search.NewEngine(nil), client, tools.Options{HTTP: netclient.NewClient(netclient.Options{})})
	return NewAgent(client, reg, Options{MaxSteps: 10, Channel: ChannelTelegram}), f
}

func lastContent(req map[string]any) string {
	m := msgs(req)
	c, _ := m[len(m)-1]["content"].(string)
	return c
}

func TestBudgetIsAnnouncedAndTheEndIsReported(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n < 5 {
			return 200, answer(callsTo("list_files", `{}`), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "Did half; say continue."}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "big job"}, MaxSteps: 5})
	if err != nil || !res.Partial || res.Answer != "Did half; say continue." {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := f.all()
	first := msgs(reqs[0])
	if sys, now := first[0]["content"].(string), first[len(first)-2]["content"].(string); !strings.Contains(now, "up to 5 tool rounds") ||
		!strings.Contains(sys, "Big and long jobs") {
		t.Error("the model must know its budget (right before the message)")
	}
	if !strings.Contains(lastContent(reqs[3]), "Only 2 tool rounds are left") {
		t.Errorf("no warning before the end: %q", lastContent(reqs[3]))
	}
	if !strings.Contains(lastContent(reqs[5]), "Stop working now") {
		t.Errorf("wrap-up: %q", lastContent(reqs[5]))
	}
	if strings.Count(res.WorkLog, "list_files") != 5 {
		t.Errorf("work log:\n%s", res.WorkLog)
	}

	if m := msgs(reqs[3]); !strings.Contains(m[len(m)-2]["content"].(string), "already made this exact call 2 times") {
		t.Errorf("repetition notice missing: %v", m[len(m)-2])
	}
}

func TestTimeRunningOutEndsWithAReport(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(callsTo("wait", `{}`), "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "Here's what I did."}, "stop")
	})
	wait := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "wait", Parameters: map[string]any{"type": "object"}}},
		Handler: func(ctx context.Context, _ map[string]any) (ToolOutput, error) {
			time.Sleep(1500 * time.Millisecond)
			return ToolOutput{Text: "waited"}, nil
		}}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	res, err := a.Do(ctx, Request{SessionID: "s", Input: llm.Message{Content: "x"}, Tools: []LocalTool{wait}})
	if err != nil || !res.Partial || res.Answer != "Here's what I did." {
		t.Fatalf("%+v %v", res, err)
	}
	if reqs := f.all(); len(reqs) != 2 || !strings.Contains(lastContent(reqs[1]), "Stop working now") {
		t.Errorf("with little time left the model reports instead of working on: %d requests", len(reqs))
	}
}

func TestTheModelFailingMidJobKeepsTheProgress(t *testing.T) {
	old := midRunBackoff
	midRunBackoff = time.Millisecond
	t.Cleanup(func() { midRunBackoff = old })
	var calls atomic.Int32
	a, _ := newBudgetAgent(t, func(n int) (int, any) {
		calls.Add(1)
		if n == 0 {
			return 200, answer(callsTo("list_files", `{}`), "tool_calls")
		}
		return 503, map[string]any{"error": map[string]any{"message": "overloaded"}}
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}, Lang: "en"})
	if err != nil || !res.Partial || !strings.Contains(res.Answer, "Say «continue»") || !strings.Contains(res.WorkLog, "list_files") {
		t.Fatalf("%+v %v", res, err)
	}
	if calls.Load() != 2+midRunRetries {
		t.Errorf("mid-job failures are retried: %d calls", calls.Load())
	}

	var first atomic.Int32
	b, _ := newBudgetAgent(t, func(int) (int, any) {
		first.Add(1)
		return 503, map[string]any{"error": map[string]any{"message": "down"}}
	})
	if _, err := b.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}}); err == nil || first.Load() != 2 {
		t.Errorf("expected an error after one retry: %v, %d calls", err, first.Load())
	}

	c, _ := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 503, map[string]any{"error": map[string]any{"message": "busy"}}
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "hello"}, "stop")
	})
	if res, err := c.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}}); err != nil || res.Answer != "hello" {
		t.Errorf("the first answer is retried once: %+v %v", res, err)
	}
}

func TestCutAnswersAreContinued(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		switch n {
		case 0:
			return 200, answer(map[string]any{"role": "assistant", "content": "part one, "}, "length")
		case 1:
			return 200, answer(map[string]any{"role": "assistant", "content": "part two, "}, "length")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "the end."}, "stop")
	})
	var streamed string
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"},
		Listener: &EventListener{OnText: func(s string) { streamed = s }}})
	if err != nil || res.Answer != "part one, part two, the end." {
		t.Fatalf("%+v %v", res, err)
	}
	if reqs := f.all(); len(reqs) != 3 || !strings.Contains(lastContent(reqs[1]), "cut off by the output length limit") {
		t.Errorf("continuation requests: %d", len(reqs))
	}
	_ = streamed
}

func TestTooLongConversationsAreShortened(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 400, map[string]any{"error": map[string]any{"message": "This model's maximum context length is 128000 tokens"}}
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "fits now"}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}})
	if err != nil || res.Answer != "fits now" || len(f.all()) != 2 {
		t.Fatalf("%+v %v %d", res, err, len(f.all()))
	}
}

func TestReadOnlyToolsRunAtTheSameTime(t *testing.T) {
	a, _ := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(map[string]any{"role": "assistant", "tool_calls": []any{
				map[string]any{"id": "a", "type": "function", "function": map[string]any{"name": "web_search", "arguments": `{"query":"a"}`}},
				map[string]any{"id": "b", "type": "function", "function": map[string]any{"name": "web_fetch", "arguments": `{"url":"b"}`}},
				map[string]any{"id": "c", "type": "function", "function": map[string]any{"name": "web_search", "arguments": `{"query":"c"}`}},
			}}, "tool_calls")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "done"}, "stop")
	})
	slow := func(name string) LocalTool {
		return LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: name, Parameters: map[string]any{"type": "object"}}},
			Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
				time.Sleep(400 * time.Millisecond)
				return ToolOutput{Text: name + " " + args["query"].(string) + args["url"].(string)}, nil
			}}
	}
	start := time.Now()
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"},
		Tools: []LocalTool{slow("web_search"), slow("web_fetch")}})
	if err != nil || res.Answer != "done" {
		t.Fatalf("%+v %v", res, err)
	}
	if took := time.Since(start); took > 1100*time.Millisecond {
		t.Errorf("three reads took %s: they did not run at the same time", took)
	}
}

func TestStoppedTextUsesTheLanguagesDigits(t *testing.T) {
	if s := stoppedText("fa", 12, stopTime, false); !strings.Contains(s, "۱۲ مرحله") || !strings.Contains(s, "وقت این درخواست تموم شد") {
		t.Errorf("fa: %s", s)
	}
	if s := stoppedText("en", 12, stopFailed, false); !strings.Contains(s, "12 steps") || !strings.Contains(s, "connection") {
		t.Errorf("en: %s", s)
	}
	if s := stoppedText("fa", 3, stopSteps, false); !strings.Contains(s, "تعداد قدم‌های مجاز") {
		t.Errorf("steps: %s", s)
	}
}

func TestTheProjectPlaybookIsInThePrompt(t *testing.T) {
	a, _ := newBudgetAgent(t, func(int) (int, any) { return 200, answer(map[string]any{"role": "assistant", "content": "x"}, "stop") })
	p := a.SystemPrompt()
	for _, want := range []string{"Working on code projects", "project_tree", "search_code", "edit_file", "import_repo", "pack_files", "go mod download"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt misses %q", want)
		}
	}
}

func TestMessagesDuringAJobReachTheModel(t *testing.T) {
	var mu sync.Mutex
	var pending []llm.Message
	push := func(text string) {
		mu.Lock()
		pending = append(pending, llm.Message{Role: "user", Content: InterjectionNote + text})
		mu.Unlock()
	}
	inbox := func() []llm.Message {
		mu.Lock()
		defer mu.Unlock()
		out := pending
		pending = nil
		return out
	}
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		switch n {
		case 0:
			push("also add a chart")
			return 200, answer(callsTo("list_files", `{}`), "tool_calls")
		case 1:
			push("and make it short")
			return 200, answer(map[string]any{"role": "assistant", "content": "Here is the report."}, "stop")
		}
		return 200, answer(map[string]any{"role": "assistant", "content": "Short report with a chart."}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "make a report"}, Inbox: inbox})
	if err != nil || res.Answer != "Short report with a chart." {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := f.all()
	if len(reqs) != 3 || !strings.Contains(llmBody(reqs[1]), "also add a chart") || !strings.Contains(llmBody(reqs[2]), "and make it short") ||
		!strings.Contains(llmBody(reqs[2]), "Here is the report.") || !strings.Contains(llmBody(reqs[2]), "was not delivered") {
		t.Errorf("messages during the job: %d requests", len(reqs))
	}
}

func TestAutoContinueReportDoesNotAskToContinue(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n < 5 {
			return 200, answer(callsTo("list_files", fmt.Sprintf(`{"path":"p%d"}`, n)), "tool_calls")
		}
		return 500, map[string]any{"error": "down"}
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "big job"}, MaxSteps: 5, AutoContinue: true, Lang: "en"})
	if err != nil || !res.Partial || res.StopReason != "steps" {
		t.Fatalf("%+v %v", res, err)
	}
	if strings.Contains(res.Answer, "continue") {
		t.Errorf("the bot goes on by itself: %s", res.Answer)
	}
	if body := llmBody(f.all()[5]); !strings.Contains(body, "goes on automatically") {
		t.Errorf("the model is told the job goes on: %s", body)
	}
}

func llmBody(req map[string]any) string {
	b, _ := json.Marshal(req["messages"])
	return string(b)
}

func TestFastModeAnswersAtTheLastStep(t *testing.T) {
	a, f := newBudgetAgent(t, func(n int) (int, any) {
		if n == 0 {
			return 200, answer(callsTo("list_files", `{}`), "tool_calls")
		}

		return 200, answer(map[string]any{"role": "assistant", "content": "Here it is."}, "stop")
	})
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "quick"}, MaxSteps: 2,
		AnswerOnLastStep: true, ToolFilter: func(name string) bool { return name == "list_files" }})
	if err != nil || res.Partial || res.Answer != "Here it is." {
		t.Fatalf("%+v %v", res, err)
	}
	reqs := f.all()
	if len(reqs) != 2 {
		t.Fatalf("two steps: %d", len(reqs))
	}
	var names []string
	for _, tl := range reqs[0]["tools"].([]any) {
		names = append(names, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "list_files" {
		t.Errorf("only the allowed tools are offered: %v", names)
	}
	if reqs[1]["tools"] != nil || !strings.Contains(llmBody(reqs[1]), "without calling any tools") {
		t.Errorf("the last step offers no tools and asks for the answer: %v", reqs[1]["tools"])
	}
}

func TestTheUsersPhotoOutlivesCompaction(t *testing.T) {
	img := func(text string) llm.Message {
		return llm.Message{Role: "user", Parts: []llm.Part{llm.TextPart(text), llm.ImagePart("data:image/png;base64,AA")}}
	}
	msgs := []llm.Message{{Role: "system", Content: "s"}, img("an old photo"), img("edit this photo"), {Role: "assistant", Content: "ok"},
		img("view_image result 1"), img("view_image result 2")}
	dropOldImages(msgs, 2)
	has := func(m llm.Message) bool {
		for _, p := range m.Parts {
			if p.Type == "image_url" {
				return true
			}
		}
		return false
	}
	if has(msgs[1]) || !has(msgs[2]) || has(msgs[4]) || !has(msgs[5]) {
		t.Errorf("kept: old %v, request %v, tool %v, last %v", has(msgs[1]), has(msgs[2]), has(msgs[4]), has(msgs[5]))
	}
}

func TestTheLearnedWindowMovesUp(t *testing.T) {
	m := &contextMeter{window: 256000}
	m.overflowed(200000)
	if m.window != 180000 || !m.fixed {
		t.Fatalf("learned: %d", m.window)
	}
	m.observe("model", 185000, nil)
	if m.window != 192500 {
		t.Errorf("a bigger prompt that went through moves it up between the two: %d", m.window)
	}
	m.observe("model", 199999, nil)
	if m.window >= 200000 {
		t.Errorf("never past what overflowed: %d", m.window)
	}
}

type providerLLM struct {
	calls int
	names []string
}

func (p *providerLLM) Complete(_ context.Context, req llm.Request) (*llm.Response, error) {
	p.calls++
	if len(req.Tools) > 0 && p.calls == 1 {
		return nil, &llm.APIError{Status: 400, Body: "this model does not support tools"}
	}
	name := p.names[min(len(p.names)-1, p.calls-1)]
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: "ok"}, Provider: name}, nil
}

func TestNativeToolsComeBackWithAnotherProvider(t *testing.T) {
	p := &providerLLM{names: []string{"", "relay", "relay", "openai"}}
	a := &Agent{LLM: p}
	mode := &textTools{}
	tools := []llm.Tool{{Type: "function", Function: llm.ToolDef{Name: "x"}}}
	_, _ = a.complete(context.Background(), nil, tools, nil, mode)
	if !mode.on || mode.by != "relay" {
		t.Fatalf("text calls after a refusal: %+v", mode)
	}
	_, _ = a.complete(context.Background(), nil, tools, nil, mode)
	if !mode.on {
		t.Error("the same provider keeps text calls")
	}
	_, _ = a.complete(context.Background(), nil, tools, nil, mode)
	if mode.on {
		t.Error("another provider answered: native tools again")
	}
}
