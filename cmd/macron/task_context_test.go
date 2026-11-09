package main

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func testAPK(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, body := range map[string]string{"AndroidManifest.xml": "<manifest package=\"example.app\"/>", "classes.dex": "dex\n035\x00example", "assets/config.json": "{\"version\":1}"} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func apkMessage(t *testing.T, h *harness, uid, id int64, caption string) *telegram.Message {
	t.Helper()
	data := testAPK(t)
	h.tg.mu.Lock()
	h.tg.files["apk-fixture"] = data
	h.tg.mu.Unlock()
	m := private(uid, "")
	m.MessageID = id
	m.Caption = caption
	m.Document = &telegram.Document{FileID: "apk-fixture", FileName: "sample.apk", MimeType: "application/vnd.android.package-archive", FileSize: int64(len(data))}
	return m
}

func TestAPKAnalysisAfterInstallationKeepsNewFileAsCurrentTarget(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("bash_execute", `{"command":"printf installed > installed.txt"}`), 0
		case 1:
			return map[string]any{"role": "assistant", "content": "Installation finished."}, 0
		case 2:
			return toolCall("extract_archive", `{"filename":"sample.apk","list":true}`), 0
		}
		return map[string]any{"role": "assistant", "content": "The application contains a manifest and DEX bytecode."}, 0
	}
	h.send(private(ownerID, "ابزار قبلی رو نصب کن"))
	h.send(apkMessage(t, h, ownerID, 90, "این اپ رو بررسی کن"))
	h.llm.mu.Lock()
	requests := append([]map[string]any(nil), h.llm.requests...)
	h.llm.mu.Unlock()
	if len(requests) < 4 {
		t.Fatalf("APK inspection did not run: %d calls", len(requests))
	}
	messages := requests[2]["messages"].([]any)
	latest := messages[len(messages)-1].(map[string]any)
	if !strings.Contains(lastContent(latest), "sample.apk") {
		t.Error("current user turn does not identify its own APK")
	}
	if !strings.Contains(llmText(requests[2]), "Installation finished.") {
		t.Error("switching task erased conversation history")
	}
	if all := llmText(requests[3]); !strings.Contains(all, "AndroidManifest.xml") || !strings.Contains(all, "classes.dex") {
		t.Error("APK was not inspected as a ZIP-based application package")
	}
}

func lastContent(message map[string]any) string {
	content, _ := message["content"].(string)
	return content
}

func TestDescriptionAfterQueuedAPKDoesNotSteerOldInstallation(t *testing.T) {
	h := newHarness(t)
	first := private(ownerID, "install previous tool")
	run := newRun(ownerID, first, func() {}, nil, tierOwner)
	h.bot.runs.Store(int64(ownerID), run)
	defer h.bot.runs.Delete(int64(ownerID))
	user := &store.User{ID: ownerID}
	file := apkMessage(t, h, ownerID, 90, "")
	if !h.bot.interjectMode(user, file, "", false) {
		t.Fatal("file was not queued")
	}
	text := followUpMsg(first, 91, "این فایل رو بررسی کن")
	if !h.bot.interjectMode(user, text, text.Text, false) {
		t.Fatal("file description was not accepted")
	}
	if xs := run.take(); len(xs) != 0 {
		t.Fatal("APK description was injected into the unrelated installation")
	}
	xs := run.finish()
	if len(xs) != 1 || xs[0].m.Document == nil || !strings.Contains(xs[0].text, text.Text) {
		t.Fatal("queued APK and its description were split into different turns")
	}
}

func TestFreshAPKDoesNotResumeUnfinishedInstallation(t *testing.T) {
	h := newHarness(t)
	_ = h.st.BeginJob(bg, ownerID, ownerID, 77, "Install old server package", "old-install-marker")
	_ = h.st.InterruptJob(bg, ownerID, store.JobStopped, "")
	h.llm.reply = say("I will inspect this application.")
	h.send(apkMessage(t, h, ownerID, 90, "ادامه تحلیل این فایل"))
	req := llmText(h.llm.last())
	if strings.Contains(req, "[Unfinished job]") || strings.Contains(req, "old-install-marker") {
		t.Fatal("fresh upload resumed an unrelated stopped installation")
	}
}

func TestExplicitNewTopicWhileBusyIsASeparateTurn(t *testing.T) {
	h := newHarness(t)
	first := private(ownerID, "old installation")
	run := newRun(ownerID, first, func() {}, nil, tierOwner)
	h.bot.runs.Store(int64(ownerID), run)
	defer h.bot.runs.Delete(int64(ownerID))
	m := followUpMsg(first, 91, "یه کار دیگه: درباره اپلیکیشن اندروید توضیح بده")
	h.bot.interjectMode(&store.User{ID: ownerID}, m, m.Text, false)
	if xs := run.take(); len(xs) != 0 {
		t.Fatal("a clearly separate task steered the old job")
	}
	if xs := run.finish(); len(xs) != 1 || xs[0].text != m.Text {
		t.Fatal("independent request was not preserved as its own turn")
	}
}

func TestFileExplanationTargetsQueuedFileWithoutChangingFIFO(t *testing.T) {
	first := private(ownerID, "install")
	run := newRun(ownerID, first, func() {}, nil, tierOwner)
	file := private(ownerID, "")
	file.MessageID = 90
	file.Document = &telegram.Document{FileID: "apk", FileName: "sample.apk"}
	run.push(&interjection{m: file, text: "inspect permissions"})
	other := followUpMsg(first, 91, "unrelated queued question")
	run.push(&interjection{m: other, text: other.Text})
	description := followUpMsg(first, 92, "فقط مجوزهای خطرناکش رو بگو")
	description.ReplyToMessage = file
	if !run.describePendingAttachment(description, description.Text) {
		t.Fatal("explicit reply did not reach its queued upload")
	}
	xs := run.finish()
	if len(xs) != 2 || !strings.Contains(xs[0].text, description.Text) || xs[1].text != other.Text {
		t.Fatal("reply lost its file or reordered the queue")
	}
}

func TestQueuedFileDescriptionsDoNotSwallowUnrelatedCorrections(t *testing.T) {
	first := private(ownerID, "install")
	for _, text := range []string{"انگلیسی بنویس و کوتاه باشد", "چی شد؟", "یه کار دیگه: وضعیت وب رو بررسی کن"} {
		t.Run(text, func(t *testing.T) {
			run := newRun(ownerID, first, func() {}, nil, tierOwner)
			file := private(ownerID, "")
			file.Document = &telegram.Document{FileID: "apk", FileName: "sample.apk"}
			run.push(&interjection{m: file})
			if run.describePendingAttachment(followUpMsg(first, 91, text), text) {
				t.Fatal("unrelated text was added to a file request")
			}
			if xs := run.finish(); len(xs) != 1 || xs[0].text != "" {
				t.Fatal("queued file was altered")
			}
		})
	}
}

func TestQueuedAttachmentCanBeExplainedAfterStoppingOldTask(t *testing.T) {
	first := private(ownerID, "old install")
	run := newRun(ownerID, first, func() {}, nil, tierOwner)
	file := private(ownerID, "")
	file.Document = &telegram.Document{FileID: "apk", FileName: "sample.apk"}
	run.push(&interjection{m: file})
	run.stop()
	m := followUpMsg(first, 91, "این فایل رو تحلیل کن")
	if !run.describePendingAttachment(m, m.Text) {
		t.Fatal("stopping the unrelated task detached the queued file from its explanation")
	}
	if xs := run.finish(); len(xs) != 1 || xs[0].text != m.Text {
		t.Fatal("queued attachment explanation was lost")
	}
}

func TestContinueWordInsideNewRequestDoesNotResumeOldJob(t *testing.T) {
	for _, text := range []string{"فرق continue و break در پایتون چیه؟", "Explain continue in Python", "ادامه تحلیل این فایل", "برنامه ای به نام Resume بساز", "درباره رمان ادامه داستان توضیح بده"} {
		if isContinueRequest(text) {
			t.Errorf("new request mistaken for resume: %s", text)
		}
	}
}

func TestSameNamedDocumentsKeepTheirOwnBytesAndReplyTargets(t *testing.T) {
	h := newHarness(t)
	user := &store.User{ID: ownerID}
	h.tg.files["first-copy"] = []byte("first document")
	h.tg.files["second-copy"] = []byte("second document")
	first := private(ownerID, "")
	first.MessageID = 90
	first.Document = &telegram.Document{FileID: "first-copy", FileName: "report.txt"}
	second := private(ownerID, "")
	second.MessageID = 91
	second.Document = &telegram.Document{FileID: "second-copy", FileName: "report.txt"}
	a, _, err := h.bot.incomingDocument(bg, sessionFor(ownerID), first)
	if err != nil {
		t.Fatal(err)
	}
	b, _, err := h.bot.incomingDocument(bg, sessionFor(ownerID), second)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("same-named upload overwrote the previous document")
	}
	for name, want := range map[string]string{a: "first document", b: "second document"} {
		data, _, err := h.bot.app.Machine.ReadFile(sessionFor(ownerID), name, 1024)
		if err != nil || string(data) != want {
			t.Fatalf("wrong uploaded bytes: %s: %s %v", name, data, err)
		}
	}
	reply := private(ownerID, "این فایل قدیمی رو بخون")
	reply.ReplyToMessage = first
	in, err := h.bot.buildInput(bg, user, reply, reply.Text, false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.about, "as "+a+" (") && !strings.Contains(in.about, "as "+a+".") {
		t.Fatalf("old-file reply does not resolve to its own upload: %s", in.about)
	}
	again, _, err := h.bot.incomingDocument(bg, sessionFor(ownerID), second)
	if err != nil || again != b {
		t.Fatal("replaying the same Telegram message created another copy")
	}
}

func TestBusyInstallationThenAPKAndDescriptionRunsExactlyTwoTasks(t *testing.T) {
	h := newHarness(t)
	started, release := make(chan struct{}), make(chan struct{})
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		switch n {
		case 0:
			return toolCall("bash_execute", `{"command":"printf installed > install-check.txt"}`), 0
		case 1:
			close(started)
			<-release
			return map[string]any{"role": "assistant", "content": "Installation finished."}, 0
		case 2:
			return toolCall("extract_archive", `{"filename":"sample.apk","list":true}`), 0
		}
		return map[string]any{"role": "assistant", "content": "The uploaded application has a manifest and DEX bytecode."}, 0
	}
	first := private(ownerID, "نصب ابزار سرور")
	wait := h.background(first)
	<-started
	h.bot.handleMessage(apkMessage(t, h, ownerID, 90, ""))
	h.bot.handleMessage(followUpMsg(first, 91, "این فایل رو بررسی کن"))
	before := h.llm.count()
	close(release)
	wait()
	if before != 2 || h.llm.count() != 4 {
		t.Fatalf("file description leaked into the old job or became an extra task: before=%d after=%d", before, h.llm.count())
	}
	h.llm.mu.Lock()
	requests := append([]map[string]any(nil), h.llm.requests...)
	h.llm.mu.Unlock()
	messages := requests[2]["messages"].([]any)
	current := messages[len(messages)-1].(map[string]any)
	if body := lastContent(current); !strings.Contains(body, "sample.apk") || !strings.Contains(body, "این فایل رو بررسی کن") {
		t.Fatal("APK and explanation did not reach the same current user turn")
	}
	if !strings.Contains(llmText(requests[3]), "AndroidManifest.xml") {
		t.Fatal("queued APK was not inspected")
	}
	if used, _ := h.st.UsedToday(bg, ownerID); used != 2 {
		t.Fatalf("file explanation was billed as another task: %d", used)
	}
}

func TestNamelessAPKStillRoutesAndIdentifiesItsCurrentFile(t *testing.T) {
	h := newHarness(t)
	m := apkMessage(t, h, ownerID, 90, "")
	m.Document.FileName = ""
	in, err := h.bot.buildInput(bg, &store.User{ID: ownerID}, m, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(in.msg.Text(), "file_90.apk") || !strings.Contains(in.about, "Android application package") {
		t.Fatal("APK without a Telegram filename lost its identity")
	}
	if names := routeInput(m, "").Files; len(names) != 1 || names[0] != "attachment.apk" {
		t.Fatal("nameless APK was not exposed to routing")
	}
}

func TestOnlyExplicitReferenceResumesAnOlderJobAfterTopicChange(t *testing.T) {
	j := &store.OpenJob{UserID: ownerID, ChatID: ownerID, MsgID: 77, Later: 2, Request: "old installation", Notes: "older-job-marker"}
	m := private(ownerID, "ادامه بده")
	m.MessageID = 100
	if note, continued := messageJob(j, m, m.Text, false); continued || note != "" {
		t.Fatal("bare continue overrode the latest conversation topic")
	}
	m.ReplyToMessage = &telegram.Message{MessageID: 77, Chat: m.Chat}
	if note, continued := messageJob(j, m, m.Text, false); !continued || !strings.Contains(note, "older-job-marker") {
		t.Fatal("explicit reply could not resume its older job")
	}
	m.ReplyToMessage.MessageID = 88
	if note, continued := messageJob(j, m, m.Text, false); continued || note != "" {
		t.Fatal("reply to a different message resumed an older job")
	}
	if note, continued := messageJob(j, m, m.Text, true); !continued || !strings.Contains(note, "older-job-marker") {
		t.Fatal("explicit resume button did not target its job")
	}
}

func TestConcurrentSameNamedUploadsStayDistinct(t *testing.T) {
	h := newHarness(t)
	h.tg.files["concurrent-a"] = []byte("document A")
	h.tg.files["concurrent-b"] = []byte("document B")
	type result struct {
		name, want string
		err        error
	}
	results := make(chan result, 2)
	for i, id := range []string{"concurrent-a", "concurrent-b"} {
		m := private(ownerID, "")
		m.MessageID = int64(90 + i)
		m.Document = &telegram.Document{FileID: id, FileName: "same.txt"}
		want := []string{"document A", "document B"}[i]
		go func() {
			name, _, err := h.bot.incomingDocument(bg, sessionFor(ownerID), m)
			results <- result{name, want, err}
		}()
	}
	seen := map[string]bool{}
	for range 2 {
		got := <-results
		if got.err != nil {
			t.Fatal(got.err)
		}
		if seen[got.name] {
			t.Fatal("concurrent uploads chose the same target")
		}
		seen[got.name] = true
		data, _, err := h.bot.app.Machine.ReadFile(sessionFor(ownerID), got.name, 1024)
		if err != nil || string(data) != got.want {
			t.Fatalf("concurrent upload lost its data: %s %v", data, err)
		}
	}
}

func TestExternalReferenceDoesNotAttachToQueuedUpload(t *testing.T) {
	for _, kind := range []string{"external reply", "quote only", "story"} {
		t.Run(kind, func(t *testing.T) {
			first := private(ownerID, "old task")
			run := newRun(ownerID, first, func() {}, nil, tierOwner)
			file := private(ownerID, "")
			file.Document = &telegram.Document{FileID: "queued", FileName: "sample.apk"}
			run.push(&interjection{m: file})
			m := followUpMsg(first, 91, "این فایل رو بررسی کن")
			switch kind {
			case "external reply":
				m.ExternalReply = &telegram.ExternalReplyInfo{MessageID: 800}
			case "quote only":
				m.Quote = &telegram.TextQuote{Text: "other reference"}
			case "story":
				m.ReplyToStory = &telegram.Story{ID: 1}
			}
			if run.describePendingAttachment(m, m.Text) {
				t.Fatal("an explicit external reference was rebound to an unrelated queued file")
			}
			if xs := run.finish(); len(xs) != 1 || xs[0].text != "" {
				t.Fatal("queued upload changed")
			}
		})
	}
}
