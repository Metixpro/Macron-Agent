package main

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestToolSkillIsLearnedAndFocuses(t *testing.T) {
	h := newHarness(t)
	id := h.bot.prov.ActiveID()

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("functions.ListFiles", `{}`), 0
		}
		return map[string]any{"role": "assistant", "content": "هیچی نیست"}, 0
	}
	h.send(private(20, "چه فایل‌هایی دارم؟"))
	if s := h.bot.toolSkill(id); s.Calls != 1 || s.Problems != 1 {
		t.Fatalf("learned: %+v", s)
	}
	full := offeredTools(h.llm.last())
	if slices.Contains(full, "more_tools") {
		t.Fatal("too few calls to judge: every tool")
	}

	h.bot.saveToolSkill(bg, id, toolSkill{Calls: 30, Problems: 9})
	h.llm.reply = say("۸۵ هزار تومن")
	h.send(private(20, "قیمت دلار امروز چنده؟"))
	if got := offeredTools(h.llm.last()); slices.Contains(got, "more_tools") || len(got) != len(full) {
		t.Errorf("a slipping model keeps every tool: %d of %d", len(got), len(full))
	}

	h.bot.saveToolSkill(bg, id, toolSkill{Mode: toolModeFull, Calls: 30, Problems: 9})
	h.send(private(20, "قیمت دلار امروز چنده؟"))
	if slices.Contains(offeredTools(h.llm.last()), "more_tools") {
		t.Error("all tools when the admins say so")
	}
	h.bot.saveToolSkill(bg, id, toolSkill{Mode: toolModeFocus})
	h.send(private(20, "قیمت دلار امروز چنده؟"))
	focused := offeredTools(h.llm.last())
	if !slices.Contains(focused, "more_tools") || !slices.Contains(focused, "currency_gold_price") || slices.Contains(focused, "generate_image") ||
		len(focused) >= len(full) {
		t.Errorf("focused when the admins say so: %d of %d: %v", len(focused), len(full), focused)
	}

	h.bot.saveToolSkill(bg, id, toolSkill{Calls: 30, Problems: 9})
	h.click(ownerID, ownerID, 9, "a:pv:"+strconv.FormatInt(id, 10))
	if v := lastOf(h, "editMessageText"); !strings.Contains(v.body["text"].(string), "خودکار: همه ابزارها") ||
		!strings.Contains(v.body["text"].(string), "۷۰٪ فراخوانی درست از ۳۰") || !hasButton(buttons(v), "a:pv:"+strconv.FormatInt(id, 10)+":tools") {
		t.Errorf("the card: %s %v", v.body["text"], buttons(v))
	}
	for _, want := range []string{toolModeFull, toolModeFocus, toolModeAuto} {
		h.click(ownerID, ownerID, 9, "a:pv:"+strconv.FormatInt(id, 10)+":tools")
		if s := h.bot.toolSkill(id); s.Mode != want || s.Calls != 30 {
			t.Errorf("mode %q: %+v", want, s)
		}
	}
}
