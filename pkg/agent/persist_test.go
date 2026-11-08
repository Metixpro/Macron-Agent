package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func failingTool() LocalTool {
	return LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{Name: "read_site", Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}},
		Handler: func(ctx context.Context, args map[string]any) (ToolOutput, error) {
			return ToolOutput{Text: "fetch error: HTTP 403 forbidden"}, nil
		},
	}
}

func TestGivingUpAfterAFailureIsSentBackOnce(t *testing.T) {
	a, script := setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "read_site", "{}")}},
		llm.Message{Role: "assistant", Content: "متأسفانه نتونستم صفحه رو بخونم."},
		llm.Message{Role: "assistant", Content: "از نسخه آرشیوشده خوندمش: …"},
	)
	a.MaxSteps = 8
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "این صفحه رو بخون"}, Tools: []LocalTool{failingTool()}})
	if err != nil || res.Answer != "از نسخه آرشیوشده خوندمش: …" {
		t.Fatalf("%q %v", res.Answer, err)
	}
	if len(script.requests) != 3 || lastContent(script.requests[2]) != noticePersist {
		t.Fatalf("one nudge: %d requests", len(script.requests))
	}
}

func TestNoNudgeWithoutAFailureOrBudget(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: "متأسفانه این کار از من برنمیاد."})
	if res, _ := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}}); len(script.requests) != 1 || res.Answer == "" {
		t.Fatal("no failed step, no nudge")
	}
	a, script = setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "read_site", "{}")}},
		llm.Message{Role: "assistant", Content: "I couldn't read it."},
	)
	a.MaxSteps = 8
	if _, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}, Tools: []LocalTool{failingTool()},
		AnswerOnLastStep: true}); err != nil || len(script.requests) != 2 {
		t.Fatalf("fast mode answers at once: %d requests", len(script.requests))
	}
	a, script = setup(t,
		map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "read_site", "{}")}},
		llm.Message{Role: "assistant", Content: "I couldn't read it."},
	)
	a.MaxSteps = 8
	_, _ = a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "x"}, Tools: []LocalTool{failingTool()}})
	if len(script.requests) != 3 {
		t.Fatalf("the nudge comes once, then the answer stands: %d requests", len(script.requests))
	}
}

func TestFailureAndGiveUpDetection(t *testing.T) {
	for out, want := range map[string]bool{"error: x": true, "image generation failed: busy": true, "fetch error: HTTP 403": true,
		"Image created and queued for delivery.": false, "Found 3 results": false,
		"read error: no such file or directory": true, "music search error: busy": true, "write error: disk full": true,
		"Errors in the log are listed below": true, "No errors: the build is clean": false} {
		if failedOutput(out) != want {
			t.Errorf("failedOutput(%q) != %v", out, want)
		}
	}
	for ans, want := range map[string]bool{"نمی‌تونم این کار رو بکنم": true, "I'm unable to open it.": true, "انجام شد ✅": false, "Here it is.": false,
		"هیچ مشکلی پیدا نشده و فایل آماده‌ست": false, "متاسفانه فایل بزرگ بود، پس تیکه‌تیکه‌ش کردم": false, "نتونستم فایل رو باز کنم.": true,
		"Unfortunately it was large, so I split it.": false, "I couldn't reach the site.": true} {
		if givesUp(ans) != want {
			t.Errorf("givesUp(%q) != %v", ans, want)
		}
	}
	if !strings.Contains(noticePersist, "two different ways") {
		t.Fatal("the nudge lets a model that already tried answer")
	}
}

// An empty reply is asked again once, instead of telling the user to resend.
func TestAnEmptyReplyIsAskedAgain(t *testing.T) {
	a, script := setup(t, llm.Message{Role: "assistant", Content: ""}, llm.Message{Role: "assistant", Content: "سلام رفیق 👋"})
	a.MaxSteps = 8
	res, err := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "سلام"}})
	if err != nil || res.Answer != "سلام رفیق 👋" || len(script.requests) != 2 || lastContent(script.requests[1]) != noticeEmpty {
		t.Fatalf("%q %v, %d requests", res.Answer, err, len(script.requests))
	}
}

// "I have no internet" while the tools are offered: sent back once to do it.
func TestSayingItHasNoToolsIsSentBack(t *testing.T) {
	a, script := setup(t,
		llm.Message{Role: "assistant", Content: "متأسفانه من به اینترنت دسترسی ندارم و نمی‌تونم قیمت امروز رو ببینم."},
		llm.Message{Role: "assistant", Content: "دلار امروز …"},
	)
	a.MaxSteps = 8
	res, _ := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "قیمت دلار؟"}})
	if res.Answer != "دلار امروز …" || len(script.requests) != 2 || !strings.Contains(lastContent(script.requests[1]), "web search and pages") {
		t.Fatalf("%q, %d requests", res.Answer, len(script.requests))
	}
	offered := map[string]bool{"web_search": true, "send_file": true}
	for ans, want := range map[string]bool{"I can't browse the web, sorry.": true, "نمی تونم فایل بفرستم": true,
		"به عنوان یه هوش مصنوعی احساسی ندارم": false, "سایت رو باز کردم و خوندم": false,
		"نمی‌تونم عکس بسازم": false} { // no picture tool offered: saying so is right
		if _, got := deniesTools(ans, offered); got != want {
			t.Errorf("deniesTools(%q) != %v", ans, want)
		}
	}
}

// A long job may be sent back twice, when it failed again after the first nudge.
func TestALongJobIsNudgedTwice(t *testing.T) {
	call := map[string]any{"role": "assistant", "tool_calls": []llm.ToolCall{toolCall("c1", "read_site", "{}")}}
	a, script := setup(t,
		call, llm.Message{Role: "assistant", Content: "نتونستم بخونمش."},
		call, llm.Message{Role: "assistant", Content: "بازم نتونستم."},
		llm.Message{Role: "assistant", Content: "از یه منبع دیگه پیداش کردم: …"},
	)
	a.MaxSteps = 20
	res, _ := a.Do(context.Background(), Request{SessionID: "s", Input: llm.Message{Content: "این صفحه رو بخون"}, Tools: []LocalTool{failingTool()}})
	if res.Answer != "از یه منبع دیگه پیداش کردم: …" || len(script.requests) != 5 {
		t.Fatalf("%q, %d requests", res.Answer, len(script.requests))
	}
}
