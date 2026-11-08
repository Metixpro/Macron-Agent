package main

import (
	"bytes"
	"encoding/base64"
	"fmt"
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
			img.Set(x, y, color.NRGBA{200, 10, 10, uint8(x % 256)}) // transparent parts too
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

// ---------------------------------------------------------------- unit: vision

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

// ---------------------------------------------------------------- unit: terminal trace

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

// ---------------------------------------------------------------- live terminal

func TestTerminalShowsEachStep(t *testing.T) {
	h := newHarness(t)
	_ = h.bot.app.Machine.WriteFile(sessionFor(20), "notes.txt", []byte("hi"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return map[string]any{"role": "assistant", "content": "یه نگاه به فایل‌هات بندازم", "tool_calls": toolCall("list_files", `{}`)["tool_calls"]}, 0
		}
		time.Sleep(900 * time.Millisecond) // give the draft time to show the finished step
		return map[string]any{"role": "assistant", "content": "یه فایل notes.txt داری."}, 0
	}
	h.send(private(20, "چه فایل‌هایی دارم؟"))
	var all []string
	for _, d := range h.tg.of("sendRichMessageDraft") {
		all = append(all, fmt.Sprint(d.body["rich_message"]))
	}
	joined := strings.Join(all, "\n=====\n")
	for _, want := range []string{"tg-thinking", "MACRON ⟫ live", "✓ Files"} {
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
	drafts := h.tg.of("sendRichMessageDraft")
	if len(drafts) == 0 || !strings.Contains(fmt.Sprint(drafts[0].body["rich_message"]), "tg-thinking") ||
		strings.Contains(fmt.Sprint(drafts[0].body["rich_message"]), traceTitle) {
		t.Errorf("with the terminal off the draft is a thinking bubble: %v", drafts)
	}
}

func TestGroupTraceMessageIsRemovedAfterTheAnswer(t *testing.T) {
	h := newHarness(t)
	h.bot.richOff.Store(true) // a Bot API server without rich messages: the classic trace
	oldDelay, oldEdit := traceMsgDelay, groupEditEvery
	traceMsgDelay, groupEditEvery = 0, 0
	t.Cleanup(func() { traceMsgDelay, groupEditEvery = oldDelay, oldEdit })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("list_files", `{}`), 0
		}
		time.Sleep(900 * time.Millisecond)
		return map[string]any{"role": "assistant", "content": "فضای کاری خالیه."}, 0
	}
	h.send(group(22, "مکرون فایل‌هام چیه؟"))
	var traceID float64
	for _, c := range h.tg.of("sendMessage") {
		if s, _ := c.body["text"].(string); strings.Contains(s, "MACRON ⟫ live") {
			traceID = -1
			if rp, ok := c.body["reply_parameters"].(map[string]any); !ok || rp["message_id"].(float64) != 77 {
				t.Errorf("the trace replies to the user's message: %v", c.body)
			}
		}
	}
	if traceID == 0 {
		t.Fatalf("no trace message in the group:\n%s", h.tg.allText())
	}
	if len(h.tg.of("deleteMessage")) == 0 {
		t.Error("the trace message must be deleted once the answer is sent")
	}
	h.wantText("فضای کاری خالیه.")
}

// ---------------------------------------------------------------- workspace vision and files

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
	rich := h.tg.of("sendRichMessage")
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
	// run_code exists where an interpreter is installed, and the prompt
	// promises it exactly then.
	h.send(private(25, "یه سوال دارم"))
	req := llmText(h.llm.last())
	if has := h.bot.app.Registry.Has(tools.ToolRunCode); has != strings.Contains(req, "Developer mode") {
		t.Errorf("run_code is %v but the developer mode prompt is %v", has, strings.Contains(req, "Developer mode"))
	}
	if !strings.Contains(req, "view_image") || !strings.Contains(req, "files from messages they reply to") {
		t.Error("the prompt tells the model it can look at its own images")
	}
}

// ---------------------------------------------------------------- groups

func TestGroupWakeWord(t *testing.T) {
	h := newHarness(t)
	cases := []struct {
		text, want string
		ok         bool
	}{
		{"مکرون قیمت دلار؟", "قیمت دلار؟", true},
		{"مکرون، یه جوک بگو", "یه جوک بگو", true},
		{"مكرون سلام", "سلام", true}, // Arabic kaf
		{"ماکرون چطوری؟", "چطوری؟", true},
		{"Macron what's up", "what's up", true},
		{"macron: hi", "hi", true},
		{"Macron Ai what's up", "what's up", true},
		{"Macron AI, hi", "hi", true},
		{"مکرون ای‌آی سلام", "سلام", true},
		{"Macron aims high", "aims high", true},
		{"مکرون", textOnlyName, true},
		{"مکرونی پختم امشب", "", false},
		{"سلام مکرون", "", false},
		{"macronutrients matter", "", false},
		{"ویز سلام", "", false}, // the old name
	}
	for _, c := range cases {
		ok, got := h.bot.addressed(group(30, c.text))
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("%q → %v %q, want %v %q", c.text, ok, got, c.ok, c.want)
		}
	}

	h.send(group(31, "مکرون ساعت چنده؟"))
	if h.llm.count() != 1 || !strings.Contains(llmText(h.llm.last()), `"ساعت چنده؟"`) {
		t.Fatalf("the wake word must start a request without the word itself: %s", llmText(h.llm.last()))
	}
	if rich := h.tg.of("sendRichMessage"); len(rich) == 0 || rich[0].body["reply_parameters"] == nil {
		t.Error("in groups the answer replies to the person who asked")
	}
	h.send(group(32, "بچه‌ها کی میاد بیرون؟"))
	if _, err := h.st.User(bg, 32); err == nil {
		t.Error("members who never talked to the bot must not become users")
	}
	if c, err := h.st.Chat(bg, -100500); err != nil || c.Title != "بچه‌های Go" || !c.Active() {
		t.Errorf("a group the bot is used in is recorded: %+v %v", c, err)
	}
}

func TestGroupUsersWorkInParallel(t *testing.T) {
	h := newHarness(t)
	release := make(chan struct{})
	started := make(chan int64, 2)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		text := llmText(req)
		for _, id := range []int64{41, 42} {
			if strings.Contains(text, fmt.Sprintf("Ali%d", id)) {
				started <- id
			}
		}
		<-release
		return map[string]any{"role": "assistant", "content": "باشه"}, 0
	}
	done := make(chan struct{}, 2)
	for _, id := range []int64{41, 42} {
		m := group(id, "مکرون یه کار طولانی")
		m.From.FirstName = fmt.Sprintf("Ali%d", id)
		go func() { h.bot.handleMessage(m); done <- struct{}{} }()
	}
	timeout := time.After(10 * time.Second)
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-timeout:
			t.Fatal("two users of one group must be served at the same time")
		}
	}
	close(release)
	<-done
	<-done
	h.bot.wg.Wait()
}

func TestBotAddedToGroupNotifiesTheOwner(t *testing.T) {
	h := newHarness(t)
	chat := telegram.Chat{ID: -100777, Type: "supergroup", Title: "گروه برنامه‌نویس‌ها", Username: "go_devs"}
	adder := telegram.User{ID: 555, FirstName: "Reza", Username: "reza"}
	_ = h.st.SetSetting(bg, store.SettingGroupApproval, "false") // approvals: approval_test.go
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: chat, From: adder,
		OldChatMember: telegram.ChatMember{Status: "left"}, NewChatMember: telegram.ChatMember{Status: "member"}})

	var notice, hello string
	for _, c := range h.tg.of("sendMessage") {
		text, _ := c.body["text"].(string)
		switch int64(c.body["chat_id"].(float64)) {
		case ownerID:
			notice = text
		case chat.ID:
			hello = text
		}
	}
	for _, want := range []string{"اضافه شد", "گروه برنامه‌نویس‌ها", "-100777", "@go_devs", "57", "Reza", "@reza", "555"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the owner's notice misses %q:\n%s", want, notice)
		}
	}
	if !strings.Contains(hello, "مکرون") {
		t.Errorf("the bot greets the group and explains the wake word: %q", hello)
	}
	c, err := h.st.Chat(bg, chat.ID)
	if err != nil || !c.Active() || c.Members != 57 || c.AddedBy != 555 {
		t.Fatalf("the group is logged: %+v %v", c, err)
	}

	// With a log channel the notices go there instead.
	h.tg.reset()
	_ = h.st.SetSetting(bg, store.SettingLogChat, "-1009999")
	h.bot.handleMembership(&telegram.ChatMemberUpdated{Chat: chat, From: adder,
		OldChatMember: telegram.ChatMember{Status: "member"}, NewChatMember: telegram.ChatMember{Status: "kicked"}})
	sent := h.tg.of("sendMessage")
	if len(sent) != 1 || int64(sent[0].body["chat_id"].(float64)) != -1009999 || !strings.Contains(sent[0].body["text"].(string), "حذف شد") {
		t.Errorf("the removal notice goes to the log chat: %v", sent)
	}
	if c, _ := h.st.Chat(bg, chat.ID); c.Active() || c.LeftAt == nil || c.AddedBy != 555 {
		t.Errorf("the removal is recorded: %+v", c)
	}

	// The admin panel lists it.
	v := h.bot.viewGroups(bg, fa)
	if !strings.Contains(v.html, "خارج‌شده: 1") || !strings.Contains(fmt.Sprint(v.kb), "گروه برنامه‌نویس‌ها") {
		t.Errorf("groups view:\n%s\n%v", v.html, v.kb)
	}
}

func TestOwnerSetsTheLogChannel(t *testing.T) {
	h := newHarness(t)
	h.click(ownerID, ownerID, 9, "a:gr:log")
	h.send(&telegram.Message{MessageID: 80, From: &telegram.User{ID: ownerID, FirstName: "Owner"},
		Chat: telegram.Chat{ID: ownerID, Type: "private"}, Text: "@my_logs"})
	if v, _ := h.st.Setting(store.SettingLogChat); v != "-1001234" {
		t.Fatalf("log chat = %q; sent:\n%s", v, h.tg.allText())
	}
	h.wantText("کانال لاگ تنظیم شد")
	h.click(ownerID, ownerID, 9, "a:gr:logdel")
	if v, ok := h.st.Setting(store.SettingLogChat); ok && v != "" {
		t.Errorf("the log chat is reset to the owners: %q", v)
	}
}

// ---------------------------------------------------------------- stop button

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
	// draft_id arrived in a shape that decoded to 0: the chat still identifies the run.
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
