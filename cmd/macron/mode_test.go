package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
)

func offeredTools(req map[string]any) []string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, tl := range tools {
		names = append(names, tl.(map[string]any)["function"].(map[string]any)["name"].(string))
	}
	return names
}

func TestFastMode(t *testing.T) {
	h := newHarness(t)
	h.send(private(90, "/mode"))
	h.wantText("حالت پاسخ")
	h.wantText("🧠 عمیق")
	h.click(90, 90, 600, "mode:set:fast")
	if v, _ := h.st.UserPref(bg, 90, store.PrefMode); v != modeFast {
		t.Fatalf("stored in user_prefs: %q", v)
	}

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("web_search", `{"query":"قیمت دلار"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "دلار امروز حدود ۹۰ هزار تومنه."}, 0
	}
	h.send(private(90, "دلار چنده؟"))
	h.wantText("دلار امروز حدود ۹۰ هزار تومنه.")
	h.llm.mu.Lock()
	first, last := h.llm.requests[0], h.llm.requests[len(h.llm.requests)-1]
	h.llm.mu.Unlock()
	tools := offeredTools(first)
	for _, name := range tools {
		if !fastTools[name] {
			t.Errorf("a heavy tool is offered in fast mode: %s", name)
		}
	}
	if !strings.Contains(strings.Join(tools, ","), "web_search") {
		t.Errorf("web search stays: %v", tools)
	}
	if !strings.Contains(llmText(first), "[Fast mode]") || !strings.Contains(llmText(first), "up to 2 tool rounds") {
		t.Error("the model is told to be quick, with a two-step budget")
	}
	if last["tools"] != nil {
		t.Error("the second step answers without tools")
	}
	// No button under the answer: the mode is chosen with /mode.
	rich := h.tg.of("sendRichMessage")
	if kb := rich[len(rich)-1].body["reply_markup"]; kb != nil {
		t.Fatalf("no mode button under answers: %v", kb)
	}
	h.send(private(90, "/deep"))
	if h.bot.fastMode(bg, 90) {
		t.Error("/deep switches back")
	}
	if !strings.Contains(h.tg.allText(), "✓ 🧠 عمیق") && !strings.Contains(fmt.Sprint(h.tg.of("sendMessage")), "✓ 🧠") {
		t.Error("/deep shows the mode message with the new choice")
	}

	// Deep mode: the full agent again.
	h.llm.reply = say("باشه.")
	h.send(private(90, "یه سایت بساز"))
	if tools := strings.Join(offeredTools(h.llm.last()), ","); !strings.Contains(tools, "write_file") {
		t.Errorf("deep mode offers every tool: %s", tools)
	}

	// A button left under an older answer only removes itself.
	h.click(91, -100500, 701, "mode:t:90")
	if h.bot.fastMode(bg, 91) {
		t.Error("an old answer button changes nothing")
	}
	if e := h.tg.of("editMessageReplyMarkup"); len(e) == 0 || !strings.Contains(fmt.Sprint(e[len(e)-1].body["reply_markup"]), "inline_keyboard:[]") {
		t.Errorf("the old button is removed: %v", e)
	}
}

func TestAnswersCarryNoModeButton(t *testing.T) {
	h := newHarness(t)
	h.send(group(92, "مکرون سلام"))
	h.send(private(92, "سلام"))
	for _, m := range []string{"sendRichMessage", "editMessageText"} {
		for _, c := range h.tg.of(m) {
			if strings.Contains(fmt.Sprint(c.body["reply_markup"]), "mode:") {
				t.Errorf("%s carries a mode button: %v", m, c.body["reply_markup"])
			}
		}
	}
}
