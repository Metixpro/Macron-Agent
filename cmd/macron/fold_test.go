package main

import (
	"strings"
	"testing"
)

func TestLongAnswersFold(t *testing.T) {
	h := newHarness(t)
	long := "خلاصه سفر به اصفهان 👇\n\n## روز اول\n\n" + strings.Repeat("میدون نقش جهان و مسجد شیخ لطف‌الله رو ببین. ", 40) +
		"\n\n## روز دوم\n\n" + strings.Repeat("سی‌وسه‌پل و پل خواجو رو عصر برو. ", 40) + "\n\n## روز سوم\n\n" + strings.Repeat("کلیسای وانک و بازار. ", 40)
	h.llm.reply = say(long)
	h.send(private(91, "برنامه سه روزه اصفهان"))
	rich := richAnswers(h)
	if len(rich) != 1 {
		t.Fatalf("one message: %d", len(rich))
	}
	md := h.tg.allText()
	if !strings.Contains(md, "خلاصه سفر به اصفهان") || !strings.Contains(md, "<details open><summary>**روز اول**</summary>") ||
		strings.Count(md, "<details") != 3 {
		t.Errorf("the sections fold, the first open:\n%.600s", md)
	}
	mem, _ := h.bot.mem.Load(bg, 91)
	if strings.Contains(mem.History[len(mem.History)-1].Content, "<details") {
		t.Error("the memory keeps the answer as written")
	}

	h.tg.reset()
	h.llm.reply = say("سلام! چطوری؟")
	h.send(private(92, "سلام"))
	if strings.Contains(h.tg.allText(), "<details") {
		t.Error("a short answer is not folded")
	}
}
