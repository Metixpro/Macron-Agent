package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestDelegateRunsASubAgent(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("d1", ToolDelegate,
			`{"goal":"write a short note about Go into notes.md","context":"the user likes bullet points"}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("w1", "write_file", `{"filename":"notes.md","content":"- Go is fast"}`)}},
		llm.Message{Role: "assistant", Content: "Wrote notes.md with one bullet."},
		llm.Message{Role: "assistant", Content: "Done: notes.md is ready."},
	)
	a.MaxSteps = 6
	var started []string
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "make me notes"},
		Listener: &EventListener{OnToolStart: func(name, args string) { started = append(started, name) }}})
	if err != nil || res.Answer != "Done: notes.md is ready." {
		t.Fatalf("%q %v", res.Answer, err)
	}
	if len(script.requests) != 4 {
		t.Fatalf("parent, sub, sub, parent: %d requests", len(script.requests))
	}
	sub := script.requests[1]
	subMsgs := msgs(sub)
	if !strings.Contains(subMsgs[len(subMsgs)-1]["content"].(string), "write a short note about Go") ||
		!strings.Contains(subMsgs[len(subMsgs)-1]["content"].(string), "bullet points") {
		t.Fatalf("the sub-agent gets the goal and the context: %v", subMsgs[len(subMsgs)-1])
	}
	if offeredTools(sub)[ToolDelegate] || !offeredTools(script.requests[0])[ToolDelegate] {
		t.Fatal("only the main agent can delegate")
	}
	report := ""
	for _, m := range msgs(script.requests[3]) {
		if m["role"] == "tool" {
			report = m["content"].(string)
		}
	}
	if !strings.Contains(report, "Wrote notes.md with one bullet.") || !strings.Contains(report, "Files it handed over (now in the workspace): notes.md") {
		t.Fatalf("report: %q", report)
	}
	if data, _, err := a.Registry.Machine.ReadFile("s", "notes.md", 100); err != nil || string(data) != "- Go is fast" {
		t.Fatalf("its file is in the main workspace: %q %v", data, err)
	}
	if strings.Join(started, ",") != ToolDelegate+",write_file" {
		t.Fatalf("the sub-agent's tools show in the live view: %v", started)
	}
	if !strings.Contains(msgs(script.requests[0])[0]["content"].(string), "hand each part to "+ToolDelegate) {
		t.Fatal("the prompt explains delegation")
	}
}

func TestNoDelegationInFastMode(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "ok"})
	_, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}, ToolFilter: func(n string) bool { return n == "web_search" }})
	if offeredTools(script.requests[0])[ToolDelegate] || strings.Contains(msgs(script.requests[0])[0]["content"].(string), "hand each part") {
		t.Fatal("a filter without delegate_task hides it")
	}
}

func TestSubAgentsWorkAlongsideAndApart(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{
			toolCall("p1", "make_picture", `{}`),
			toolCall("d1", ToolDelegate, `{"title":"Music search & download","goal":"write song.txt"}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("w1", "write_file", `{"filename":"song.txt","content":"la la"}`)}},
		llm.Message{Role: "assistant", Content: "song.txt written"},
		llm.Message{Role: "assistant", Content: "Here are both."},
	)
	a.MaxSteps = 6
	subWorking := make(chan struct{})
	var once sync.Once
	picture := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "make_picture",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			select {
			case <-subWorking:
				_ = a.Registry.Machine.WriteFile("s", "picture.png", []byte("png"))
				return ToolOutput{Text: "picture made while the sub-agent worked"}, nil
			case <-time.After(5 * time.Second):
				return ToolOutput{Text: "the sub-agent never ran alongside"}, nil
			}
		}}
	var mu sync.Mutex
	var mainTools, subTools []string
	var title string
	doneSteps, mainContext := -1, 0
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "send a song and make a picture"},
		Tools: []LocalTool{picture},
		Listener: &EventListener{
			OnToolStart: func(name, _ string) { mu.Lock(); mainTools = append(mainTools, name); mu.Unlock() },
			OnContext:   func(used, window int) { mu.Lock(); mainContext++; mu.Unlock() },
			SubAgent: func(name, session string) *EventListener {
				title = name
				return &EventListener{
					OnToolStart: func(name, _ string) {
						mu.Lock()
						subTools = append(subTools, name)
						mu.Unlock()
						once.Do(func() { close(subWorking) })
					},
					OnContext: func(int, int) { t.Error("a sub-agent never reports its context") },
					OnDone:    func(steps int, failed bool) { doneSteps = steps },
				}
			},
		}})
	if err != nil || res.Answer != "Here are both." {
		t.Fatalf("%q %v", res.Answer, err)
	}
	last := fmt.Sprint(msgs(script.requests[len(script.requests)-1]))
	if !strings.Contains(last, "picture made while the sub-agent worked") || !strings.Contains(last, "song.txt written") {
		t.Fatalf("the sub-agent works alongside the agent's own tool: %s", last)
	}
	if !strings.Contains(last, "Files it handed over (now in the workspace): song.txt") || strings.Contains(last, "song.txt, picture.png") ||
		strings.Contains(last, "picture.png\\n") {
		t.Fatalf("a sub-agent claims only its own files: %s", last)
	}
	if title != "Music search & download" || doneSteps != 2 {
		t.Fatalf("title %q, steps %d", title, doneSteps)
	}
	if strings.Join(subTools, ",") != "write_file" || strings.Contains(strings.Join(mainTools, ","), "write_file") {
		t.Fatalf("the sub-agent's calls go to its own listener: main %v, sub %v", mainTools, subTools)
	}
	if mainContext != res.Steps {
		t.Fatalf("only the main agent moves the meter: %d reports for %d steps", mainContext, res.Steps)
	}
	if subTitle("", "find the song\nthen download it") != "find the song" {
		t.Fatal("without a title, the goal's first line names it")
	}
}

func TestSubAgentsHaveTheirOwnWorkspaces(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("d1", ToolDelegate,
			`{"goal":"turn photo.jpg into a poster","toolsets":["images"]}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("l1", "list_files", `{}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{
			toolCall("w1", "write_file", `{"filename":"poster.png","content":"new poster"}`),
			toolCall("w2", "write_file", `{"filename":"work/draft.txt","content":"draft"}`)}},
		llm.Message{Role: "assistant", Content: "Made poster.png from photo.jpg."},
		llm.Message{Role: "assistant", Content: "Here it is."},
	)
	a.MaxSteps = 6
	sb := a.Registry.Machine
	_ = sb.WriteFile("u", "photo.jpg", []byte("jpeg"))
	_ = sb.WriteFile("u", "other.txt", []byte("someone else's"))
	_ = sb.WriteFile("u", "poster.png", []byte("an older poster"))
	var moved map[string]string
	res, err := a.Do(context.Background(), Request{SessionID: "u", Input: llm.Message{Content: "make a poster"},
		Listener: &EventListener{SubAgent: func(string, string) *EventListener {
			return &EventListener{OnFiles: func(m map[string]string) { moved = m }}
		}}})
	if err != nil || res.Answer != "Here it is." {
		t.Fatalf("%+v %v", res, err)
	}
	listing := ""
	for _, m := range msgs(script.requests[2]) {
		if m["role"] == "tool" {
			listing += m["content"].(string)
		}
	}
	if !strings.Contains(listing, "photo.jpg") || strings.Contains(listing, "other.txt") || strings.Contains(listing, "poster.png") {
		t.Fatalf("the sub-agent sees copies of the files its task names, nothing else: %s", listing)
	}
	if data, _, _ := sb.ReadFile("u", "poster.png", 100); string(data) != "an older poster" {
		t.Fatal("the user's file of the same name stays")
	}
	if data, _, _ := sb.ReadFile("u", "poster-2.png", 100); string(data) != "new poster" || moved["poster.png"] != "poster-2.png" {
		t.Fatalf("its poster comes back under a free name: %q %v", data, moved)
	}
	if _, err := sb.Stat("u", "work/draft.txt"); err == nil {
		t.Fatal("its drafts stay behind")
	}
	if _, err := sb.Stat("u", ".agents"); err == nil {
		if entries, _ := sb.List("u", 3, 10); len(entries) > 0 {
			for _, e := range entries {
				if strings.HasPrefix(e.Name, ".agents/") {
					t.Fatalf("its workspace is removed: %s", e.Name)
				}
			}
		}
	}
	report := fmt.Sprint(msgs(script.requests[4]))
	if !strings.Contains(report, "poster-2.png (it called it poster.png)") {
		t.Fatalf("the report gives the new name: %s", report)
	}
}

func TestASubAgentSendsAGivenFile(t *testing.T) {
	a, _ := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("d1", ToolDelegate,
			`{"goal":"check photo.jpg and send it to the user"}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("s1", "send_file", `{"filename":"./photo.jpg"}`)}},
		llm.Message{Role: "assistant", Content: "Sent photo.jpg."},
		llm.Message{Role: "assistant", Content: "Done."},
	)
	a.MaxSteps = 5
	_ = a.Registry.Machine.WriteFile("u", "photo.jpg", []byte("jpeg"))
	var sent []string
	sendTool := LocalTool{Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "send_file", Description: "send",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"filename": map[string]any{"type": "string"}}}}},
		Handler: func(_ context.Context, args map[string]any) (ToolOutput, error) {
			sent = append(sent, fmt.Sprint(args["filename"]))
			return ToolOutput{Text: "ok"}, nil
		}}
	res, err := a.Do(context.Background(), Request{SessionID: "u", Input: llm.Message{Content: "send me the photo"}, Tools: []LocalTool{sendTool}})
	if err != nil || res.Answer != "Done." {
		t.Fatalf("%+v %v", res, err)
	}
	if len(sent) != 1 || sent[0] != "photo.jpg" {
		t.Errorf("the given file is sent from the parent's workspace: %v", sent)
	}
}
