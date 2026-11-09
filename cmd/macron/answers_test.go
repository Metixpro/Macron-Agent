package main

import (
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func TestFileNotesNeverReachTheUser(t *testing.T) {
	for in, want := range map[string]string{
		"اینم عکست 🌅\n\n(sent files: sunset.png)":             "اینم عکست 🌅",
		"Done.\n\nSent files: a.png, b.png\n\nAnything else?": "Done.\n\nAnything else?",
		"تمومه <files_sent>report.pdf</files_sent>":           "تمومه",
		"گزارش آماده‌ست.\n(فایل‌های ارسال‌شده: report.pdf)":   "گزارش آماده‌ست.",
		"The sent files: none of them are big.":               "The sent files: none of them are big.",
	} {
		if got := scrubFileNotes(in); got != want {
			t.Errorf("scrubFileNotes(%q) = %q, want %q", in, got, want)
		}
	}
	h := []llm.Message{{Role: "user", Content: "(sent files: x)"}, {Role: "assistant", Content: "Here it is.\n\n(sent files: cat.png, dog.png)"}}
	modernizeHistory(h)
	if h[0].Content != "(sent files: x)" || h[1].Content != "Here it is.\n\n<files_sent>cat.png, dog.png</files_sent>" {
		t.Errorf("history: %+v", h)
	}
}

func TestClaimsSent(t *testing.T) {
	for answer, want := range map[string]bool{
		"فایل رو برات فرستادم 📄":                      true,
		"اینم عکس‌هات 👇":                              true,
		"I've sent you the PDF.":                      true,
		"Here's your file, enjoy!":                    true,
		"report.pdf ارسال شد ✅":                       true,
		"قیمت دلار امروز ۱۰۰ هزار تومنه":              false,
		"پیامت رو فرستادم برای ادمین":                 false,
		"این عکس یه غروب رو نشون می‌ده":               false,
		"I couldn't make the video: the site is down": false,
	} {
		if got := claimsSent(answer); got != want {
			t.Errorf("claimsSent(%q) = %v", answer, got)
		}
	}
}

func TestAnAnswerThatClaimsAFileIsCheckedOnce(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("write_file", `{"filename":"notes.md","content":"# Go\n- goroutines"}`), 0
		case 1:
			return map[string]any{"role": "assistant", "content": "فایل یادداشت‌ها رو برات فرستادم 📄"}, 0
		case 2:
			if !strings.Contains(llmText(req), "no file goes to the user") {
				t.Errorf("the model is told what is wrong:\n%s", llmText(req))
			}
			return toolCall(toolSendFile, `{"filename":"notes.md"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "یادداشت‌هات آماده‌ست 📄"}, 0
	}
	h.send(private(63, "یه یادداشت درباره Go بنویس و بده"))
	if h.llm.count() != 4 {
		t.Errorf("model calls: %d", h.llm.count())
	}
	rich := richAnswers(h)
	if len(rich) != 1 || rich[0].body["m0_name"] != "notes.md" {
		t.Fatalf("the file goes with the answer: %v", rich)
	}
	if all := h.tg.allText(); strings.Contains(all, "فرستادم") || strings.Contains(all, "sent files") {
		t.Errorf("the corrected answer is the one sent:\n%s", all)
	}
	mem, _ := h.bot.mem.Load(bg, 63)
	if last := mem.History[len(mem.History)-1].Content; !strings.Contains(last, "<files_sent>notes.md</files_sent>") {
		t.Errorf("the reply is remembered with its files: %q", last)
	}

	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("write_file", `{"filename":"plan.md","content":"x"}`), 0
		case 1:
			return toolCall(toolSendFile, `{"filename":"plan.md"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "فایل رو فرستادم ✅\n\n(sent files: plan.md)"}, 0
	}
	h.llm.mu.Lock()
	h.llm.requests = nil
	h.llm.mu.Unlock()
	h.send(private(64, "یه برنامه بنویس و بفرست"))
	if h.llm.count() != 3 {
		t.Errorf("a true claim is not sent back: %d model calls", h.llm.count())
	}
	if all := h.tg.allText(); strings.Contains(all, "sent files") {
		t.Errorf("the copied note never shows:\n%s", all)
	}
}

func TestALostFileIsNotRememberedAsSent(t *testing.T) {
	h := newHarness(t)
	h.tg.mu.Lock()
	h.tg.refuseRichFiles = true
	h.tg.fail["sendDocument"] = 500
	h.tg.fail["sendMediaGroup"] = 500
	h.tg.mu.Unlock()
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("write_file", `{"filename":"report.md","content":"# report"}`), 0
		case 1:
			return toolCall(toolSendFile, `{"filename":"report.md"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "گزارش آماده‌ست 📄"}, 0
	}
	h.send(private(65, "یه گزارش بنویس و بفرست"))
	mem, _ := h.bot.mem.Load(bg, 65)
	if len(mem.History) == 0 {
		t.Fatal("the turn is remembered")
	}
	if last := mem.History[len(mem.History)-1].Content; strings.Contains(last, "files_sent") {
		t.Errorf("a file that failed is not noted as sent: %q", last)
	}
}

func TestTwoRunsInOneWorkspaceKeepTheirFiles(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	live, task := newFileTracker(sb, "tg-1"), newFileTracker(sb, "tg-1")
	live.start("search_image")
	task.start("search_image")
	_ = sb.WriteFile("tg-1", "task-chart.png", []byte("png"))
	task.end("search_image", "Image saved: task-chart.png")
	_ = sb.WriteFile("tg-1", "cat.png", []byte("png"))
	live.end("search_image", "Image saved: cat.png")
	if strings.Join(live.auto, ",") != "cat.png" {
		t.Errorf("the live job sends only its own picture: %v", live.auto)
	}
	if strings.Join(task.auto, ",") != "task-chart.png" {
		t.Errorf("the task sends only its own: %v", task.auto)
	}

	live.start("search_image")
	_ = sb.WriteFile("tg-1", "unnamed.png", []byte("png"))
	live.end("search_image", "done")
	if !strings.Contains(strings.Join(live.auto, ","), "unnamed.png") {
		t.Errorf("a call alone in the workspace owns its changes: %v", live.auto)
	}
}

func TestCopiedMemoryNotesNeverReachTheUser(t *testing.T) {
	for in, want := range map[string]string{
		"گزارش آماده شد.\n\n[sent file: report.pdf]":                                                 "گزارش آماده شد.",
		"اینم نتیجه [the user sent a file: a.pdf] 👇":                                                 "اینم نتیجه  👇",
		"Done.\n[sent a photo: photo_77.jpg]\n[the user sent an audio file: x.mp3]":                  "Done.",
		"[the user stopped this request before I finished it]\nادامه دادم.":                          "ادامه دادم.",
		"[sent a sticker 😀] سلام":                                                                    "سلام",
		"ok\n[the user sent a voice message: v.ogg] what they said in it":                            "ok",
		"ok\n\n[messages the user sent while I was working on it]\n- add a chart\n\nبعدش":            "ok\n\nبعدش",
		"ok\n\n[work log before stopping — the job is not finished]\n- web_search x\n- write_file y": "ok",
		"Use [brackets] for options.":                                                                "Use [brackets] for options.",
		"```go\nlog.Printf(\"[sent file: %s]\", name)\n```":                                          "```go\nlog.Printf(\"[sent file: %s]\", name)\n```",
		"the format is `[sent a photo: x.jpg]` exactly":                                              "the format is `[sent a photo: x.jpg]` exactly",
	} {
		if got := scrubFileNotes(in); got != want {
			t.Errorf("scrubFileNotes(%q) = %q, want %q", in, got, want)
		}
	}
	none := fileClaimCheck(func(string) []string { return nil }, nil)
	if none("گزارش آماده شد.\n\n[sent file: report.pdf]") != noticeNoFileSent {
		t.Error("a copied «sent file» claim with no file goes back to the model")
	}
	if none("ممنون از عکست!\n[the user sent a photo: p.jpg]") != "" {
		t.Error("a copied note about the user's own file is only scrubbed")
	}
	if none("```\n[sent file: x]\n```") != "" {
		t.Error("code is not a claim")
	}
	attached := fileClaimCheck(func(string) []string { return []string{"report.pdf"} }, nil)
	if attached("گزارش آماده شد.\n\n[sent file: report.pdf]") != "" {
		t.Error("with the file attached, the note is only scrubbed")
	}
	for in, want := range map[string]string{
		"Done. [the user se": "Done. ", "Done. [se": "Done. ", "Done. [sent fi": "Done. ", "see [1": "see [1", "a [link](x": "a [link](x", "ok": "ok",
	} {
		if got := streamSafe(in); got != want {
			t.Errorf("streamSafe(%q) = %q, want %q", in, got, want)
		}
	}
	if aOrAn("audio file") != "an audio file" || aOrAn("video") != "a video" {
		t.Error("aOrAn")
	}
}
