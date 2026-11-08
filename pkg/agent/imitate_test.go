package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

// A reply that writes a tool call out as text («web_search(…) → …», the
// shape of the work notes a turn keeps) did nothing. In a conversation it
// means the message needs the tools: the request starts over with them.
func TestAConversationThatWritesACallGetsItsTools(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "باشه، الان درستش می‌کنم\nweb_search(query=\"tgs sticker\") → {\n</rfl:parameter>"},
		llm.Message{Role: "assistant", Content: "درست شد"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "بهترش کن"}, Chat: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 2 || slices.Equal(toolNames(script.requests[1]), []string{toolUseTools}) || !slices.Contains(toolNames(script.requests[1]), "web_search") {
		t.Fatalf("the second call has every tool: %d %v", len(script.requests), toolNames(script.requests[len(script.requests)-1]))
	}
	if res.Answer != "درست شد" {
		t.Fatalf("answer: %q", res.Answer)
	}
}

// With its tools offered, a reply that writes its calls out as text is sent
// back once, and what remains of such lines never reaches the user.
func TestCallsWrittenAsTextAreSentBack(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "خروجی آماده‌ست ✅\nweb_search(query=\"x\") → {\"ok\":true}"},
		llm.Message{Role: "assistant", Content: "بازم: web_search(query=\"x\")\nweb_search(query=\"y\") → {}\nاین هم جواب"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "بگرد"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 2 || !strings.Contains(lastContent(script.requests[1]), "wrote tool calls out as text (web_search)") {
		t.Fatalf("sent back: %d %q", len(script.requests), lastContent(script.requests[len(script.requests)-1]))
	}
	if strings.Contains(res.Answer, "web_search(query=\"y\")") || !strings.Contains(res.Answer, "این هم جواب") {
		t.Fatalf("answer: %q", res.Answer)
	}
}

func TestImitatedCalls(t *testing.T) {
	known := map[string]bool{"bash_execute": true, "run_code": true, toolUseTools: true}
	text := "استیکر آماده‌ست 👇\nbash_execute(python3 - << 'EOF' import json) → {\n- run_code(print(1))\n```\nbash_execute(ls) in a code block stays\n```\nprint(x) is not a tool"
	if got := imitatedCalls(text, known); !slices.Equal(got, []string{"bash_execute", "run_code"}) {
		t.Fatalf("found: %v", got)
	}
	want := "استیکر آماده‌ست 👇\n```\nbash_execute(ls) in a code block stays\n```\nprint(x) is not a tool"
	if got := dropImitatedCalls(text, known); got != want {
		t.Fatalf("dropped:\n%q\nwant:\n%q", got, want)
	}
	if imitatedCalls("use_tools()", known) != nil || imitatedCalls("نوشتم bash_execute(x) وسط جمله", known) != nil {
		t.Fatal("only a line that starts with a call")
	}
	if got := scrubMarkup("جواب\n</rfl:parameter>\n<ns:invoke name=\"x\">"); got != "جواب" {
		t.Fatalf("namespaced tags: %q", got)
	}
}

// A call that arrives without a name (a provider's parser that failed:
// «"name":null,"arguments":"{}"») never runs and never goes back in the
// history (the provider would refuse the next request); the model is asked
// for it again.
func TestANamelessCallIsAskedForAgain(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("", "", `{}`)}},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c2", "list_files", `{}`)}},
		llm.Message{Role: "assistant", Content: "فایلی نیست"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "فایل‌هامو ببین"}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Answer != "فایلی نیست" || len(script.requests) != 3 {
		t.Fatalf("answer %q after %d calls", res.Answer, len(script.requests))
	}
	second := msgs(script.requests[1])
	if !strings.Contains(lastContent(script.requests[1]), "without a tool name") {
		t.Fatalf("asked again: %q", lastContent(script.requests[1]))
	}
	for _, m := range append(second, msgs(script.requests[2])...) {
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				if c.(map[string]any)["function"].(map[string]any)["name"] == "" {
					t.Fatal("a nameless call went back to the provider")
				}
			}
		}
	}
}

// A reply that says the work is done when no tool ran («یادآور تنظیم شد»
// from a weak model) is sent back once: it does the work, or says plainly.
func TestAClaimOfWorkNoToolDidIsSentBack(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "فایل notes.txt رو ساختم ✅"},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "write_file", `{"filename":"notes.txt","content":"x"}`)}},
		llm.Message{Role: "assistant", Content: "ساخته شد"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "یه فایل notes.txt بساز"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 3 || !strings.Contains(lastContent(script.requests[1]), "Receipts of this request: none") || res.Answer != "ساخته شد" {
		t.Fatalf("calls %d, answer %q", len(script.requests), res.Answer)
	}
}

func TestClaimsDone(t *testing.T) {
	for _, s := range []string{"یادآور برات تنظیم شد: دوشنبه ساعت ۸", "فایل رو ساختم", "فایل todo.md آماده‌ست", "ذخیره کردم", "I've set a reminder", "The file has been saved"} {
		if !claimsDone(s) {
			t.Errorf("not seen: %q", s)
		}
	}
	for _, s := range []string{"یه شعر برات نوشتم:", "ذخیره نشد، دوباره امتحان کن", "می‌خوای برات یادآور بذارم؟", "سلام! چطوری؟", "اگه بخوای فایل می‌سازم"} {
		if claimsDone(s) {
			t.Errorf("seen in: %q", s)
		}
	}
}

// A reply that asks the user what something is, before any search («ترمکس
// چیه؟») is sent back once: it looks it up and goes on.
func TestAQuestionASearchWouldAnswerIsSentBack(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "ترمکس دقیقاً چیه؟ منظورت کدوم برنامه‌ست؟"},
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "web_search", `{"query":"Termux"}`)}},
		llm.Message{Role: "assistant", Content: "ترمکس یه ترمینال لینوکس برای اندرویده؛ نسخه آخرش اینه…"},
	)
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "ترمکس رو میخوام"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(script.requests) != 3 || !strings.Contains(lastContent(script.requests[1]), "you can find out yourself") || !strings.HasPrefix(res.Answer, "ترمکس یه ترمینال") {
		t.Fatalf("calls %d, answer %q", len(script.requests), res.Answer)
	}
}

func TestAsksFirst(t *testing.T) {
	for _, s := range []string{"ترمکس چیه؟", "منظورت از Snap Decisions چیه؟", "کدوم نسخه رو می‌خوای؟", "لینکش رو بفرست تا دانلود کنم؟",
		"میشه بیشتر توضیح بدی؟ کدوم رو مد نظرته؟", "What do you mean by Tabler?", "Which version do you want?", "Could you clarify which app?"} {
		if !asksFirst(s) {
			t.Errorf("not seen: %q", s)
		}
	}
	for _, s := range []string{"سلام! چطوری؟", "می‌خوای PDF هم بسازم؟", "این جواب کامل بود؟ اگه چیز دیگه‌ای خواستی بگو", "قیمت دلار امروز ۸۵ هزار تومنه.",
		"فایل رو ساختم. چیز دیگه‌ای لازم داری؟", "How are you?", strings.Repeat("یه جواب بلند. ", 60) + "ترمکس چیه؟"} {
		if asksFirst(s) {
			t.Errorf("seen in: %q", s)
		}
	}
}

// A call the provider split in two while streaming («download_file» with half
// its arguments, «filename» with «}») is asked for again without streaming;
// and arguments that are still not JSON never go back to the provider (it
// refuses the next request with them in the history).
func TestABrokenStreamedCallIsAskedForAgain(t *testing.T) {
	broken := &llm.Response{Message: llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{
		toolCall("c1", "download_file", `{"url": "https://a.com/x.svg"`), toolCall("c2", "filename", "}")}}}
	if !brokenCalls(broken) {
		t.Fatal("a call split in two is broken")
	}
	if brokenCalls(&llm.Response{Message: llm.Message{ToolCalls: []llm.ToolCall{toolCall("c1", "list_files", `{}`)}}}) {
		t.Fatal("a whole call is not")
	}
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "list_files", `{"path": "x"`)}},
		llm.Message{Role: "assistant", Content: "فایلی نیست"},
	)
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Role: "user", Content: "فایل‌هامو ببین"}}); err != nil {
		t.Fatal(err)
	}
	for _, m := range msgs(script.requests[len(script.requests)-1]) {
		if calls, ok := m["tool_calls"].([]any); ok {
			for _, c := range calls {
				if args := c.(map[string]any)["function"].(map[string]any)["arguments"].(string); !json.Valid([]byte(args)) {
					t.Fatalf("arguments that are not JSON went back: %q", args)
				}
			}
		}
	}
}

// Once a streamed reply's calls arrive broken, the rest of the request goes
// without streaming: each step costs one call, not a broken one and its retry.
func TestAfterABrokenStreamTheRequestStopsStreaming(t *testing.T) {
	var mu sync.Mutex
	var streamed []bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		n := len(streamed)
		stream, _ := req["stream"].(bool)
		streamed = append(streamed, stream)
		mu.Unlock()
		var msg map[string]any
		switch n {
		case 0: // streamed: a call without a name
			msg = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "x", "type": "function", "function": map[string]any{"name": "", "arguments": "{}"}}}}
		case 1, 2: // the retry and the next step: whole calls
			msg = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": fmt.Sprint("c", n), "type": "function", "function": map[string]any{"name": "list_files", "arguments": "{}"}}}}
		default:
			msg = map[string]any{"role": "assistant", "content": "تموم شد"}
		}
		if stream {
			w.Header().Set("Content-Type", "text/event-stream")
			delta := map[string]any{"role": "assistant"}
			if c, ok := msg["content"]; ok {
				delta["content"] = c
			}
			if tc, ok := msg["tool_calls"].([]any); ok {
				call := tc[0].(map[string]any)
				call["index"] = 0
				delta["tool_calls"] = []any{call}
			}
			b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": delta}}})
			_, _ = w.Write([]byte("data: " + string(b) + "\n\ndata: [DONE]\n\n"))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": msg}}})
	}))
	defer srv.Close()
	a, _ := setup(t, llm.Message{Role: "assistant", Content: "x"})
	a.LLM = llm.NewClient(srv.URL, "k", "m")
	a.MaxSteps = 5
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "فایل‌هامو ببین"},
		Listener: &EventListener{OnText: func(string) {}}})
	if err != nil || res.Answer != "تموم شد" {
		t.Fatalf("%+v %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(streamed) != 4 || !streamed[0] || streamed[1] || streamed[2] || streamed[3] {
		t.Fatalf("streamed: %v (the first, then never again)", streamed)
	}
}

// A model that does not see pictures refuses a request with one in it (a
// view_image of the chart it made, a photo the user sent): the job goes on
// with a line in place of each picture instead of stopping.
func TestAModelWithoutVisionGoesOnWithoutThePictures(t *testing.T) {
	var mu sync.Mutex
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		sent = append(sent, string(body))
		mu.Unlock()
		if strings.Contains(string(body), "image_url") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Model does not support image or multimodal input","type":"invalid_request_error"}}`))
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "عکس رو نمی‌بینم، ولی از متنش…"}}}})
	}))
	defer srv.Close()
	a, _ := setup(t, llm.Message{Role: "assistant", Content: "x"})
	a.LLM = llm.NewClient(srv.URL, "k", "m")
	in := llm.Message{Role: "user", Parts: []llm.Part{llm.TextPart("این چیه؟"), llm.ImagePart("data:image/png;base64,iVBORw0KGgo=")}}
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: in})
	if err != nil || !strings.HasPrefix(res.Answer, "عکس رو نمی‌بینم") {
		t.Fatalf("%+v %v", res, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || strings.Contains(sent[1], "image_url") || !strings.Contains(sent[1], "cannot see pictures") {
		t.Fatalf("the second request goes without the picture: %d", len(sent))
	}
}
