package main

import (
	"bytes"
	"encoding/base64"

	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

func testJPEG(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, nil)
	return buf.Bytes()
}

func testPNG(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.NRGBA{200, 10, 10, uint8(x % 256)})
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	return buf.Bytes()
}

func group(userID int64, text string) *telegram.Message {
	m := private(userID, text)
	m.Chat = telegram.Chat{ID: -100500, Type: "supergroup", Title: "بچه‌های Go"}
	return m
}

func TestVisionImageShrinksAndFlattens(t *testing.T) {
	url, desc, err := visionImage(testPNG(3000, 1000))
	if err != nil {
		t.Fatal(err)
	}
	if desc != "3000×1000 PNG" {
		t.Errorf("desc = %q", desc)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || format != "jpeg" || cfg.Width != visionMaxSide || cfg.Height != 523 {
		t.Errorf("want a %d px wide JPEG, got %v %s %v", visionMaxSide, cfg, format, err)
	}
	small := testJPEG(64, 64)
	if url, _, _ := visionImage(small); url != dataURL("image/jpeg", small) {
		t.Error("a small JPEG is passed through untouched")
	}
	if _, _, err := visionImage([]byte("print('hi')")); err == nil {
		t.Error("text is not an image")
	}
}

func TestTraceShowsOnlyToolNames(t *testing.T) {
	now := time.Now()
	var tr trace
	if tr.start(0, toolReact, now) || tr.start(0, toolEffect, now) {
		t.Error("reactions and effects are not shown")
	}
	tr.start(0, "web_search", now)
	tr.end(0, "web_search", false)
	tr.start(0, "web_search", now)
	tr.end(0, "web_search", false)
	tr.start(0, "web_fetch", now)
	tr.end(0, "web_fetch", failedResult("fetch error: dial tcp: timeout"))
	tr.start(0, tools.ToolDownloadMedia, now)
	out := tr.render(now.Add(7*time.Second), now, true)
	want := traceTitle + " · step 4 · 0:07\n" + traceRule + "\n✓ Search ×2\n✗ Read Page\n◆ Download ⠇ 7s"
	if out != want {
		t.Errorf("render:\n%s\nwant:\n%s", out, want)
	}
	if g := tr.render(now.Add(7*time.Second), now, false); !strings.HasSuffix(g, "◆ Download …") {
		t.Errorf("groups show no spinner:\n%s", g)
	}
	tr.end(0, tools.ToolDownloadMedia, false)
	if out := tr.render(now.Add(time.Second), now, true); !strings.Contains(out, "✓ Download") || !strings.HasPrefix(out, traceTitle+" ⠼") {
		t.Errorf("idle: the spinner moves to the title:\n%s", out)
	}
	if toolLabel("split_video") != "Split Video" || toolLabel("some_new_tool") != "Some New Tool" || toolLabel(tools.ToolRunCode) != "Run Code" {
		t.Error("labels follow the function names")
	}
	transcript := "$ python3 app.py\nZeroDivisionError: division by zero\n[exit 1 · 31ms]"
	if !failedResult(transcript) || failedResult("$ python3 app.py\nok\n[exit 0 · 3ms]") {
		t.Error("exit codes")
	}
}

func TestTerminalShowsEachStep(t *testing.T) {
	h := newHarness(t)
	_ = h.bot.app.Machine.WriteFile(sessionFor(20), "notes.txt", []byte("hi"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return map[string]any{"role": "assistant", "content": "یه نگاه به فایل‌هات بندازم", "tool_calls": toolCall("list_files", `{}`)["tool_calls"]}, 0
		}
		time.Sleep(900 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "یه فایل notes.txt داری."}, 0
	}
	h.send(private(20, "چه فایل‌هایی دارم؟"))
	all := liveFrames(h)
	joined := strings.Join(all, "\n=====\n")
	for _, want := range []string{"دارم فکر", "مکرون ایجنت", "✓ Files"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the terminal misses %q:\n%s", want, joined)
		}
	}
	for _, hidden := range []string{"یه نگاه به فایل‌هات بندازم", "ls -la", "/workspace", "notes.txt", "💬"} {
		if strings.Contains(joined, hidden) {
			t.Errorf("the terminal shows a detail (%q):\n%s", hidden, joined)
		}
	}
	h.wantText("یه فایل notes.txt داری.")
}

func TestThinkingBubbleWhenTerminalIsOff(t *testing.T) {
	h := newHarness(t)
	_ = h.st.SetSetting(bg, store.SettingLiveTrace, "false")
	h.send(private(21, "سلام"))
	frames := liveFrames(h)
	if len(frames) == 0 || strings.Contains(strings.Join(frames, ""), traceTitle) {
		t.Fatalf("private live status missing: %v", frames)
	}
}

func TestViewImageLetsTheModelCheckItsOutput(t *testing.T) {
	h := newHarness(t)
	_ = h.bot.app.Machine.WriteFile(sessionFor(23), "cat_clean.png", testPNG(2000, 1000))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolViewImage, `{"filenames":["/workspace/cat_clean.png","missing.png"]}`), 0
		}
		return map[string]any{"role": "assistant", "content": "واترمارک کامل پاک شد."}, 0
	}
	h.send(private(23, "واترمارکش رو پاک کردی؟"))
	req := llmText(h.llm.last())
	for _, want := range []string{"image 1 = cat_clean.png (2000×1000 PNG", "missing.png: cannot be opened", "data:image/jpeg;base64,"} {
		if !strings.Contains(req, want) {
			t.Errorf("the model must see its own output image; request misses %q", want)
		}
	}
	h.wantText("واترمارک کامل پاک شد.")
}

func TestSendFileDeliversAFixedScript(t *testing.T) {
	h := newHarness(t)
	_ = h.bot.app.Machine.WriteFile(sessionFor(24), "app_fixed.py", []byte("print(1/1)\n"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall(toolSendFile, `{"filename":"app_fixed.py"}`), 0
		case 1:
			return toolCall(toolSendFile, `{"filename":"../../etc/passwd"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "باگ تقسیم بر صفر بود؛ فایل درست‌شده رو فرستادم."}, 0
	}
	h.send(private(24, "این کد رو درست کن"))
	rich := richAnswers(h)
	if len(rich) != 1 || rich[0].body["m0_name"] != "app_fixed.py" || len(h.tg.of("sendDocument")) != 0 {
		t.Fatalf("the fixed file must be sent once, inside the answer: %v", rich)
	}
	if md := rich[0].body["rich_message"].(map[string]any)["markdown"].(string); !strings.HasSuffix(md, "![](tg://document?id=m0)") {
		t.Errorf("the document goes under the text: %s", md)
	}
	if !strings.Contains(llmText(h.llm.last()), "cannot be sent") {
		t.Error("a path outside the workspace is refused")
	}
}

func TestDeveloperModePromptIsPresentWhenCodeCanRun(t *testing.T) {
	h := newHarness(t)

	h.send(private(25, "یه سوال دارم"))
	req := llmText(h.llm.last())
	if has := h.bot.app.Registry.Has(tools.ToolRunCode); has != strings.Contains(req, "Developer mode") {
		t.Errorf("run_code is %v but the developer mode prompt is %v", has, strings.Contains(req, "Developer mode"))
	}
	if !strings.Contains(req, "view_image") || !strings.Contains(req, "files from messages they reply to") {
		t.Error("the prompt tells the model it can look at its own images")
	}
}

func TestStopWorksWhenTheDraftIDIsUnknown(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(map[string]any, int) (map[string]any, int) {
		close(started)
		<-release
		return map[string]any{"role": "assistant", "content": "late"}, 0
	}
	done := make(chan struct{})
	go func() { h.send(private(26, "یه کار طولانی")); close(done) }()
	<-started
	v, _ := h.bot.runs.Load(int64(26))
	h.bot.drafts.Store(int64(123), draftOwner{userID: 26, chatID: 26, key: v.(*activeRun).key})
	h.bot.handleStop(&telegram.MessageGenerationStopped{Chat: telegram.Chat{ID: 26, Type: "private"}})
	close(release)
	<-done
	h.wantText(textCancelled)
}

func TestHiddenToolsKeepTheStreamedText(t *testing.T) {
	l := &liveView{terminal: true}
	l.setText("Hello")
	l.toolStart(0, toolReact)
	if l.text != "Hello" || l.tracing {
		t.Errorf("a reaction must not reset the answer or open the terminal: %q %v", l.text, l.tracing)
	}
	l.toolStart(0, "web_search")
	if l.text != "" || !l.tracing {
		t.Errorf("a real tool opens the terminal: %q %v", l.text, l.tracing)
	}
	l.toolEnd(0, "web_search", "1. result")
	if out := l.tr.render(time.Now(), time.Now(), false); !strings.Contains(out, "✓ Search") {
		t.Errorf("render: %s", out)
	}
}

func groupMsgIn(chatID, user int64, text string) *telegram.Message {
	m := private(user, text)
	m.Chat = telegram.Chat{ID: chatID, Type: "supergroup", Title: "گروه تست"}
	return m
}
