package main

import (
	"strings"
	"testing"
)

// A model that says «stopped, deleted, logged out» and called nothing is sent
// back for its receipts; when it keeps the claim, the user gets the answer
// with a plain warning in front of it, and the memory keeps the warning, so a
// later «why didn't you do it?» has the truth to read.
func TestAnswerThatClaimsWorkNoToolDidIsWarnedAboutAndRemembered(t *testing.T) {
	h := newHarness(t)
	claim := "پروژه متوقف شد، سشن پاک شد.\n۱. پروسس ایجنت kill شد.\n۲. فایل‌های سشن حذف شدند.\nاکانت از سرور خارج شد."
	h.llm.reply = say(claim)
	h.send(private(41, "حله مرسی. سشن از اکانت لاگ اوت کن پروژم آف کن"))
	h.wantText("⚠️ توجه")
	h.wantText("kill شد") // the model's own words are still there, after the warning
	if n := len(h.llm.requests); n < 2 {
		t.Fatalf("the answer was sent back for its receipts first: %d model calls", n)
	}
	if !strings.Contains(llmText(h.llm.requests[1]), "Receipts of this request: none") {
		t.Errorf("the notice says what the receipts are")
	}

	// The next message sees the warning in the conversation.
	h.llm.reply = say("راست می‌گی؛ هیچ ابزاری اجرا نشد.")
	h.tg.reset()
	h.send(private(41, "چرا انجام ندادی؟"))
	if text := llmText(h.llm.last()); !strings.Contains(text, "no tool ran for it") && !strings.Contains(text, "هیچ ابزاری برای انجامش اجرا نشد") {
		t.Errorf("the memory keeps the warning with the answer:\n%s", text)
	}
}
