package moa

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// fakeModels answers each slot: advisors with their name, the aggregator
// with "done"; fail lists providers that fail.
type fakeModels struct {
	mu    sync.Mutex
	calls map[string][]llm.Request
	fail  map[string]error
	once  map[string]error // fails the first call only
}

func (f *fakeModels) call(_ context.Context, s Slot, req llm.Request) (*llm.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.calls == nil {
		f.calls = map[string][]llm.Request{}
	}
	f.calls[s.Provider] = append(f.calls[s.Provider], req)
	if err := f.once[s.Provider]; err != nil {
		delete(f.once, s.Provider)
		return nil, err
	}
	if err := f.fail[s.Provider]; err != nil {
		return nil, err
	}
	return &llm.Response{Message: llm.Message{Role: "assistant", Content: s.Provider + " thinks so"}}, nil
}

func (f *fakeModels) n(p string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls[p])
}

func preset(fanout string) Preset {
	return Preset{Name: "duo", Aggregator: Slot{Provider: "opus"}, References: []Slot{{Provider: "gpt"}}, Fanout: fanout}
}

var conversation = []llm.Message{{Role: "system", Content: "you are an agent"}, {Role: "user", Content: "solve the hard task"}}

// rounds runs n rounds of a job: each round adds a tool call and its result.
func rounds(c *Client, n int) {
	msgs := append([]llm.Message(nil), conversation...)
	for i := 0; i < n; i++ {
		_, _ = c.Complete(context.Background(), llm.Request{Messages: msgs})
		msgs = append(msgs, llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{{ID: fmt.Sprint(i), Function: llm.FunctionCall{Name: "ls"}}}},
			llm.Message{Role: "tool", ToolCallID: fmt.Sprint(i), Content: "out"})
	}
}

func TestFanoutCadence(t *testing.T) {
	for _, c := range []struct {
		fanout string
		want   int
	}{{FanoutUserTurn, 1}, {"", 1}, {"bogus", 1}, {FanoutPerIteration, 7}, {"every_n:3", 3}, {"every_n:1", 7}} {
		f := &fakeModels{}
		cl := &Client{Preset: preset(c.fanout), Call: f.call}
		rounds(cl, 7)
		if f.n("gpt") != c.want || cl.Fanouts != c.want || f.n("opus") != 7 {
			t.Errorf("%q: advisors %d, aggregator %d, want %d", c.fanout, f.n("gpt"), f.n("opus"), c.want)
		}
	}
	if (Preset{Fanout: "every_n:0"}).FanoutName() != FanoutUserTurn || (Preset{Fanout: "every_n:3"}).FanoutName() != "every_n:3" {
		t.Error("names")
	}
}

func TestDisabledIsTheAggregatorAlone(t *testing.T) {
	f := &fakeModels{}
	off := false
	p := preset(FanoutPerIteration)
	p.Enabled = &off
	rounds(&Client{Preset: p, Call: f.call}, 4)
	if f.n("gpt") != 0 || f.n("opus") != 4 {
		t.Fatalf("advisors %d", f.n("gpt"))
	}
	if strings.Contains(fmt.Sprint(f.calls["opus"][0].Messages), "reference") {
		t.Error("no reference block")
	}
}

// The advisors see the conversation's text only; their views are a separate
// last user message, the aggregator's prefix stays as it was.
func TestAdvisorsAndTheBlock(t *testing.T) {
	f := &fakeModels{}
	cl := &Client{Preset: preset(FanoutUserTurn), Call: f.call}
	req := llm.Request{Messages: conversation, Tools: []llm.Tool{{Type: "function"}}}
	if _, err := cl.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	adv := f.calls["gpt"][0]
	if len(adv.Tools) != 0 || len(adv.Messages) != 1 || adv.Messages[0].Role != "user" || adv.Messages[0].Content != "solve the hard task" {
		t.Fatalf("an advisor sees no system prompt and no tools: %+v", adv)
	}
	agg := f.calls["opus"][0]
	if len(agg.Tools) != 1 || len(agg.Messages) != 3 || agg.Messages[0].Content != conversation[0].Content || agg.Messages[1].Content != conversation[1].Content {
		t.Fatalf("the aggregator's prefix is untouched: %+v", agg.Messages)
	}
	last := agg.Messages[2]
	if last.Role != "user" || !strings.Contains(last.Content, `<reference model="gpt">`) || !strings.Contains(last.Content, "gpt thinks so") ||
		!strings.Contains(last.Content, "private context") {
		t.Fatalf("the block: %+v", last)
	}
	if len(conversation) != 2 {
		t.Fatal("the caller's messages were changed")
	}
	// The view is a function of the stable history: the same again.
	if a, b := fmt.Sprint(View(conversation)), fmt.Sprint(View(append(conversation, llm.Message{Role: "tool", Content: "x"}))); a != b {
		t.Errorf("tool results change the view:\n%s\n%s", a, b)
	}
}

// An advisor whose key fails does not fail the turn: its error is in the block.
func TestAFailingAdvisor(t *testing.T) {
	f := &fakeModels{fail: map[string]error{"gpt": errors.New("401 invalid api key")}}
	p := preset(FanoutUserTurn)
	p.References = append(p.References, Slot{Provider: "gemini"})
	cl := &Client{Preset: p, Call: f.call}
	var seen []Advice
	cl.OnAdvice = func(a []Advice) { seen = a }
	resp, err := cl.Complete(context.Background(), llm.Request{Messages: conversation})
	if err != nil || resp.Message.Content != "opus thinks so" {
		t.Fatalf("the turn completes: %v %v", resp, err)
	}
	block := f.calls["opus"][0].Messages[2].Content
	if !strings.Contains(block, `<reference model="gpt" error="401 invalid api key"/>`) || !strings.Contains(block, "gemini thinks so") {
		t.Fatalf("the block: %s", block)
	}
	if len(seen) != 2 || seen[0].Err == nil || seen[1].Text == "" {
		t.Fatalf("the trace: %+v", seen)
	}
}

// A template that insists on alternating roles: the failed request is sent
// once more merged, and from then on that destination gets it merged at once.
func TestAlternationRecovery(t *testing.T) {
	strict := &Strict{}
	alternate := &llm.APIError{Status: 400, Body: "Conversation roles must alternate user/assistant/user/assistant/..."}
	f := &fakeModels{once: map[string]error{"opus": alternate}}
	dest := func(s Slot) string { return "http://llama.local/v1|" + s.Provider }
	cl := &Client{Preset: preset(FanoutUserTurn), Call: f.call, Strict: strict, Dest: dest}
	if _, err := cl.Complete(context.Background(), llm.Request{Messages: conversation}); err != nil {
		t.Fatal(err)
	}
	if f.n("opus") != 2 {
		t.Fatalf("one retry: %d calls", f.n("opus"))
	}
	if retry := f.calls["opus"][1].Messages; len(retry) != 2 || !strings.Contains(retry[1].Content, "solve the hard task") || !strings.Contains(retry[1].Content, "reference") {
		t.Fatalf("the retry merges the user messages: %+v", retry)
	}
	// The next request of the session goes merged at once.
	cl2 := &Client{Preset: preset(FanoutUserTurn), Call: f.call, Strict: strict, Dest: dest}
	if _, err := cl2.Complete(context.Background(), llm.Request{Messages: conversation}); err != nil || f.n("opus") != 3 || len(f.calls["opus"][2].Messages) != 2 {
		t.Fatalf("pre-merged: %d calls, %v", f.n("opus"), err)
	}
	// Another destination keeps the cacheable shape.
	other := &Client{Preset: Preset{Name: "x", Aggregator: Slot{Provider: "claude"}, References: []Slot{{Provider: "gpt"}}}, Call: f.call, Strict: strict, Dest: dest}
	_, _ = other.Complete(context.Background(), llm.Request{Messages: conversation})
	if len(f.calls["claude"][0].Messages) != 3 {
		t.Fatal("other destinations are left alone")
	}
	if Alternation(errors.New("timeout")) || !Alternation(alternate) {
		t.Error("Alternation")
	}
}

func TestScrubIsConservative(t *testing.T) {
	in := "mail me at ali.r@example.co.uk or call +98 912 345 6789 or 09123456789; see line 1234567890, " +
		"commit 3f5b9c2d1e4f6a7b8c9d0e1f, on 2026-11-08 12:30, host 192.168.10.200:8080, version v1.2.3-rc1, port 0912"
	out := Scrub(in)
	for _, gone := range []string{"ali.r@example.co.uk", "912 345 6789", "09123456789"} {
		if strings.Contains(out, gone) {
			t.Errorf("%q stays: %s", gone, out)
		}
	}
	for _, kept := range []string{"line 1234567890", "3f5b9c2d1e4f6a7b8c9d0e1f", "2026-11-08 12:30", "192.168.10.200:8080", "v1.2.3-rc1", "port 0912"} {
		if !strings.Contains(out, kept) {
			t.Errorf("%q was scrubbed: %s", kept, out)
		}
	}
}

func TestProblems(t *testing.T) {
	resolve := func(p string) (bool, bool, bool) {
		switch p {
		case "opus":
			return true, true, false
		case "nokey":
			return true, false, false
		case "mix":
			return true, true, true
		}
		return false, false, false
	}
	if p := Problems(preset(""), func(s string) (bool, bool, bool) {
		if s == "gpt" {
			return true, true, false
		}
		return resolve(s)
	}); len(p) != 0 {
		t.Errorf("a good preset: %v", p)
	}
	for name, p := range map[string]Preset{
		"missing reference": {Name: "a", Aggregator: Slot{Provider: "opus"}, References: []Slot{{Provider: "ghost"}}},
		"no key":            {Name: "b", Aggregator: Slot{Provider: "nokey"}},
		"recursive":         {Name: "c", Aggregator: Slot{Provider: "mix"}},
		"bad effort":        {Name: "d", Aggregator: Slot{Provider: "opus", Effort: "insane"}},
	} {
		if len(Problems(p, resolve)) == 0 {
			t.Errorf("%s: no problem found", name)
		}
	}
}
